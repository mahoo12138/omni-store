package files

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/omni-store/omnistore/internal/auth"
	"github.com/omni-store/omnistore/internal/journal"
	"github.com/omni-store/omnistore/internal/security"
)

const transferOperationVersion = 1

type transferOperation struct {
	Version               int    `json:"version"`
	OperationID           string `json:"operation_id"`
	SourceStorageSourceID int64  `json:"source_storage_source_id"`
	TargetStorageSourceID int64  `json:"target_storage_source_id"`
	SourceRelativePath    string `json:"source_relative_path"`
	TargetRelativePath    string `json:"target_relative_path"`
	IsDirectory           bool   `json:"is_directory"`
}

// TransferRecoveryResult 描述启动时恢复的中断跨来源移动。
type TransferRecoveryResult struct {
	CompletedMoves  int `json:"completed_moves"`
	RolledBackMoves int `json:"rolled_back_moves"`
}

func (s *Service) transferOperationsDir() string {
	return filepath.Join(s.sources.DataDir(), "operations", "transfers")
}

func (s *Service) transferOperationPath(operationID string) string {
	return filepath.Join(s.transferOperationsDir(), operationID+".json")
}

func (s *Service) transferTargetReadyPath(operationID string) string {
	return filepath.Join(s.transferOperationsDir(), operationID+".target-ready")
}

func (s *Service) transferDatabaseReadyPath(operationID string) string {
	return filepath.Join(s.transferOperationsDir(), operationID+".database-ready")
}

func validTransferOperationID(operationID string) bool {
	if !strings.HasPrefix(operationID, "trf-") || len(operationID) != len("trf-")+24 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(operationID, "trf-"))
	return err == nil
}

func validateTransferOperation(op transferOperation) error {
	if op.Version != transferOperationVersion || !validTransferOperationID(op.OperationID) ||
		op.SourceStorageSourceID <= 0 || op.TargetStorageSourceID <= 0 ||
		op.SourceStorageSourceID == op.TargetStorageSourceID {
		return fmt.Errorf("非法跨来源移动操作日志")
	}
	for label, value := range map[string]string{
		"源":  op.SourceRelativePath,
		"目标": op.TargetRelativePath,
	} {
		normalized, err := security.NormalizeRelPath(value)
		if err != nil || normalized == "" || normalized != value {
			return fmt.Errorf("非法跨来源移动%s路径", label)
		}
	}
	return nil
}

func (s *Service) newTransferOperation(sourceID, targetID int64, sourceRel, targetRel string, isDir bool) transferOperation {
	return transferOperation{
		Version: transferOperationVersion, OperationID: auth.NewRandomToken("trf-", 12),
		SourceStorageSourceID: sourceID, TargetStorageSourceID: targetID,
		SourceRelativePath: sourceRel, TargetRelativePath: targetRel, IsDirectory: isDir,
	}
}

// writeTransferOperation 在复制目标数据前持久化移动意图。完整同步后通过硬链接
// 提交 JSON，避免崩溃留下可见的半截日志。
func (s *Service) writeTransferOperation(op transferOperation) error {
	if err := validateTransferOperation(op); err != nil {
		return err
	}
	dir := s.transferOperationsDir()
	for _, marker := range []string{s.transferTargetReadyPath(op.OperationID), s.transferDatabaseReadyPath(op.OperationID)} {
		if err := os.Remove(marker); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return journal.WriteJSONExclusive(dir, s.transferOperationPath(op.OperationID), op)
}

func (s *Service) createTransferMarker(path string) error {
	return journal.CreateMarker(path, s.transferOperationsDir())
}

func (s *Service) markTransferTargetReady(operationID string) error {
	return s.createTransferMarker(s.transferTargetReadyPath(operationID))
}

func (s *Service) markTransferDatabaseReady(operationID string) error {
	return s.createTransferMarker(s.transferDatabaseReadyPath(operationID))
}

func (s *Service) removeTransferOperation(operationID string) error {
	return journal.RemoveFiles(s.transferOperationsDir(),
		s.transferOperationPath(operationID),
		s.transferTargetReadyPath(operationID),
		s.transferDatabaseReadyPath(operationID),
	)
}

func (s *Service) transferRecoveryPaths(op transferOperation) (sourceAbs, targetAbs string, err error) {
	source, err := s.sources.GetByID(op.SourceStorageSourceID)
	if err != nil {
		return "", "", err
	}
	target, err := s.sources.GetByID(op.TargetStorageSourceID)
	if err != nil {
		return "", "", err
	}
	sourceAbs, err = security.ResolveInSource(source.RootPath, op.SourceRelativePath)
	if err != nil {
		return "", "", err
	}
	targetAbs, err = security.ResolveInSource(target.RootPath, op.TargetRelativePath)
	return sourceAbs, targetAbs, err
}

func (s *Service) transferRecoveryPlan(op transferOperation, sourceAbs, targetAbs string) *transferPlan {
	return &transferPlan{
		sourceRel: op.SourceRelativePath, sourceAbs: sourceAbs,
		targetRel: op.TargetRelativePath, targetAbs: targetAbs, isDir: op.IsDirectory,
	}
}

func (s *Service) cleanupTransferRecoveryArtifacts(_ []os.DirEntry) error {
	return journal.CleanOrphans(s.transferOperationsDir(), ".target-ready", ".database-ready")
}

func decodeTransferOperation(handle io.Reader) (transferOperation, error) {
	return journal.DecodeStrictJSON[transferOperation](handle)
}

func transferMarkerExists(path string) (bool, error) {
	return journal.MarkerExists(path)
}

// SourceTransferOperationCount 返回仍依赖该存储源的中断移动数量。管理员删除
// 存储源前必须为 0，否则会破坏下一次启动恢复所需的路径与数据库定位。
func (s *Service) SourceTransferOperationCount(storageSourceID int64) (int, error) {
	dir := s.transferOperationsDir()
	entries, err := os.ReadDir(dir)
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
			return 0, fmt.Errorf("跨来源移动操作日志 %s 不能是符号链接", entry.Name())
		}
		handle, err := os.Open(filepath.Join(dir, entry.Name()))
		if err != nil {
			return 0, err
		}
		op, decodeErr := decodeTransferOperation(handle)
		closeErr := handle.Close()
		if decodeErr != nil {
			return 0, fmt.Errorf("读取跨来源移动操作日志 %s 失败: %w", entry.Name(), decodeErr)
		}
		if closeErr != nil {
			return 0, closeErr
		}
		if err := validateTransferOperation(op); err != nil || entry.Name() != op.OperationID+".json" {
			return 0, fmt.Errorf("跨来源移动操作日志 %s 非法", entry.Name())
		}
		if op.SourceStorageSourceID == storageSourceID || op.TargetStorageSourceID == storageSourceID {
			count++
		}
	}
	return count, nil
}

// RecoverTransferOperations 在服务监听前恢复中断的跨来源移动。
// database-ready 是提交边界：此前源数据仍完整，因此统一回滚目标副本和数据库定位；
// 此后数据库已指向目标，统一完成源路径删除。无法证明唯一完整副本时停止启动。
func (s *Service) RecoverTransferOperations() (TransferRecoveryResult, error) {
	result := TransferRecoveryResult{}
	dir := s.transferOperationsDir()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if err := s.cleanupTransferRecoveryArtifacts(entries); err != nil {
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
			return result, fmt.Errorf("跨来源移动操作日志 %s 不能是符号链接", entry.Name())
		}
		journalPath := filepath.Join(dir, entry.Name())
		handle, err := os.Open(journalPath)
		if err != nil {
			return result, err
		}
		op, decodeErr := decodeTransferOperation(handle)
		closeErr := handle.Close()
		if decodeErr != nil {
			return result, fmt.Errorf("读取跨来源移动操作日志 %s 失败: %w", entry.Name(), decodeErr)
		}
		if closeErr != nil {
			return result, closeErr
		}
		if err := validateTransferOperation(op); err != nil {
			return result, fmt.Errorf("跨来源移动操作日志 %s 非法: %w", entry.Name(), err)
		}
		if entry.Name() != op.OperationID+".json" {
			return result, fmt.Errorf("跨来源移动操作日志文件名 %s 与 operation_id 不一致", entry.Name())
		}
		sourceAbs, targetAbs, err := s.transferRecoveryPaths(op)
		if err != nil {
			return result, fmt.Errorf("解析跨来源移动 %s 路径失败: %w", op.OperationID, err)
		}
		sourceExists, err := pathExists(sourceAbs)
		if err != nil {
			return result, err
		}
		targetExists, err := pathExists(targetAbs)
		if err != nil {
			return result, err
		}
		targetReady, err := transferMarkerExists(s.transferTargetReadyPath(op.OperationID))
		if err != nil {
			return result, err
		}
		databaseReady, err := transferMarkerExists(s.transferDatabaseReadyPath(op.OperationID))
		if err != nil {
			return result, err
		}
		if databaseReady && !targetReady {
			return result, fmt.Errorf("跨来源移动 %s 的数据库阶段缺少目标完成标记", op.OperationID)
		}

		if databaseReady {
			if !targetExists {
				return result, fmt.Errorf("跨来源移动 %s 的数据库已提交但目标数据不存在", op.OperationID)
			}
			if sourceExists {
				if err := os.RemoveAll(sourceAbs); err != nil {
					return result, fmt.Errorf("完成跨来源移动 %s 删除源路径失败: %w", op.OperationID, err)
				}
				if err := syncDirectory(filepath.Dir(sourceAbs)); err != nil {
					return result, fmt.Errorf("同步跨来源移动 %s 源目录失败: %w", op.OperationID, err)
				}
			}
			if err := s.removeSourcePersistentLocks(op.SourceStorageSourceID, op.SourceRelativePath); err != nil {
				return result, fmt.Errorf("清理跨来源移动 %s 持久锁失败: %w", op.OperationID, err)
			}
			if err := s.removeTransferOperation(op.OperationID); err != nil {
				return result, err
			}
			result.CompletedMoves++
			continue
		}

		if !sourceExists {
			return result, fmt.Errorf("跨来源移动 %s 尚未提交但源数据不存在", op.OperationID)
		}
		source, err := s.sources.GetByID(op.SourceStorageSourceID)
		if err != nil {
			return result, err
		}
		target, err := s.sources.GetByID(op.TargetStorageSourceID)
		if err != nil {
			return result, err
		}
		plan := s.transferRecoveryPlan(op, sourceAbs, targetAbs)
		// 即使目标副本被外部删除，也必须回滚可能已经提交、但尚未来得及写
		// database-ready 标记的数据库定位。
		if err := s.rollbackTransferRecords(source, target, plan); err != nil {
			return result, fmt.Errorf("回滚跨来源移动 %s 数据库定位失败: %w", op.OperationID, err)
		}
		if targetExists {
			if err := os.RemoveAll(targetAbs); err != nil {
				return result, fmt.Errorf("回滚跨来源移动 %s 目标副本失败: %w", op.OperationID, err)
			}
			if err := syncDirectory(filepath.Dir(targetAbs)); err != nil {
				return result, fmt.Errorf("同步跨来源移动 %s 目标目录失败: %w", op.OperationID, err)
			}
		}
		if err := s.removeTransferOperation(op.OperationID); err != nil {
			return result, err
		}
		result.RolledBackMoves++
	}
	return result, nil
}

func (s *Service) removeSourcePersistentLocks(storageSourceID int64, relPath string) error {
	_, err := s.db.Exec(`DELETE FROM webdav_locks
  WHERE storage_source_id = ? AND (relative_path = ? OR substr(relative_path, 1, length(?) + 1) = ? || '/')`,
		storageSourceID, relPath, relPath, relPath)
	return err
}
