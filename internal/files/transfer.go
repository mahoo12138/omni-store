package files

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/omni-store/omnistore/internal/locks"
	"github.com/omni-store/omnistore/internal/models"
	"github.com/omni-store/omnistore/internal/security"
)

// TransferResult 描述一次复制或跨来源移动的结果。
type TransferResult struct {
	Path      string `json:"path"`
	Files     int64  `json:"files"`
	Bytes     int64  `json:"bytes"`
	SourceKey string `json:"source_key"`
	TargetKey string `json:"target_source_key"`
	WasMove   bool   `json:"was_move"`
}

type transferFile struct {
	sourceRel string
	sourceAbs string
	targetRel string
	targetAbs string
	size      int64
}

type transferPlan struct {
	sourceRel string
	sourceAbs string
	targetRel string
	targetAbs string
	isDir     bool
	dirs      []string
	files     []transferFile
	total     int64
}

// Copy 将文件或目录复制到目标存储源；目标已存在时拒绝，不覆盖。
// 复制产生的新文件归当前执行用户所有。
func (s *Service) Copy(source, target *models.StorageSource, fromInput, toInput string, actorUserID *int64) (*TransferResult, error) {
	return s.transfer(source, target, fromInput, toInput, false, actorUserID)
}

// MoveAcrossSources 将文件或目录移动到另一个存储源，并保留已有文件归属和图床公开链接。
func (s *Service) MoveAcrossSources(source, target *models.StorageSource, fromInput, toInput string, actorUserID *int64) (*TransferResult, error) {
	if source.ID == target.ID {
		newRel, err := s.MoveWithLockTokens(source, fromInput, toInput, nil, actorUserID)
		if err != nil {
			return nil, err
		}
		return &TransferResult{Path: newRel, SourceKey: source.Key, TargetKey: target.Key, WasMove: true}, nil
	}
	return s.transfer(source, target, fromInput, toInput, true, actorUserID)
}

func (s *Service) transfer(source, target *models.StorageSource, fromInput, toInput string, move bool, actorUserID *int64) (*TransferResult, error) {
	releaseLifecycle, err := s.guardLifecycle([]*models.StorageSource{source, target}, actorUserID)
	if err != nil {
		return nil, err
	}
	defer releaseLifecycle()
	fromRel, err := security.NormalizeRelPath(fromInput)
	if err != nil || fromRel == "" {
		return nil, fmt.Errorf("%w: 非法源路径", ErrInvalid)
	}
	toRel, err := security.NormalizeRelPath(toInput)
	if err != nil || toRel == "" {
		return nil, fmt.Errorf("%w: 非法目标路径", ErrInvalid)
	}
	if source.ID == target.ID && (fromRel == toRel || strings.HasPrefix(toRel, fromRel+"/")) {
		return nil, fmt.Errorf("%w: 目标不能是源路径本身或其子目录", ErrInvalid)
	}

	mutations := []locks.SourceMutation{{
		StorageSourceID: target.ID,
		Scopes:          []locks.MutationScope{{Path: toRel, IncludeDescendants: true}},
	}}
	if move {
		mutations = append(mutations, locks.SourceMutation{
			StorageSourceID: source.ID,
			Scopes:          []locks.MutationScope{{Path: fromRel, IncludeDescendants: true}},
		})
	}
	releasePersistent, err := s.persistentLocks.GuardMutations(context.Background(), mutations, nil, actorUserID)
	if err != nil {
		return nil, err
	}
	defer releasePersistent(nil)

	var quotaGuard *QuotaWriteGuard
	if move {
		quotaGuard, err = s.BeginQuotaWrite(target, "")
	} else {
		quotaGuard, err = s.BeginQuotaWriteForUser(target, "", actorUserID)
	}
	if err != nil {
		return nil, err
	}
	defer quotaGuard.Close()

	unlock := s.locks.LockPair(locks.Key(source.Key, fromRel), locks.Key(target.Key, toRel))
	defer unlock()

	plan, err := s.buildTransferPlan(source, target, fromRel, toRel)
	if err != nil {
		return nil, err
	}
	if maxBytes, limited := quotaGuard.MaxBytes(); limited && plan.total > maxBytes {
		return nil, ErrQuotaExceeded
	}
	if !move {
		return s.executeCrashSafeCopy(source, target, plan, actorUserID)
	}

	operation := s.newTransferOperation(source.ID, target.ID, plan.sourceRel, plan.targetRel, plan.isDir)
	if err := s.writeTransferOperation(operation); err != nil {
		return nil, fmt.Errorf("记录跨来源移动意图失败: %w", err)
	}
	if err := s.executeTransferCopy(plan); err != nil {
		rollbackErr := s.rollbackTransferTarget(target, plan)
		if rollbackErr == nil {
			_ = s.removeTransferOperation(operation.OperationID)
		}
		return nil, errors.Join(err, rollbackErr)
	}
	if err := s.syncTransferDestination(plan); err != nil {
		rollbackErr := s.rollbackTransferTarget(target, plan)
		if rollbackErr == nil {
			_ = s.removeTransferOperation(operation.OperationID)
		}
		return nil, errors.Join(fmt.Errorf("同步跨来源移动目标失败: %w", err), rollbackErr)
	}
	if err := s.markTransferTargetReady(operation.OperationID); err != nil {
		rollbackErr := s.rollbackTransferTarget(target, plan)
		if rollbackErr == nil {
			_ = s.removeTransferOperation(operation.OperationID)
		}
		return nil, errors.Join(fmt.Errorf("记录跨来源移动目标阶段失败: %w", err), rollbackErr)
	}
	if err := s.syncTransferRecords(source, target, plan, true, actorUserID); err != nil {
		rollbackErr := s.rollbackTransferTarget(target, plan)
		if rollbackErr == nil {
			_ = s.removeTransferOperation(operation.OperationID)
		}
		return nil, errors.Join(fmt.Errorf("更新文件台账失败: %w", err), rollbackErr)
	}
	if err := s.markTransferDatabaseReady(operation.OperationID); err != nil {
		rollbackErr := s.rollbackTransferRecords(source, target, plan)
		targetRollbackErr := s.rollbackTransferTarget(target, plan)
		if rollbackErr == nil && targetRollbackErr == nil {
			_ = s.removeTransferOperation(operation.OperationID)
		}
		return nil, errors.Join(fmt.Errorf("记录跨来源移动数据库阶段失败: %w", err), rollbackErr, targetRollbackErr)
	}
	if err := os.RemoveAll(plan.sourceAbs); err != nil {
		return nil, fmt.Errorf("删除源路径失败，操作将在重启时继续: %w", err)
	}
	if err := syncDirectory(filepath.Dir(plan.sourceAbs)); err != nil {
		return nil, fmt.Errorf("同步源目录失败，操作将在重启时继续: %w", err)
	}
	if err := releasePersistent(map[int64][]string{source.ID: []string{plan.sourceRel}}); err != nil {
		return nil, err
	}
	if err := s.removeTransferOperation(operation.OperationID); err != nil {
		return nil, fmt.Errorf("清理跨来源移动日志失败: %w", err)
	}
	return &TransferResult{
		Path: plan.targetRel, Files: int64(len(plan.files)), Bytes: plan.total,
		SourceKey: source.Key, TargetKey: target.Key, WasMove: true,
	}, nil
}

func (s *Service) executeCrashSafeCopy(source, target *models.StorageSource, plan *transferPlan, actorUserID *int64) (*TransferResult, error) {
	op := s.newCopyOperation(source.ID, target.ID, plan.targetRel, plan.isDir)
	if err := s.writeCopyOperation(op); err != nil {
		return nil, fmt.Errorf("记录复制意图失败: %w", err)
	}
	stagingPlan, err := s.copyStagingPlan(plan, op, target)
	if err != nil {
		_ = s.removeCopyOperation(op.OperationID)
		return nil, err
	}
	rollback := func(removeTarget bool) error {
		rollbackErr := s.rollbackCopyOperation(op, target, removeTarget)
		if rollbackErr == nil {
			_ = s.removeCopyOperation(op.OperationID)
		}
		return rollbackErr
	}
	if err := s.executeTransferCopy(stagingPlan); err != nil {
		return nil, errors.Join(err, rollback(false))
	}
	if err := s.syncTransferDestination(stagingPlan); err != nil {
		return nil, errors.Join(fmt.Errorf("同步复制 staging 失败: %w", err), rollback(false))
	}
	if _, err := os.Lstat(plan.targetAbs); err == nil {
		return nil, errors.Join(ErrAlreadyExists, rollback(false))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.Join(err, rollback(false))
	}
	if err := os.Rename(stagingPlan.targetAbs, plan.targetAbs); err != nil {
		return nil, errors.Join(fmt.Errorf("发布复制目标失败: %w", err), rollback(false))
	}
	if err := syncDirectory(filepath.Dir(plan.targetAbs)); err != nil {
		return nil, errors.Join(fmt.Errorf("同步复制目标目录失败: %w", err), rollback(true))
	}
	if err := s.syncTransferRecords(source, target, plan, false, actorUserID); err != nil {
		return nil, errors.Join(fmt.Errorf("更新复制文件台账失败: %w", err), rollback(true))
	}
	if err := s.markCopyDatabaseReady(op.OperationID); err != nil {
		return nil, errors.Join(fmt.Errorf("记录复制数据库阶段失败: %w", err), rollback(true))
	}
	// 数据与台账均已提交；日志清理失败交给启动恢复，不诱发重复复制。
	_ = s.removeCopyOperation(op.OperationID)
	return &TransferResult{
		Path: plan.targetRel, Files: int64(len(plan.files)), Bytes: plan.total,
		SourceKey: source.Key, TargetKey: target.Key, WasMove: false,
	}, nil
}

func (s *Service) copyStagingPlan(plan *transferPlan, op copyOperation, target *models.StorageSource) (*transferPlan, error) {
	stagingAbs, _, err := s.copyOperationPaths(op, target)
	if err != nil {
		return nil, err
	}
	staged := *plan
	staged.targetRel = op.StagingRelativePath
	staged.targetAbs = stagingAbs
	staged.dirs = make([]string, len(plan.dirs))
	for i, dir := range plan.dirs {
		staged.dirs[i] = stagingAbs + strings.TrimPrefix(dir, plan.targetAbs)
	}
	staged.files = make([]transferFile, len(plan.files))
	for i, item := range plan.files {
		staged.files[i] = item
		staged.files[i].targetRel = op.StagingRelativePath + strings.TrimPrefix(item.targetRel, plan.targetRel)
		staged.files[i].targetAbs = stagingAbs + strings.TrimPrefix(item.targetAbs, plan.targetAbs)
	}
	return &staged, nil
}

func (s *Service) syncTransferDestination(plan *transferPlan) error {
	dirs := map[string]struct{}{filepath.Dir(plan.targetAbs): {}}
	if plan.isDir {
		dirs[plan.targetAbs] = struct{}{}
		for _, dir := range plan.dirs {
			dirs[dir] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(dirs))
	for dir := range dirs {
		ordered = append(ordered, dir)
	}
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, dir := range ordered {
		if err := syncDirectory(dir); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) buildTransferPlan(source, target *models.StorageSource, fromRel, toRel string) (*transferPlan, error) {
	fromRel, fromAbs, err := s.prepare(source, fromRel)
	if err != nil {
		return nil, err
	}
	toRel, toAbs, err := s.prepare(target, toRel)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(toAbs); err == nil {
		return nil, ErrAlreadyExists
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	parent, err := os.Lstat(filepath.Dir(toAbs))
	if err != nil || !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: 目标目录不存在或不可用", ErrInvalid)
	}
	rootInfo, err := os.Lstat(fromAbs)
	if os.IsNotExist(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || (!rootInfo.IsDir() && !rootInfo.Mode().IsRegular()) {
		return nil, ErrUnsupported
	}

	sourceMatcher, err := s.sources.Matcher(source.ID)
	if err != nil {
		return nil, err
	}
	targetMatcher, err := s.sources.Matcher(target.ID)
	if err != nil {
		return nil, err
	}
	plan := &transferPlan{sourceRel: fromRel, sourceAbs: fromAbs, targetRel: toRel, targetAbs: toAbs, isDir: rootInfo.IsDir()}
	if rootInfo.Mode().IsRegular() {
		plan.files = append(plan.files, transferFile{sourceRel: fromRel, sourceAbs: fromAbs, targetRel: toRel, targetAbs: toAbs, size: rootInfo.Size()})
		plan.total = rootInfo.Size()
		return plan, nil
	}

	err = filepath.WalkDir(fromAbs, func(absPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		suffix, err := filepath.Rel(fromAbs, absPath)
		if err != nil {
			return err
		}
		if suffix == "." {
			suffix = ""
		} else {
			suffix = filepath.ToSlash(suffix)
		}
		sourceChild := fromRel
		targetChild := toRel
		if suffix != "" {
			sourceChild += "/" + suffix
			targetChild += "/" + suffix
		}
		if sourceMatcher.MatchPrefix(sourceChild) || targetMatcher.MatchPrefix(targetChild) {
			return ErrPathExcluded
		}
		if security.IsManagedNamespaceName(entry.Name()) {
			// 复制/跨源移动不能把托管子树搬出其所属来源，也不能静默跳过一部分。
			return fmt.Errorf("%w: 目标包含系统托管目录 %s，拒绝整体操作", ErrInvalid, entry.Name())
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return ErrUnsupported
		}
		if info.IsDir() {
			plan.dirs = append(plan.dirs, filepath.Join(toAbs, filepath.FromSlash(suffix)))
			return nil
		}
		if isInternalName(entry.Name()) {
			return nil
		}
		targetAbs := filepath.Join(toAbs, filepath.FromSlash(suffix))
		plan.files = append(plan.files, transferFile{
			sourceRel: sourceChild, sourceAbs: absPath, targetRel: targetChild, targetAbs: targetAbs, size: info.Size(),
		})
		plan.total += info.Size()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return plan, nil
}

func (s *Service) executeTransferCopy(plan *transferPlan) error {
	if plan.isDir {
		for _, dir := range plan.dirs {
			if err := os.Mkdir(dir, 0o755); err != nil {
				return fmt.Errorf("创建目标目录失败: %w", err)
			}
		}
	}
	for _, item := range plan.files {
		input, err := os.Open(item.sourceAbs)
		if err != nil {
			return fmt.Errorf("读取源文件失败: %w", err)
		}
		_, copyErr := writeViaTemp(filepath.Dir(item.targetAbs), item.targetAbs, input, 0, false)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (s *Service) rollbackTransferTarget(target *models.StorageSource, plan *transferPlan) error {
	removeErr := os.RemoveAll(plan.targetAbs)
	var syncErr error
	if removeErr == nil {
		syncErr = syncDirectory(filepath.Dir(plan.targetAbs))
	}
	recordErr := s.deleteFileRecords(target.ID, plan.targetRel, plan.isDir)
	return errors.Join(removeErr, syncErr, recordErr)
}

type transferRecord struct {
	ownerUserID     sql.NullInt64
	ownerType       string
	createdByUserID sql.NullInt64
	createdAt       time.Time
}

func (s *Service) syncTransferRecords(source, target *models.StorageSource, plan *transferPlan, move bool, actorUserID *int64) error {
	records := make(map[string]transferRecord)
	if move {
		rows, err := s.db.Query(`SELECT relative_path, owner_user_id, owner_type, created_by_user_id, created_at
  FROM file_records WHERE storage_source_id = ? AND record_status = ? AND `+relativePathSubtreeSQL,
			appendRelativePathSubtreeArgs([]any{source.ID, models.FileRecordActive}, plan.sourceRel)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var rel string
			var record transferRecord
			if err := rows.Scan(&rel, &record.ownerUserID, &record.ownerType, &record.createdByUserID, &record.createdAt); err != nil {
				rows.Close()
				return err
			}
			records[rel] = record
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if _, err := tx.Exec(`DELETE FROM file_records WHERE storage_source_id = ? AND `+relativePathSubtreeSQL,
		appendRelativePathSubtreeArgs([]any{target.ID}, plan.targetRel)...); err != nil {
		return err
	}
	for _, item := range plan.files {
		info, err := os.Lstat(item.targetAbs)
		if err != nil {
			return err
		}
		record := records[item.sourceRel]
		if !move {
			record = transferRecord{ownerType: models.FileOwnerUnowned, createdAt: now}
			if actorUserID != nil {
				record.ownerType = models.FileOwnerUser
				record.ownerUserID = sql.NullInt64{Int64: *actorUserID, Valid: true}
				record.createdByUserID = record.ownerUserID
			}
		} else if record.ownerType == "" {
			record.ownerType = models.FileOwnerUnowned
			record.createdAt = now
		}
		if _, err := tx.Exec(`INSERT INTO file_records
  (storage_source_id, relative_path, size, owner_user_id, owner_type, created_by_user_id,
   updated_by_user_id, mtime_unix_nano, record_status, created_at, updated_at)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, target.ID, item.targetRel, info.Size(), nullableInt(record.ownerUserID),
			record.ownerType, nullableInt(record.createdByUserID), actorUserID, info.ModTime().UnixNano(),
			models.FileRecordActive, record.createdAt, now); err != nil {
			return err
		}
	}
	if err := s.AdjustSourceUsageTx(tx, target.ID, plan.total); err != nil {
		return err
	}
	if move {
		if _, err := tx.Exec(`DELETE FROM file_records WHERE storage_source_id = ? AND `+relativePathSubtreeSQL,
			appendRelativePathSubtreeArgs([]any{source.ID}, plan.sourceRel)...); err != nil {
			return err
		}
		if err := moveImageRecordsTx(tx, source.ID, target.ID, plan.sourceRel, plan.targetRel); err != nil {
			return err
		}
		if err := moveShareRecordsTx(tx, source.ID, target.ID, plan.sourceRel, plan.targetRel); err != nil {
			return err
		}
		if err := s.rebaseUserPathsTx(tx, source.ID, plan.sourceRel, target.ID, plan.targetRel); err != nil {
			return err
		}
		if err := s.AdjustSourceUsageTx(tx, source.ID, -plan.total); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func nullableInt(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func moveImageRecordsTx(tx *sql.Tx, sourceID, targetID int64, fromRel, toRel string) error {
	rows, err := tx.Query(`SELECT id, relative_path FROM images
  WHERE storage_source_id = ? AND `+relativePathSubtreeSQL,
		appendRelativePathSubtreeArgs([]any{sourceID}, fromRel)...)
	if err != nil {
		return err
	}
	type imagePath struct {
		id  int64
		rel string
	}
	var paths []imagePath
	for rows.Next() {
		var item imagePath
		if err := rows.Scan(&item.id, &item.rel); err != nil {
			rows.Close()
			return err
		}
		paths = append(paths, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range paths {
		newRel := toRel + strings.TrimPrefix(item.rel, fromRel)
		if _, err := tx.Exec(`UPDATE images SET storage_source_id = ?, relative_path = ? WHERE id = ?`, targetID, newRel, item.id); err != nil {
			return err
		}
	}
	return nil
}

func moveShareRecordsTx(tx *sql.Tx, sourceID, targetID int64, fromRel, toRel string) error {
	rows, err := tx.Query(`SELECT id, relative_path FROM file_shares
  WHERE storage_source_id = ? AND trash_key IS NULL AND `+relativePathSubtreeSQL,
		appendRelativePathSubtreeArgs([]any{sourceID}, fromRel)...)
	if err != nil {
		return err
	}
	type sharePath struct {
		id  int64
		rel string
	}
	var paths []sharePath
	for rows.Next() {
		var item sharePath
		if err := rows.Scan(&item.id, &item.rel); err != nil {
			rows.Close()
			return err
		}
		paths = append(paths, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range paths {
		newRel := toRel + strings.TrimPrefix(item.rel, fromRel)
		if _, err := tx.Exec(`UPDATE file_shares SET storage_source_id = ?, relative_path = ? WHERE id = ?`, targetID, newRel, item.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) rollbackTransferRecords(source, target *models.StorageSource, plan *transferPlan) error {
	// 物理删除源失败时，在同一事务中把目标台账、图床与分享定位反向迁回来源。
	// 不能直接删除目标台账后 Reconcile，否则原用户所有权会退化为 unowned。
	tx, txErr := s.db.Begin()
	if txErr == nil {
		txErr = moveFileRecordsTx(tx, target.ID, source.ID, plan.targetRel, plan.sourceRel)
		if txErr == nil {
			txErr = moveImageRecordsTx(tx, target.ID, source.ID, plan.targetRel, plan.sourceRel)
		}
		if txErr == nil {
			txErr = moveShareRecordsTx(tx, target.ID, source.ID, plan.targetRel, plan.sourceRel)
		}
		if txErr == nil {
			txErr = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
	}
	_, err := s.reconcileSourceForRecovery(source)
	if txErr != nil {
		return txErr
	}
	return err
}

func moveFileRecordsTx(tx *sql.Tx, fromSourceID, toSourceID int64, fromRel, toRel string) error {
	rows, err := tx.Query(`SELECT relative_path FROM file_records
  WHERE storage_source_id = ? AND `+relativePathSubtreeSQL,
		appendRelativePathSubtreeArgs([]any{fromSourceID}, fromRel)...)
	if err != nil {
		return err
	}
	var paths []string
	for rows.Next() {
		var relPath string
		if err := rows.Scan(&relPath); err != nil {
			rows.Close()
			return err
		}
		paths = append(paths, relPath)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, relPath := range paths {
		newRel := toRel + strings.TrimPrefix(relPath, fromRel)
		if _, err := tx.Exec(`DELETE FROM file_records WHERE storage_source_id = ? AND relative_path = ?`, toSourceID, newRel); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE file_records SET storage_source_id = ?, relative_path = ?, updated_at = ?
  WHERE storage_source_id = ? AND relative_path = ?`,
			toSourceID, newRel, time.Now().UTC(), fromSourceID, relPath); err != nil {
			return err
		}
	}
	return nil
}
