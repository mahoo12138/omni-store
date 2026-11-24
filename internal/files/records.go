package files

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/omni-store/omnistore/internal/models"
	"github.com/omni-store/omnistore/internal/security"
	"github.com/omni-store/omnistore/internal/sources"
)

// ErrSourceInitialization 表示创建来源时写入初始文件台账失败。
var ErrSourceInitialization = errors.New("初始化存储源台账失败")

type scannedFile struct {
	rel       string
	size      int64
	mtimeNano int64
}

// PreparedFileRecord 是已经完成路径、排除规则和真实普通文件校验的台账写入。
// 字段保持包内私有，其他服务只能通过 PrepareFileRecord /
// PrepareManagedFileRecord 构造，避免绕过文件层校验。
type PreparedFileRecord struct {
	storageSourceID int64
	relPath         string
	size            int64
	ownerUserID     *int64
	ownerType       string
	actorUserID     *int64
	mtimeUnixNano   int64
	resourceScope   string
	createdAt       time.Time
}

// ReconcileSource 扫描真实普通文件并校准 active 台账；新发现文件标记为 unowned。
func (s *Service) ReconcileSource(src *models.StorageSource) (*models.ReconcileResult, error) {
	return s.reconcileSource(src, true)
}

// reconcileSourceForRecovery 只供 journal 恢复使用。保留名与当前恢复无关时跳过，
// 避免宿主机同名文件阻断启动；公开校准仍通过 ReconcileSource 明确报错。
func (s *Service) reconcileSourceForRecovery(src *models.StorageSource) (*models.ReconcileResult, error) {
	return s.reconcileSource(src, false)
}

const reconcileBatchSize = 500

func (s *Service) reconcileSource(src *models.StorageSource, rejectReserved bool) (*models.ReconcileResult, error) {
	root, err := security.ResolveInSource(src.RootPath, "")
	if err != nil {
		return nil, err
	}
	matcher, err := s.sources.Matcher(src.ID)
	if err != nil {
		return nil, err
	}
	filesOnDisk, result, err := scanSourceFiles(root, matcher, rejectReserved)
	if err != nil {
		return nil, err
	}

	// 普通校准只处理 file scope；托管台账（image_bed 等）由所属服务维护。
	// 先在只读连接池读取现有快照并计算差异，再按小批次提交写事务，
	// 避免大源 Reconcile 长时间独占 SQLite 写连接阻塞会话校验和上传。
	existing := make(map[string]scannedFile)
	rows, err := s.readDB().Query(`SELECT relative_path, size, mtime_unix_nano FROM file_records
  WHERE storage_source_id = ? AND record_status = ? AND resource_scope = ?`,
		src.ID, models.FileRecordActive, models.FileRecordScopeFile)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var item scannedFile
		if err := rows.Scan(&item.rel, &item.size, &item.mtimeNano); err != nil {
			rows.Close()
			return nil, err
		}
		existing[item.rel] = item
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var (
		toInsert []scannedFile
		toUpdate []scannedFile
		toDelete []string
	)
	for rel, item := range filesOnDisk {
		old, found := existing[rel]
		if !found {
			toInsert = append(toInsert, item)
			continue
		}
		delete(existing, rel)
		if old.size != item.size || old.mtimeNano != item.mtimeNano {
			toUpdate = append(toUpdate, item)
		}
	}
	for rel := range existing {
		if security.ContainsManagedNamespace(rel) {
			// 托管命名空间中的既有台账行（如图床 scope）不属于普通校准范围，
			// 不能因本轮未扫描到而被删除；只有所属服务能处理它们。
			continue
		}
		toDelete = append(toDelete, rel)
	}

	now := time.Now().UTC()
	for start := 0; start < len(toInsert); start += reconcileBatchSize {
		end := min(start+reconcileBatchSize, len(toInsert))
		tx, err := s.db.Begin()
		if err != nil {
			return nil, err
		}
		for _, item := range toInsert[start:end] {
			if err := insertUnownedFileRecord(tx, src.ID, item, now); err != nil {
				_ = tx.Rollback()
				return nil, err
			}
			result.Added++
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}
	for start := 0; start < len(toUpdate); start += reconcileBatchSize {
		end := min(start+reconcileBatchSize, len(toUpdate))
		tx, err := s.db.Begin()
		if err != nil {
			return nil, err
		}
		for _, item := range toUpdate[start:end] {
			if _, err := tx.Exec(`UPDATE file_records SET size = ?, mtime_unix_nano = ?, updated_at = ?
  WHERE storage_source_id = ? AND relative_path = ?`, item.size, item.mtimeNano, now, src.ID, item.rel); err != nil {
				_ = tx.Rollback()
				return nil, err
			}
			result.Updated++
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}
	for start := 0; start < len(toDelete); start += reconcileBatchSize {
		end := min(start+reconcileBatchSize, len(toDelete))
		tx, err := s.db.Begin()
		if err != nil {
			return nil, err
		}
		for _, rel := range toDelete[start:end] {
			if _, err := tx.Exec(`DELETE FROM file_records WHERE storage_source_id = ? AND relative_path = ?`, src.ID, rel); err != nil {
				_ = tx.Rollback()
				return nil, err
			}
			result.Removed++
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}

	if err := s.readDB().QueryRow(`SELECT COUNT(*) FROM file_records
  WHERE storage_source_id = ? AND record_status = ? AND owner_type = ?`,
		src.ID, models.FileRecordActive, models.FileOwnerUnowned).Scan(&result.Unowned); err != nil {
		return nil, err
	}
	// 普通校准同时校准物理用量缓存，收敛外部直接写盘偏差。
	_, _ = s.CalibrateStorageUsage(src)
	return result, nil
}

// CreateSource 扫描已有目录快照，并在一个 SQLite 事务中创建来源、排除规则和初始台账。
func (s *Service) CreateSource(in sources.CreateInput) (*models.StorageSource, *models.ReconcileResult, error) {
	preflightInput := sources.PreflightInput{RootPath: in.RootPath}
	if in.HasPatterns {
		preflightInput.ExcludePatterns = append([]string(nil), in.ExcludePatterns...)
		preflightInput.HasPatterns = true
	}
	preview, err := s.sources.Preflight(preflightInput)
	if err != nil {
		return nil, nil, err
	}
	if !preview.IsEmpty && !in.ImportExisting {
		return nil, nil, sources.ErrExistingConfirmationRequired
	}
	filesOnDisk, result, err := scanSourceFiles(preview.RootPath, security.NewExcludeMatcher(preview.ExcludePatterns), true)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	src, err := s.sources.CreateWithInitializer(in, func(tx *sql.Tx, pending *models.StorageSource, patterns []string) error {
		if pending.RootPath != preview.RootPath || !slices.Equal(patterns, preview.ExcludePatterns) {
			return fmt.Errorf("存储源预检状态已变化，请重新预检")
		}
		for _, item := range filesOnDisk {
			if err := insertUnownedFileRecord(tx, pending.ID, item, now); err != nil {
				return fmt.Errorf("%w: %v", ErrSourceInitialization, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	result.Added = result.ScannedFiles
	result.Unowned = int64(result.ScannedFiles)
	return src, result, nil
}

func scanSourceFiles(root string, matcher *security.ExcludeMatcher, rejectReserved bool) (map[string]scannedFile, *models.ReconcileResult, error) {
	filesOnDisk := make(map[string]scannedFile)
	result := &models.ReconcileResult{}
	err := filepath.WalkDir(root, func(absPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if absPath == root {
			return nil
		}
		rel, err := filepath.Rel(root, absPath)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if security.IsManagedNamespaceName(entry.Name()) {
			// 托管命名空间不导入普通台账，也不阻止校准；其内容由所属服务维护。
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if security.IsReservedName(entry.Name()) {
			if rejectReserved {
				return fmt.Errorf("%w: %s", security.ErrReservedName, rel)
			}
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if matcher.MatchPrefix(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil
		}
		filesOnDisk[rel] = scannedFile{rel: rel, size: info.Size(), mtimeNano: info.ModTime().UnixNano()}
		result.ScannedFiles++
		result.UsageBytes += info.Size()
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("扫描存储源失败: %w", err)
	}
	return filesOnDisk, result, nil
}

func insertUnownedFileRecord(tx *sql.Tx, storageSourceID int64, item scannedFile, now time.Time) error {
	_, err := tx.Exec(`INSERT INTO file_records
  (storage_source_id, relative_path, size, owner_type, mtime_unix_nano, record_status, created_at, updated_at)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, storageSourceID, item.rel, item.size, models.FileOwnerUnowned,
		item.mtimeNano, models.FileRecordActive, now, now)
	return err
}

// RecordFile 对最终普通文件执行 upsert；覆盖写会把所有权转移给本次写入主体。
func (s *Service) RecordFile(src *models.StorageSource, relInput, ownerType string, ownerUserID, actorUserID *int64) error {
	prepared, err := s.PrepareFileRecord(src, relInput, ownerType, ownerUserID, actorUserID)
	if err != nil {
		return err
	}
	return upsertPreparedFileRecord(s.db, prepared)
}

// recordUploadedFileWithDelta 在同一 SQLite 事务内提交文件台账与存储源增量物理用量。
func (s *Service) recordUploadedFileWithDelta(src *models.StorageSource, relInput, ownerType string, ownerUserID, actorUserID *int64, usageDelta int64) error {
	prepared, err := s.PrepareFileRecord(src, relInput, ownerType, ownerUserID, actorUserID)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := upsertPreparedFileRecord(tx, prepared); err != nil {
		return err
	}
	if err := s.AdjustSourceUsageTx(tx, src.ID, usageDelta); err != nil {
		return err
	}
	return tx.Commit()
}

// PrepareFileRecord 校验最终文件并冻结台账所需的真实大小与 mtime。
func (s *Service) PrepareFileRecord(src *models.StorageSource, relInput, ownerType string, ownerUserID, actorUserID *int64) (*PreparedFileRecord, error) {
	relPath, _, err := s.prepare(src, relInput)
	if err != nil {
		return nil, err
	}
	srcRoot, err := s.sources.OpenRoot(src)
	if err != nil {
		return nil, err
	}
	info, err := srcRoot.Lstat(relPath)
	if err != nil {
		return nil, mapRootFSError(err)
	}
	if !info.Mode().IsRegular() {
		return nil, ErrUnsupported
	}
	if ownerType != models.FileOwnerUser && ownerType != models.FileOwnerAnonymous &&
		ownerType != models.FileOwnerSystem && ownerType != models.FileOwnerUnowned {
		return nil, fmt.Errorf("非法文件所有者类型: %s", ownerType)
	}
	if ownerType != models.FileOwnerUser {
		ownerUserID = nil
	}
	return &PreparedFileRecord{
		storageSourceID: src.ID, relPath: relPath, size: info.Size(), ownerUserID: ownerUserID,
		ownerType: ownerType, actorUserID: actorUserID, mtimeUnixNano: info.ModTime().UnixNano(),
		resourceScope: models.FileRecordScopeFile,
		createdAt:     time.Now().UTC(),
	}, nil
}

// PrepareManagedFileRecord 校验受信任服务写入托管命名空间的最终文件，
// 台账以对应的内部作用域（resource_scope）落库；普通校准与 FTS 不处理这些行。
func (s *Service) PrepareManagedFileRecord(src *models.StorageSource, relInput, scopeDir, ownerType string, ownerUserID, actorUserID *int64) (*PreparedFileRecord, error) {
	resourceScope, ok := models.ManagedScopeLedgerValue(scopeDir)
	if !ok {
		return nil, fmt.Errorf("%w: 未登记的托管作用域 %s", ErrInvalid, scopeDir)
	}
	relPath, _, err := s.prepareManaged(src, relInput, scopeDir)
	if err != nil {
		return nil, err
	}
	srcRoot, err := s.sources.OpenRoot(src)
	if err != nil {
		return nil, err
	}
	info, err := srcRoot.Lstat(relPath)
	if err != nil {
		return nil, mapRootFSError(err)
	}
	if !info.Mode().IsRegular() {
		return nil, ErrUnsupported
	}
	if ownerType != models.FileOwnerUser && ownerType != models.FileOwnerAnonymous &&
		ownerType != models.FileOwnerSystem && ownerType != models.FileOwnerUnowned {
		return nil, fmt.Errorf("非法文件所有者类型: %s", ownerType)
	}
	if ownerType != models.FileOwnerUser {
		ownerUserID = nil
	}
	return &PreparedFileRecord{
		storageSourceID: src.ID, relPath: relPath, size: info.Size(), ownerUserID: ownerUserID,
		ownerType: ownerType, actorUserID: actorUserID, mtimeUnixNano: info.ModTime().UnixNano(),
		resourceScope: resourceScope,
		createdAt:     time.Now().UTC(),
	}, nil
}

// RecordPreparedFileTx 把已经校验的最终文件写入调用方事务。
func (s *Service) RecordPreparedFileTx(tx *sql.Tx, prepared *PreparedFileRecord) error {
	if tx == nil || prepared == nil {
		return fmt.Errorf("文件台账事务参数不能为空")
	}
	return upsertPreparedFileRecord(tx, prepared)
}

type fileRecordExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func upsertPreparedFileRecord(exec fileRecordExecer, prepared *PreparedFileRecord) error {
	_, err := exec.Exec(`INSERT INTO file_records
  (storage_source_id, relative_path, size, owner_user_id, owner_type, created_by_user_id,
   updated_by_user_id, mtime_unix_nano, record_status, resource_scope, created_at, updated_at)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  ON CONFLICT(storage_source_id, relative_path) DO UPDATE SET
    size = excluded.size,
    owner_user_id = excluded.owner_user_id,
    owner_type = excluded.owner_type,
    updated_by_user_id = excluded.updated_by_user_id,
    mtime_unix_nano = excluded.mtime_unix_nano,
    record_status = excluded.record_status,
    resource_scope = excluded.resource_scope,
    updated_at = excluded.updated_at`,
		prepared.storageSourceID, prepared.relPath, prepared.size, prepared.ownerUserID,
		prepared.ownerType, prepared.actorUserID, prepared.actorUserID,
		prepared.mtimeUnixNano, models.FileRecordActive, prepared.resourceScope,
		prepared.createdAt, prepared.createdAt)
	return err
}

func (s *Service) deleteFileRecords(storageSourceID int64, relPath string, isDir bool) error {
	if isDir {
		_, err := s.db.Exec(`DELETE FROM file_records
  WHERE storage_source_id = ? AND record_status = ? AND `+relativePathSubtreeSQL,
			appendRelativePathSubtreeArgs([]any{storageSourceID, models.FileRecordActive}, relPath)...)
		return err
	}
	_, err := s.db.Exec(`DELETE FROM file_records WHERE storage_source_id = ? AND relative_path = ? AND record_status = ?`,
		storageSourceID, relPath, models.FileRecordActive)
	return err
}

// UserUsage 返回用户拥有的文件总字节数；回收站仍占用户配额，永久清理后才释放。
func (s *Service) UserUsage(userID int64) (int64, error) {
	var usage sql.NullInt64
	err := s.readDB().QueryRow(`SELECT SUM(size) FROM file_records WHERE owner_user_id = ? AND owner_type = ?`,
		userID, models.FileOwnerUser).Scan(&usage)
	return usage.Int64, err
}

// UserQuota 返回用户拥有文件的台账用量和硬配额。
func (s *Service) UserQuota(user *models.User) (*models.UserQuota, error) {
	usage, err := s.UserUsage(user.ID)
	if err != nil {
		return nil, err
	}
	quota := &models.UserQuota{
		UsageBytes: usage,
		QuotaBytes: user.QuotaBytes,
		Unlimited:  user.QuotaBytes == 0,
	}
	if !quota.Unlimited {
		quota.RemainingBytes = user.QuotaBytes - usage
		if quota.RemainingBytes < 0 {
			quota.RemainingBytes = 0
		}
	}
	return quota, nil
}

// LedgerSourceUsage 返回台账中的存储源 active 文件总字节数。
func (s *Service) LedgerSourceUsage(storageSourceID int64) (int64, error) {
	var usage sql.NullInt64
	err := s.readDB().QueryRow(`SELECT SUM(size) FROM file_records WHERE storage_source_id = ? AND record_status = ?`,
		storageSourceID, models.FileRecordActive).Scan(&usage)
	return usage.Int64, err
}
