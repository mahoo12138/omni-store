package files

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/omni-store/omnistore/internal/auth"
	"github.com/omni-store/omnistore/internal/journal"
	"github.com/omni-store/omnistore/internal/managedroot"
	"github.com/omni-store/omnistore/internal/models"
	"github.com/omni-store/omnistore/internal/security"
)

const (
	pathOperationVersion = 1
	pathOperationMove    = "move"
	pathOperationDelete  = "delete"
)

type pathOperation struct {
	Version          int    `json:"version"`
	OperationID      string `json:"operation_id"`
	Kind             string `json:"kind"`
	StorageSourceID  int64  `json:"storage_source_id"`
	FromRelativePath string `json:"from_relative_path"`
	ToRelativePath   string `json:"to_relative_path,omitempty"`
	IsDirectory      bool   `json:"is_directory"`
	ActorUserID      *int64 `json:"actor_user_id,omitempty"`
}

// PathRecoveryResult 描述启动时收敛的同来源移动和永久删除。
type PathRecoveryResult struct {
	CompletedMoves   int `json:"completed_moves"`
	RolledBackMoves  int `json:"rolled_back_moves"`
	CompletedDeletes int `json:"completed_deletes"`
}

func (s *Service) pathOperationsDir() string {
	return filepath.Join(s.sources.DataDir(), "operations", "paths")
}

func (s *Service) pathOperationPath(operationID string) string {
	return filepath.Join(s.pathOperationsDir(), operationID+".json")
}

func (s *Service) pathFilesystemReadyPath(operationID string) string {
	return filepath.Join(s.pathOperationsDir(), operationID+".filesystem-ready")
}

func (s *Service) pathDatabaseReadyPath(operationID string) string {
	return filepath.Join(s.pathOperationsDir(), operationID+".database-ready")
}

func validPathOperationID(value string) bool {
	if !strings.HasPrefix(value, "pth-") || len(value) != len("pth-")+24 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "pth-"))
	return err == nil
}

func validatePathOperation(op pathOperation) error {
	if op.Version != pathOperationVersion || !validPathOperationID(op.OperationID) || op.StorageSourceID <= 0 ||
		(op.Kind != pathOperationMove && op.Kind != pathOperationDelete) {
		return fmt.Errorf("非法路径操作日志")
	}
	from, err := security.NormalizeRelPath(op.FromRelativePath)
	if err != nil || from == "" || from != op.FromRelativePath {
		return fmt.Errorf("非法路径操作源路径")
	}
	if op.Kind == pathOperationMove {
		to, err := security.NormalizeRelPath(op.ToRelativePath)
		if err != nil || to == "" || to != op.ToRelativePath || to == from || strings.HasPrefix(to, from+"/") {
			return fmt.Errorf("非法路径操作目标路径")
		}
	} else if op.ToRelativePath != "" {
		return fmt.Errorf("删除操作不能包含目标路径")
	}
	if op.ActorUserID != nil && *op.ActorUserID <= 0 {
		return fmt.Errorf("非法路径操作用户")
	}
	return nil
}

func (s *Service) newPathOperation(kind string, src *models.StorageSource, fromRel, toRel string, isDir bool, actorUserID *int64) pathOperation {
	return pathOperation{
		Version: pathOperationVersion, OperationID: auth.NewRandomToken("pth-", 12), Kind: kind,
		StorageSourceID: src.ID, FromRelativePath: fromRel, ToRelativePath: toRel,
		IsDirectory: isDir, ActorUserID: actorUserID,
	}
}

func (s *Service) writePathOperation(op pathOperation) error {
	if err := validatePathOperation(op); err != nil {
		return err
	}
	dir := s.pathOperationsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, item := range []string{s.pathOperationPath(op.OperationID), s.pathFilesystemReadyPath(op.OperationID), s.pathDatabaseReadyPath(op.OperationID)} {
		if _, err := os.Lstat(item); err == nil {
			return fmt.Errorf("路径操作 %s 尚未恢复", op.OperationID)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	temp, err := os.CreateTemp(dir, ".operation-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	keepTemp := true
	defer func() {
		_ = temp.Close()
		if keepTemp {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		return err
	}
	if err := json.NewEncoder(temp).Encode(op); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Link(tempPath, s.pathOperationPath(op.OperationID)); err != nil {
		return err
	}
	if err := syncDirectory(dir); err != nil {
		return err
	}
	if err := os.Remove(tempPath); err != nil {
		return err
	}
	keepTemp = false
	return syncDirectory(dir)
}

func decodePathOperation(reader io.Reader) (pathOperation, error) {
	var op pathOperation
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&op); err != nil {
		return pathOperation{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return pathOperation{}, fmt.Errorf("操作日志包含额外 JSON 值")
		}
		return pathOperation{}, err
	}
	return op, validatePathOperation(op)
}

func (s *Service) readPathOperation(absPath string) (pathOperation, error) {
	handle, err := os.Open(absPath)
	if err != nil {
		return pathOperation{}, err
	}
	op, decodeErr := decodePathOperation(handle)
	return op, errors.Join(decodeErr, handle.Close())
}

func (s *Service) markPathFilesystemReady(operationID string) error {
	return createUploadMarker(s.pathFilesystemReadyPath(operationID), s.pathOperationsDir())
}

func (s *Service) markPathDatabaseReady(operationID string) error {
	return createUploadMarker(s.pathDatabaseReadyPath(operationID), s.pathOperationsDir())
}

func (s *Service) removePathOperation(operationID string) error {
	for _, item := range []string{s.pathOperationPath(operationID), s.pathFilesystemReadyPath(operationID), s.pathDatabaseReadyPath(operationID)} {
		if err := os.Remove(item); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return syncDirectory(s.pathOperationsDir())
}

func (s *Service) movePathMetadata(storageSourceID int64, fromRel, toRel string, isDir bool, actorUserID *int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := moveImageRecordsTx(tx, storageSourceID, storageSourceID, fromRel, toRel); err != nil {
		return err
	}
	if err := moveShareRecordsTx(tx, storageSourceID, storageSourceID, fromRel, toRel); err != nil {
		return err
	}
	if err := s.rebaseUserPathsTx(tx, storageSourceID, fromRel, storageSourceID, toRel); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT id, relative_path FROM file_records
  WHERE storage_source_id = ? AND record_status = ? AND `+relativePathSubtreeSQL,
		appendRelativePathSubtreeArgs([]any{storageSourceID, models.FileRecordActive}, fromRel)...)
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
	now := time.Now().UTC()
	for _, record := range records {
		newRel := toRel
		if isDir {
			newRel += strings.TrimPrefix(record.rel, fromRel)
		}
		if _, err := tx.Exec(`UPDATE file_records SET relative_path = ?, updated_by_user_id = ?, updated_at = ? WHERE id = ?`,
			newRel, actorUserID, now, record.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) deletePathMetadata(storageSourceID int64, relPath string, isDir bool) error {
	return s.deletePathMetadataWithDelta(storageSourceID, relPath, isDir, 0)
}

func (s *Service) deletePathMetadataWithDelta(storageSourceID int64, relPath string, isDir bool, usageDelta int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if isDir {
		if _, err := tx.Exec(`DELETE FROM images WHERE storage_source_id = ? AND `+relativePathSubtreeSQL,
			appendRelativePathSubtreeArgs([]any{storageSourceID}, relPath)...); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM file_shares WHERE storage_source_id = ? AND trash_key IS NULL AND `+
			relativePathSubtreeSQL, appendRelativePathSubtreeArgs([]any{storageSourceID}, relPath)...); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM file_records WHERE storage_source_id = ? AND record_status = ? AND `+
			relativePathSubtreeSQL,
			appendRelativePathSubtreeArgs([]any{storageSourceID, models.FileRecordActive}, relPath)...); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(`DELETE FROM images WHERE storage_source_id = ? AND relative_path = ?`, storageSourceID, relPath); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM file_shares WHERE storage_source_id = ? AND relative_path = ? AND trash_key IS NULL`, storageSourceID, relPath); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM file_records WHERE storage_source_id = ? AND relative_path = ? AND record_status = ?`,
			storageSourceID, relPath, models.FileRecordActive); err != nil {
			return err
		}
	}
	if usageDelta != 0 {
		if err := s.AdjustSourceUsageTx(tx, storageSourceID, usageDelta); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func syncPathParents(paths ...string) error {
	seen := make(map[string]struct{}, len(paths))
	for _, item := range paths {
		parent := filepath.Dir(item)
		if _, exists := seen[parent]; exists {
			continue
		}
		seen[parent] = struct{}{}
		if err := syncDirectory(parent); err != nil {
			return err
		}
	}
	return nil
}

// SourcePathOperationCount 返回仍依赖该来源的路径操作数量。
func (s *Service) SourcePathOperationCount(storageSourceID int64) (int, error) {
	return s.pathOperationCount(func(op pathOperation) bool { return op.StorageSourceID == storageSourceID })
}

// UserPathOperationCount 返回仍引用该用户的路径操作数量。
func (s *Service) UserPathOperationCount(userID int64) (int, error) {
	return s.pathOperationCount(func(op pathOperation) bool { return op.ActorUserID != nil && *op.ActorUserID == userID })
}

func (s *Service) pathOperationCount(matches func(pathOperation) bool) (int, error) {
	entries, err := os.ReadDir(s.pathOperationsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return 0, fmt.Errorf("路径操作日志 %s 不能是符号链接", entry.Name())
		}
		op, err := s.readPathOperation(filepath.Join(s.pathOperationsDir(), entry.Name()))
		if err != nil {
			return 0, fmt.Errorf("读取路径操作日志 %s 失败: %w", entry.Name(), err)
		}
		if entry.Name() != op.OperationID+".json" {
			return 0, fmt.Errorf("路径操作日志 %s 名称不匹配", entry.Name())
		}
		if matches(op) {
			count++
		}
	}
	return count, nil
}

func pathOperationTargetExists(absPath string, isDir bool) (bool, error) {
	info, err := os.Lstat(absPath)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.IsDir() != isDir {
		return false, fmt.Errorf("路径类型与操作日志不符")
	}
	return true, nil
}

// guardPendingManagedPathWrite prevents a failed delete/move from retaining
// authority over a new payload written before the next startup recovery.
// Completed deletes are safe to reuse: recovery only removes their journals.
func (s *Service) guardPendingManagedPathWrite(sourceID int64, relPaths ...string) error {
	entries, err := os.ReadDir(s.pathOperationsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("路径操作日志 %s 不能是符号链接", entry.Name())
		}
		op, err := s.readPathOperation(filepath.Join(s.pathOperationsDir(), entry.Name()))
		if errors.Is(err, fs.ErrNotExist) {
			// A concurrent, successful operation may have removed its journal.
			continue
		}
		if err != nil {
			return fmt.Errorf("读取路径操作日志 %s 失败: %w", entry.Name(), err)
		}
		if entry.Name() != op.OperationID+".json" {
			return fmt.Errorf("路径操作日志 %s 名称不匹配", entry.Name())
		}
		if op.StorageSourceID != sourceID {
			continue
		}
		if op.Kind == pathOperationDelete {
			committed, err := uploadMarkerExists(s.pathDatabaseReadyPath(op.OperationID))
			if err != nil {
				return err
			}
			if committed {
				continue
			}
		}
		for _, target := range relPaths {
			for _, pending := range []string{op.FromRelativePath, op.ToRelativePath} {
				if pending != "" && (target == pending || strings.HasPrefix(target, pending+"/") || strings.HasPrefix(pending, target+"/")) {
					return fmt.Errorf("托管路径 %s 与尚未恢复的路径操作 %s 冲突", target, op.OperationID)
				}
			}
		}
	}
	return nil
}

func (s *Service) verifyManagedDeleteRecovery(src *models.StorageSource, rel string) error {
	managed := false
	for index, segment := range strings.Split(rel, "/") {
		if !security.IsManagedNamespaceName(segment) {
			continue
		}
		if index != 0 || segment != security.ManagedNamespaceSegment {
			return fmt.Errorf("拒绝恢复嵌套或非规范托管路径 %s", rel)
		}
		managed = true
	}
	if !managed {
		return nil
	}
	if rel == security.ManagedNamespaceSegment {
		return fmt.Errorf("拒绝通过路径恢复删除托管根")
	}
	return managedroot.NewService(s.db).Ensure(src)
}

// RecoverPathOperations 在监听服务前回滚未提交移动并完成永久删除。
func (s *Service) RecoverPathOperations() (PathRecoveryResult, error) {
	result := PathRecoveryResult{}
	dir := s.pathOperationsDir()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if err := journal.CleanOrphans(dir, ".filesystem-ready", ".database-ready"); err != nil {
		return result, err
	}
	entries, err = os.ReadDir(dir)
	if err != nil {
		return result, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return result, fmt.Errorf("路径操作日志 %s 不能是符号链接", entry.Name())
		}
		op, err := s.readPathOperation(filepath.Join(dir, entry.Name()))
		if err != nil {
			return result, fmt.Errorf("读取路径操作日志 %s 失败: %w", entry.Name(), err)
		}
		if entry.Name() != op.OperationID+".json" {
			return result, fmt.Errorf("路径操作日志 %s 名称不匹配", entry.Name())
		}
		databaseReady, err := uploadMarkerExists(s.pathDatabaseReadyPath(op.OperationID))
		if err != nil {
			return result, err
		}
		if op.Kind == pathOperationDelete && databaseReady {
			// The delete is committed. Its old path may now belong to a new
			// object; replaying either filesystem or metadata deletion is unsafe.
			if err := s.removePathOperation(op.OperationID); err != nil {
				return result, err
			}
			result.CompletedDeletes++
			continue
		}
		src, err := s.sources.GetByID(op.StorageSourceID)
		if err != nil {
			return result, fmt.Errorf("路径操作 %s 的存储源不存在: %w", op.OperationID, err)
		}
		fromAbs, err := security.ResolveInSource(src.RootPath, op.FromRelativePath)
		if err != nil {
			return result, err
		}
		if op.Kind == pathOperationDelete {
			if err := s.verifyManagedDeleteRecovery(src, op.FromRelativePath); err != nil {
				return result, fmt.Errorf("校验内部删除归属失败: %w", err)
			}
			if err := os.RemoveAll(fromAbs); err != nil {
				return result, err
			}
			if err := syncPathParents(fromAbs); err != nil {
				return result, err
			}
			if err := s.deletePathMetadata(src.ID, op.FromRelativePath, op.IsDirectory); err != nil {
				return result, err
			}
			s.InvalidateSourceUsage(src.ID)
			if err := s.markPathDatabaseReady(op.OperationID); err != nil {
				return result, err
			}
			if err := s.removePathOperation(op.OperationID); err != nil {
				return result, err
			}
			result.CompletedDeletes++
			continue
		}
		toAbs, err := security.ResolveInSource(src.RootPath, op.ToRelativePath)
		if err != nil {
			return result, err
		}
		fromExists, err := pathOperationTargetExists(fromAbs, op.IsDirectory)
		if err != nil {
			return result, err
		}
		toExists, err := pathOperationTargetExists(toAbs, op.IsDirectory)
		if err != nil {
			return result, err
		}
		if databaseReady {
			if fromExists || !toExists {
				return result, fmt.Errorf("已提交路径移动 %s 的文件系统状态不一致", op.OperationID)
			}
			if err := s.movePathMetadata(src.ID, op.FromRelativePath, op.ToRelativePath, op.IsDirectory, op.ActorUserID); err != nil {
				return result, err
			}
			if err := s.removePathOperation(op.OperationID); err != nil {
				return result, err
			}
			result.CompletedMoves++
			continue
		}
		switch {
		case fromExists && !toExists:
			if err := s.movePathMetadata(src.ID, op.ToRelativePath, op.FromRelativePath, op.IsDirectory, op.ActorUserID); err != nil {
				return result, err
			}
		case !fromExists && toExists:
			if err := s.movePathMetadata(src.ID, op.ToRelativePath, op.FromRelativePath, op.IsDirectory, op.ActorUserID); err != nil {
				return result, err
			}
			if err := os.Rename(toAbs, fromAbs); err != nil {
				return result, err
			}
			if err := syncPathParents(fromAbs, toAbs); err != nil {
				return result, err
			}
		default:
			return result, fmt.Errorf("未提交路径移动 %s 存在歧义文件系统状态", op.OperationID)
		}
		if err := s.removePathOperation(op.OperationID); err != nil {
			return result, err
		}
		result.RolledBackMoves++
	}
	return result, nil
}
