package files

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/omni-store/omnistore/internal/auth"
	"github.com/omni-store/omnistore/internal/locks"
	"github.com/omni-store/omnistore/internal/models"
	"github.com/omni-store/omnistore/internal/security"
)

var ErrTrashNotFound = errors.New("回收站条目不存在")

type trashFile struct {
	sourceRel string
	suffix    string
	size      int64
	mtimeNano int64
}

type trashPlan struct {
	sourceRel string
	sourceAbs string
	isDir     bool
	files     []trashFile
	total     int64
}

// MoveToTrash 把文件或目录移动到系统数据目录，并保留台账归属和图床记录。
func (s *Service) MoveToTrash(src *models.StorageSource, relInput string, actorUserID int64) (*models.TrashEntry, error) {
	releaseLifecycle, err := s.guardLifecycle([]*models.StorageSource{src}, &actorUserID)
	if err != nil {
		return nil, err
	}
	defer releaseLifecycle()
	relPath, absPath, err := s.prepare(src, relInput)
	if err != nil {
		return nil, err
	}
	if relPath == "" {
		return nil, fmt.Errorf("%w: 不能删除存储源根目录", ErrInvalid)
	}
	releasePersistent, err := s.persistentLocks.GuardMutation(context.Background(), src.ID,
		[]locks.MutationScope{{Path: relPath, IncludeDescendants: true}}, nil, &actorUserID)
	if err != nil {
		return nil, err
	}
	defer releasePersistent()
	unlock := s.locks.LockPair(locks.Key(src.Key, relPath), locks.Key("trash", relPath))
	defer unlock()

	plan, err := s.inspectTrashSource(src, relPath, absPath)
	if err != nil {
		return nil, err
	}
	trashKey := auth.NewRandomToken("trh-", 12)
	payloadAbs := s.trashPayloadPath(trashKey)
	op := trashOperation{
		Version: trashOperationVersion, Kind: trashOperationMove, TrashKey: trashKey,
		StorageSourceID: src.ID, SourceRelativePath: relPath,
	}
	if err := s.writeTrashOperation(op); err != nil {
		return nil, err
	}
	removeOperationOnReturn := true
	defer func() {
		if removeOperationOnReturn {
			_ = s.removeTrashOperation(trashKey)
		}
	}()
	if err := os.MkdirAll(filepath.Dir(payloadAbs), 0o700); err != nil {
		return nil, fmt.Errorf("创建回收站目录失败: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	entryType := TypeFile
	if plan.isDir {
		entryType = TypeDir
	}
	if _, err := tx.Exec(`INSERT INTO trash_entries
  (trash_key, storage_source_id, original_relative_path, entry_type, file_count, size, deleted_by_user_id, deleted_at)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, trashKey, src.ID, relPath, entryType, len(plan.files), plan.total, actorUserID, now); err != nil {
		return nil, err
	}
	if err := s.moveRecordsToTrashTx(tx, src, trashKey, plan, actorUserID, now); err != nil {
		return nil, err
	}
	if err := s.AdjustSourceUsageTx(tx, src.ID, -plan.total); err != nil {
		return nil, err
	}
	destinationReady, err := moveFilesystemTreeTracked(plan.sourceAbs, payloadAbs, func() error {
		return s.markTrashOperationDestinationReady(trashKey)
	})
	if err != nil {
		if destinationReady {
			removeOperationOnReturn = false
		} else {
			_ = os.RemoveAll(filepath.Dir(payloadAbs))
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		if rollbackErr := moveFilesystemTree(payloadAbs, plan.sourceAbs); rollbackErr != nil {
			removeOperationOnReturn = false
			return nil, errors.Join(err, fmt.Errorf("回滚回收站文件失败: %w", rollbackErr))
		}
		if cleanupErr := os.RemoveAll(filepath.Dir(payloadAbs)); cleanupErr != nil {
			removeOperationOnReturn = false
			return nil, errors.Join(err, fmt.Errorf("清理回收站目录失败: %w", cleanupErr))
		}
		return nil, err
	}
	if err := releasePersistent(relPath); err != nil {
		return nil, err
	}
	return &models.TrashEntry{
		Key: trashKey, StorageSourceID: src.ID, SourceKey: src.Key, SourceName: src.Name,
		OriginalRelativePath: relPath, Name: path.Base(relPath), EntryType: entryType,
		FileCount: int64(len(plan.files)), Size: plan.total, DeletedByUserID: actorUserID, DeletedAt: now,
	}, nil
}

func (s *Service) inspectTrashSource(src *models.StorageSource, relPath, absPath string) (*trashPlan, error) {
	matcher, err := s.sources.Matcher(src.ID)
	if err != nil {
		return nil, err
	}
	rootInfo, err := os.Lstat(absPath)
	if os.IsNotExist(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || (!rootInfo.IsDir() && !rootInfo.Mode().IsRegular()) {
		return nil, ErrUnsupported
	}
	plan := &trashPlan{sourceRel: relPath, sourceAbs: absPath, isDir: rootInfo.IsDir()}
	if rootInfo.Mode().IsRegular() {
		plan.files = append(plan.files, trashFile{sourceRel: relPath, size: rootInfo.Size(), mtimeNano: rootInfo.ModTime().UnixNano()})
		plan.total = rootInfo.Size()
		return plan, nil
	}
	err = filepath.WalkDir(absPath, func(childAbs string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		suffix, err := filepath.Rel(absPath, childAbs)
		if err != nil {
			return err
		}
		if suffix == "." {
			suffix = ""
		} else {
			suffix = filepath.ToSlash(suffix)
		}
		childRel := relPath
		if suffix != "" {
			childRel += "/" + suffix
		}
		if matcher.MatchPrefix(childRel) {
			return ErrPathExcluded
		}
		if security.IsManagedNamespaceName(entry.Name()) {
			// 回收站作用于整棵子树，不能把托管目录搬离其所属来源。
			return fmt.Errorf("%w: 目标包含系统托管目录 %s，拒绝整体操作", ErrInvalid, entry.Name())
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) || isInternalName(entry.Name()) {
			return ErrUnsupported
		}
		if info.Mode().IsRegular() {
			plan.files = append(plan.files, trashFile{sourceRel: childRel, suffix: suffix, size: info.Size(), mtimeNano: info.ModTime().UnixNano()})
			plan.total += info.Size()
		}
		return nil
	})
	return plan, err
}

func (s *Service) moveRecordsToTrashTx(tx *sql.Tx, src *models.StorageSource, trashKey string, plan *trashPlan, actorUserID int64, now time.Time) error {
	for _, item := range plan.files {
		internalRel := trashKey
		if item.suffix != "" {
			internalRel += "/" + item.suffix
		}
		result, err := tx.Exec(`UPDATE file_records SET relative_path = ?, record_status = ?, trash_key = ?,
  updated_by_user_id = ?, updated_at = ? WHERE storage_source_id = ? AND relative_path = ? AND record_status = ?`,
			internalRel, models.FileRecordTrash, trashKey, actorUserID, now, src.ID, item.sourceRel, models.FileRecordActive)
		if err != nil {
			return err
		}
		if changed, _ := result.RowsAffected(); changed == 0 {
			if _, err := tx.Exec(`INSERT INTO file_records
  (storage_source_id, relative_path, size, owner_type, updated_by_user_id, mtime_unix_nano,
   record_status, trash_key, created_at, updated_at)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, src.ID, internalRel, item.size, models.FileOwnerUnowned,
				actorUserID, item.mtimeNano, models.FileRecordTrash, trashKey, now, now); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`UPDATE images SET trash_key = ? WHERE storage_source_id = ? AND `+relativePathSubtreeSQL,
		appendRelativePathSubtreeArgs([]any{trashKey, src.ID}, plan.sourceRel)...); err != nil {
		return err
	}
	_, err := tx.Exec(`UPDATE file_shares SET trash_key = ? WHERE storage_source_id = ? AND trash_key IS NULL AND `+
		relativePathSubtreeSQL, appendRelativePathSubtreeArgs([]any{trashKey, src.ID}, plan.sourceRel)...)
	return err
}

// ListTrash 返回来源回收站；普通用户只看到自己删除的条目，管理员可看到全部。
func (s *Service) ListTrash(src *models.StorageSource, userID int64, admin bool) ([]*models.TrashEntry, error) {
	query := `SELECT t.trash_key, t.storage_source_id, s.key, s.name, t.original_relative_path,
  t.entry_type, t.file_count, t.size, t.deleted_by_user_id, t.deleted_at
  FROM trash_entries t JOIN storage_sources s ON s.id = t.storage_source_id
  WHERE t.storage_source_id = ?`
	args := []any{src.ID}
	if !admin {
		query += ` AND t.deleted_by_user_id = ?`
		args = append(args, userID)
	}
	query += ` ORDER BY t.deleted_at DESC, t.id DESC LIMIT 500`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []*models.TrashEntry
	for rows.Next() {
		entry := &models.TrashEntry{}
		if err := rows.Scan(&entry.Key, &entry.StorageSourceID, &entry.SourceKey, &entry.SourceName,
			&entry.OriginalRelativePath, &entry.EntryType, &entry.FileCount, &entry.Size,
			&entry.DeletedByUserID, &entry.DeletedAt); err != nil {
			return nil, err
		}
		entry.Name = path.Base(entry.OriginalRelativePath)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Service) GetTrash(src *models.StorageSource, trashKey string) (*models.TrashEntry, error) {
	entry := &models.TrashEntry{}
	err := s.db.QueryRow(`SELECT t.trash_key, t.storage_source_id, s.key, s.name, t.original_relative_path,
  t.entry_type, t.file_count, t.size, t.deleted_by_user_id, t.deleted_at
  FROM trash_entries t JOIN storage_sources s ON s.id = t.storage_source_id
  WHERE t.storage_source_id = ? AND t.trash_key = ?`, src.ID, trashKey).Scan(
		&entry.Key, &entry.StorageSourceID, &entry.SourceKey, &entry.SourceName,
		&entry.OriginalRelativePath, &entry.EntryType, &entry.FileCount, &entry.Size,
		&entry.DeletedByUserID, &entry.DeletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTrashNotFound
	}
	if err != nil {
		return nil, err
	}
	entry.Name = path.Base(entry.OriginalRelativePath)
	return entry, nil
}

// RestoreTrash 恢复到原路径或显式目标路径；目标已存在时拒绝。
func (s *Service) RestoreTrash(src *models.StorageSource, trashKey, targetInput string, actorUserID int64) (*models.TrashEntry, error) {
	releaseLifecycle, err := s.guardLifecycle([]*models.StorageSource{src}, &actorUserID)
	if err != nil {
		return nil, err
	}
	defer releaseLifecycle()
	entry, err := s.GetTrash(src, trashKey)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(targetInput) == "" {
		targetInput = entry.OriginalRelativePath
	}
	targetRel, targetAbs, err := s.prepare(src, targetInput)
	if err != nil || targetRel == "" {
		return nil, fmt.Errorf("%w: 非法恢复路径", ErrInvalid)
	}
	releasePersistent, err := s.persistentLocks.GuardMutation(context.Background(), src.ID,
		[]locks.MutationScope{{Path: targetRel, IncludeDescendants: true}}, nil, &actorUserID)
	if err != nil {
		return nil, err
	}
	defer releasePersistent()
	quotaGuard, err := s.BeginQuotaWrite(src, "")
	if err != nil {
		return nil, err
	}
	defer quotaGuard.Close()
	unlock := s.locks.LockPair(locks.Key(src.Key, targetRel), locks.Key("trash", trashKey))
	defer unlock()
	if _, err := os.Lstat(targetAbs); err == nil {
		return nil, ErrAlreadyExists
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	parent, err := os.Lstat(filepath.Dir(targetAbs))
	if err != nil || !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: 目标目录不存在或不可用", ErrInvalid)
	}
	if maxBytes, limited := quotaGuard.MaxBytes(); limited && entry.Size > maxBytes {
		return nil, ErrQuotaExceeded
	}
	payloadAbs := s.trashPayloadPath(trashKey)
	if _, err := os.Lstat(payloadAbs); os.IsNotExist(err) {
		return nil, ErrTrashNotFound
	} else if err != nil {
		return nil, err
	}
	if err := s.validateTrashRestoreTarget(src, payloadAbs, targetRel); err != nil {
		return nil, err
	}
	op := trashOperation{
		Version: trashOperationVersion, Kind: trashOperationRestore, TrashKey: trashKey,
		StorageSourceID: src.ID, RestoreRelativePath: targetRel,
	}
	if err := s.writeTrashOperation(op); err != nil {
		return nil, err
	}
	removeOperationOnReturn := true
	defer func() {
		if removeOperationOnReturn {
			_ = s.removeTrashOperation(trashKey)
		}
	}()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := s.restoreRecordsTx(tx, src, entry, targetRel, actorUserID); err != nil {
		return nil, err
	}
	if err := s.rebaseUserPathsTx(tx, src.ID, entry.OriginalRelativePath, src.ID, targetRel); err != nil {
		return nil, err
	}
	destinationReady, err := moveFilesystemTreeTracked(payloadAbs, targetAbs, func() error {
		return s.markTrashOperationDestinationReady(trashKey)
	})
	if err != nil {
		if destinationReady {
			removeOperationOnReturn = false
		}
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM trash_entries WHERE trash_key = ?`, trashKey); err != nil {
		if rollbackErr := moveFilesystemTree(targetAbs, payloadAbs); rollbackErr != nil {
			removeOperationOnReturn = false
			return nil, errors.Join(err, fmt.Errorf("回滚回收站恢复失败: %w", rollbackErr))
		}
		return nil, err
	}
	if err := s.AdjustSourceUsageTx(tx, src.ID, entry.Size); err != nil {
		if rollbackErr := moveFilesystemTree(targetAbs, payloadAbs); rollbackErr != nil {
			removeOperationOnReturn = false
			return nil, errors.Join(err, fmt.Errorf("回滚回收站恢复失败: %w", rollbackErr))
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		if rollbackErr := moveFilesystemTree(targetAbs, payloadAbs); rollbackErr != nil {
			removeOperationOnReturn = false
			return nil, errors.Join(err, fmt.Errorf("回滚回收站恢复失败: %w", rollbackErr))
		}
		return nil, err
	}
	_ = os.RemoveAll(filepath.Dir(payloadAbs))
	entry.OriginalRelativePath = targetRel
	entry.Name = path.Base(targetRel)
	return entry, nil
}

// validateTrashRestoreTarget 对恢复后的整棵路径重新应用排除规则。
// 条目恢复到其他目录时，原路径合法不代表新位置下的每个子路径仍然合法。
func (s *Service) validateTrashRestoreTarget(src *models.StorageSource, payloadAbs, targetRel string) error {
	matcher, err := s.sources.Matcher(src.ID)
	if err != nil {
		return err
	}
	return filepath.WalkDir(payloadAbs, func(childAbs string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		suffix, err := filepath.Rel(payloadAbs, childAbs)
		if err != nil {
			return err
		}
		childRel := targetRel
		if suffix != "." {
			childRel += "/" + filepath.ToSlash(suffix)
		}
		if matcher.MatchPrefix(childRel) {
			return ErrPathExcluded
		}
		if security.IsManagedNamespaceName(entry.Name()) {
			return fmt.Errorf("%w: 恢复目标包含系统托管目录 %s，拒绝整体操作", ErrInvalid, entry.Name())
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return ErrUnsupported
		}
		return nil
	})
}

func (s *Service) restoreRecordsTx(tx *sql.Tx, src *models.StorageSource, entry *models.TrashEntry, targetRel string, actorUserID int64) error {
	rows, err := tx.Query(`SELECT id, relative_path FROM file_records WHERE storage_source_id = ? AND trash_key = ?`, src.ID, entry.Key)
	if err != nil {
		return err
	}
	type recordPath struct {
		id  int64
		rel string
	}
	var records []recordPath
	for rows.Next() {
		var record recordPath
		if err := rows.Scan(&record.id, &record.rel); err != nil {
			rows.Close()
			return err
		}
		records = append(records, record)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, record := range records {
		suffix := strings.TrimPrefix(record.rel, entry.Key)
		newRel := targetRel + suffix
		var conflict int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM file_records WHERE storage_source_id = ? AND relative_path = ? AND id != ?`,
			src.ID, newRel, record.id).Scan(&conflict); err != nil {
			return err
		}
		if conflict > 0 {
			return ErrAlreadyExists
		}
		if _, err := tx.Exec(`UPDATE file_records SET relative_path = ?, record_status = ?, trash_key = NULL,
  updated_by_user_id = ?, updated_at = ? WHERE id = ?`, newRel, models.FileRecordActive,
			actorUserID, time.Now().UTC(), record.id); err != nil {
			return err
		}
	}
	rows, err = tx.Query(`SELECT id, relative_path FROM images WHERE trash_key = ?`, entry.Key)
	if err != nil {
		return err
	}
	var images []recordPath
	for rows.Next() {
		var image recordPath
		if err := rows.Scan(&image.id, &image.rel); err != nil {
			rows.Close()
			return err
		}
		images = append(images, image)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, image := range images {
		newRel := targetRel + strings.TrimPrefix(image.rel, entry.OriginalRelativePath)
		if _, err := tx.Exec(`UPDATE images SET relative_path = ?, trash_key = NULL WHERE id = ?`, newRel, image.id); err != nil {
			return err
		}
	}
	rows, err = tx.Query(`SELECT id, relative_path FROM file_shares WHERE trash_key = ?`, entry.Key)
	if err != nil {
		return err
	}
	var shares []recordPath
	for rows.Next() {
		var share recordPath
		if err := rows.Scan(&share.id, &share.rel); err != nil {
			rows.Close()
			return err
		}
		shares = append(shares, share)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, share := range shares {
		newRel := targetRel + strings.TrimPrefix(share.rel, entry.OriginalRelativePath)
		if _, err := tx.Exec(`UPDATE file_shares SET relative_path = ?, trash_key = NULL WHERE id = ?`, newRel, share.id); err != nil {
			return err
		}
	}
	return nil
}

// PurgeTrash 永久删除一个回收站条目并释放用户配额。
func (s *Service) PurgeTrash(src *models.StorageSource, trashKey string) error {
	releaseLifecycle, err := s.guardLifecycle([]*models.StorageSource{src}, nil)
	if err != nil {
		return err
	}
	defer releaseLifecycle()
	if _, err := s.GetTrash(src, trashKey); err != nil {
		return err
	}
	unlock := s.locks.Lock(locks.Key("trash", trashKey))
	defer unlock()
	op := trashOperation{
		Version: trashOperationVersion, Kind: trashOperationPurge, TrashKey: trashKey,
		StorageSourceID: src.ID,
	}
	if err := s.writeTrashOperation(op); err != nil {
		return err
	}
	removeOperationOnReturn := true
	defer func() {
		if removeOperationOnReturn {
			_ = s.removeTrashOperation(trashKey)
		}
	}()
	if err := os.RemoveAll(filepath.Dir(s.trashPayloadPath(trashKey))); err != nil {
		removeOperationOnReturn = false
		return fmt.Errorf("永久删除回收站文件失败: %w", err)
	}
	if err := s.purgeTrashMetadata(src.ID, trashKey); err != nil {
		removeOperationOnReturn = false
		return err
	}
	return nil
}

func (s *Service) purgeTrashMetadata(storageSourceID int64, trashKey string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM images WHERE trash_key = ?`, trashKey); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM file_records WHERE trash_key = ?`, trashKey); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM trash_entries WHERE storage_source_id = ? AND trash_key = ?`, storageSourceID, trashKey); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) SourceTrashCount(storageSourceID int64) (int64, error) {
	var count int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM trash_entries WHERE storage_source_id = ?`, storageSourceID).Scan(&count)
	return count, err
}

func (s *Service) UserTrashCount(userID int64) (int64, error) {
	var count int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM trash_entries WHERE deleted_by_user_id = ?`, userID).Scan(&count)
	return count, err
}

func (s *Service) trashPayloadPath(trashKey string) string {
	return filepath.Join(s.trashDir, trashKey, "payload")
}

func moveFilesystemTree(fromAbs, toAbs string) error {
	_, err := moveFilesystemTreeTracked(fromAbs, toAbs, nil)
	return err
}

// moveFilesystemTreeTracked 在跨文件系统复制完整落盘、删除源路径之前调用
// destinationReady。返回的 bool 只在 error != nil 时有意义，表示目标副本
// 已经完整、但源路径可能只删除了一部分，调用方必须保留恢复日志。
func moveFilesystemTreeTracked(fromAbs, toAbs string, destinationReady func() error) (bool, error) {
	if err := os.Rename(fromAbs, toAbs); err == nil {
		return false, nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return false, fmt.Errorf("移动文件失败: %w", err)
	}
	if err := copyFilesystemTree(fromAbs, toAbs); err != nil {
		_ = os.RemoveAll(toAbs)
		return false, err
	}
	if destinationReady != nil {
		if err := destinationReady(); err != nil {
			_ = os.RemoveAll(toAbs)
			return false, fmt.Errorf("记录跨文件系统复制阶段失败: %w", err)
		}
	}
	if err := os.RemoveAll(fromAbs); err != nil {
		return true, fmt.Errorf("清理源路径失败: %w", err)
	}
	return false, nil
}

func copyFilesystemTree(fromAbs, toAbs string) error {
	root, err := os.Lstat(fromAbs)
	if err != nil {
		return err
	}
	if root.Mode()&os.ModeSymlink != 0 || (!root.IsDir() && !root.Mode().IsRegular()) {
		return ErrUnsupported
	}
	if root.Mode().IsRegular() {
		input, err := os.Open(fromAbs)
		if err != nil {
			return err
		}
		_, copyErr := writeViaTemp(filepath.Dir(toAbs), toAbs, input, 0, false)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}
	return filepath.WalkDir(fromAbs, func(childAbs string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		suffix, err := filepath.Rel(fromAbs, childAbs)
		if err != nil {
			return err
		}
		targetAbs := filepath.Join(toAbs, suffix)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return ErrUnsupported
		}
		if info.IsDir() {
			return os.Mkdir(targetAbs, 0o755)
		}
		input, err := os.Open(childAbs)
		if err != nil {
			return err
		}
		_, copyErr := writeViaTemp(filepath.Dir(targetAbs), targetAbs, input, 0, false)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}
