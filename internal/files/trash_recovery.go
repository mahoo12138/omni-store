package files

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/omni-store/omnistore/internal/journal"
	"github.com/omni-store/omnistore/internal/security"
)

const (
	trashOperationVersion = 1
	trashOperationMove    = "move_to_trash"
	trashOperationRestore = "restore_trash"
	trashOperationPurge   = "purge_trash"
)

type trashOperation struct {
	Version             int    `json:"version"`
	Kind                string `json:"kind"`
	TrashKey            string `json:"trash_key"`
	StorageSourceID     int64  `json:"storage_source_id"`
	SourceRelativePath  string `json:"source_relative_path,omitempty"`
	RestoreRelativePath string `json:"restore_relative_path,omitempty"`
}

// TrashRecoveryResult 描述启动时处理的中断回收站操作。
type TrashRecoveryResult struct {
	CompletedMoves     int `json:"completed_moves"`
	RolledBackMoves    int `json:"rolled_back_moves"`
	CompletedRestores  int `json:"completed_restores"`
	RolledBackRestores int `json:"rolled_back_restores"`
	CompletedPurges    int `json:"completed_purges"`
}

func (s *Service) trashOperationsDir() string {
	return filepath.Join(s.trashDir, ".operations")
}

func (s *Service) trashOperationPath(trashKey string) string {
	return filepath.Join(s.trashOperationsDir(), trashKey+".json")
}

func (s *Service) trashOperationReadyPath(trashKey string) string {
	return filepath.Join(s.trashOperationsDir(), trashKey+".ready")
}

func (s *Service) trashOperationRollbackReadyPath(trashKey string) string {
	return filepath.Join(s.trashOperationsDir(), trashKey+".rollback-ready")
}

func validTrashKey(trashKey string) bool {
	if !strings.HasPrefix(trashKey, "trh-") || len(trashKey) != len("trh-")+24 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(trashKey, "trh-"))
	return err == nil
}

func validateTrashOperation(op trashOperation) error {
	if op.Version != trashOperationVersion || !validTrashKey(op.TrashKey) || op.StorageSourceID <= 0 {
		return fmt.Errorf("非法回收站操作日志")
	}
	switch op.Kind {
	case trashOperationMove:
		if op.SourceRelativePath == "" || op.RestoreRelativePath != "" {
			return fmt.Errorf("非法移入回收站操作日志")
		}
	case trashOperationRestore:
		if op.RestoreRelativePath == "" || op.SourceRelativePath != "" {
			return fmt.Errorf("非法回收站恢复操作日志")
		}
	case trashOperationPurge:
		if op.SourceRelativePath != "" || op.RestoreRelativePath != "" {
			return fmt.Errorf("非法回收站清理操作日志")
		}
	default:
		return fmt.Errorf("未知回收站操作类型 %q", op.Kind)
	}
	return nil
}

// writeTrashOperation 在跨文件系统/SQLite 操作开始前持久化意图。
// 临时文件与最终文件位于同一目录，完整同步后用硬链接提交，既不会
// 覆盖未恢复的同名操作，也不会暴露半截 JSON。
func (s *Service) writeTrashOperation(op trashOperation) error {
	if err := validateTrashOperation(op); err != nil {
		return err
	}
	dir := s.trashOperationsDir()
	for _, marker := range []string{s.trashOperationReadyPath(op.TrashKey), s.trashOperationRollbackReadyPath(op.TrashKey)} {
		if err := os.Remove(marker); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return journal.WriteJSONExclusive(dir, s.trashOperationPath(op.TrashKey), op)
}

func (s *Service) removeTrashOperation(trashKey string) error {
	return journal.RemoveFiles(s.trashOperationsDir(),
		s.trashOperationPath(trashKey),
		s.trashOperationReadyPath(trashKey),
		s.trashOperationRollbackReadyPath(trashKey),
	)
}

func (s *Service) markTrashOperationDestinationReady(trashKey string) error {
	return s.createTrashOperationMarker(s.trashOperationReadyPath(trashKey))
}

func (s *Service) markTrashOperationRollbackReady(trashKey string) error {
	return s.createTrashOperationMarker(s.trashOperationRollbackReadyPath(trashKey))
}

func (s *Service) createTrashOperationMarker(readyPath string) error {
	return journal.CreateMarker(readyPath, s.trashOperationsDir())
}

func syncDirectory(dir string) error {
	return journal.SyncDir(dir)
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func (s *Service) trashEntryExists(trashKey string) (bool, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM trash_entries WHERE trash_key = ?`, trashKey).Scan(&count); err != nil {
		return false, err
	}
	return count == 1, nil
}

func (s *Service) recoverySourcePath(storageSourceID int64, relInput string) (string, error) {
	relPath, err := security.NormalizeRelPath(relInput)
	if err != nil || relPath == "" {
		return "", fmt.Errorf("非法恢复相对路径 %q", relInput)
	}
	src, err := s.sources.GetByID(storageSourceID)
	if err != nil {
		return "", err
	}
	return security.ResolveInSource(src.RootPath, relPath)
}

// RecoverTrashOperations 在 HTTP/S3 开始监听前恢复中断的回收站操作。
// SQLite 是否存在 trash_entries 记录是提交边界：存在则维持回收站状态，
// 不存在则维持源/恢复目标状态。任何无法无损判定的状态都会阻止启动。
func (s *Service) RecoverTrashOperations() (TrashRecoveryResult, error) {
	result := TrashRecoveryResult{}
	dir := s.trashOperationsDir()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), ".operation-") && strings.HasSuffix(entry.Name(), ".tmp") {
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
				return result, fmt.Errorf("清理未提交的回收站临时日志 %s 失败: %w", entry.Name(), err)
			}
			continue
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			markerSuffix := ""
			switch {
			case strings.HasSuffix(entry.Name(), ".rollback-ready"):
				markerSuffix = ".rollback-ready"
			case strings.HasSuffix(entry.Name(), ".ready"):
				markerSuffix = ".ready"
			}
			if !entry.IsDir() && markerSuffix != "" {
				trashKey := strings.TrimSuffix(entry.Name(), markerSuffix)
				if _, err := os.Stat(s.trashOperationPath(trashKey)); errors.Is(err, fs.ErrNotExist) {
					if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
						return result, fmt.Errorf("清理孤立回收站阶段标记 %s 失败: %w", entry.Name(), err)
					}
				}
			}
			continue
		}
		journalPath := filepath.Join(dir, entry.Name())
		op, err := journal.ReadStrictJSON[trashOperation](journalPath)
		if err != nil {
			return result, fmt.Errorf("解析回收站操作日志 %s 失败: %w", entry.Name(), err)
		}
		if err := validateTrashOperation(op); err != nil {
			return result, fmt.Errorf("校验回收站操作日志 %s 失败: %w", entry.Name(), err)
		}
		if entry.Name() != op.TrashKey+".json" {
			return result, fmt.Errorf("回收站操作日志文件名与 trash_key 不一致: %s", entry.Name())
		}
		switch op.Kind {
		case trashOperationMove:
			completed, rolledBack, err := s.recoverMoveToTrash(op)
			if err != nil {
				return result, fmt.Errorf("恢复移入回收站操作 %s 失败: %w", op.TrashKey, err)
			}
			if completed {
				result.CompletedMoves++
			}
			if rolledBack {
				result.RolledBackMoves++
			}
		case trashOperationRestore:
			completed, rolledBack, err := s.recoverTrashRestore(op)
			if err != nil {
				return result, fmt.Errorf("恢复回收站还原操作 %s 失败: %w", op.TrashKey, err)
			}
			if completed {
				result.CompletedRestores++
			}
			if rolledBack {
				result.RolledBackRestores++
			}
		case trashOperationPurge:
			if err := s.recoverTrashPurge(op); err != nil {
				return result, fmt.Errorf("恢复回收站永久清理操作 %s 失败: %w", op.TrashKey, err)
			}
			result.CompletedPurges++
		}
		if err := s.removeTrashOperation(op.TrashKey); err != nil {
			return result, fmt.Errorf("移除已恢复的回收站操作日志失败: %w", err)
		}
	}
	return result, nil
}

func (s *Service) recoverMoveToTrash(op trashOperation) (completed, rolledBack bool, err error) {
	hasEntry, err := s.trashEntryExists(op.TrashKey)
	if err != nil {
		return false, false, err
	}
	sourceAbs, err := s.recoverySourcePath(op.StorageSourceID, op.SourceRelativePath)
	if err != nil {
		return false, false, err
	}
	payloadAbs := s.trashPayloadPath(op.TrashKey)
	sourceExists, err := pathExists(sourceAbs)
	if err != nil {
		return false, false, err
	}
	payloadExists, err := pathExists(payloadAbs)
	if err != nil {
		return false, false, err
	}
	destinationReady, err := pathExists(s.trashOperationReadyPath(op.TrashKey))
	if err != nil {
		return false, false, err
	}
	rollbackReady, err := pathExists(s.trashOperationRollbackReadyPath(op.TrashKey))
	if err != nil {
		return false, false, err
	}
	if hasEntry {
		switch {
		case payloadExists && !sourceExists:
			return true, false, nil
		case !payloadExists && sourceExists:
			if err := os.MkdirAll(filepath.Dir(payloadAbs), 0o700); err != nil {
				return false, false, err
			}
			return true, false, moveFilesystemTree(sourceAbs, payloadAbs)
		default:
			return false, false, fmt.Errorf("已提交记录对应的源和载荷状态冲突")
		}
	}
	switch {
	case payloadExists && !sourceExists:
		if err := os.MkdirAll(filepath.Dir(sourceAbs), 0o755); err != nil {
			return false, false, err
		}
		if err := moveFilesystemTree(payloadAbs, sourceAbs); err != nil {
			return false, false, err
		}
		_ = os.RemoveAll(filepath.Dir(payloadAbs))
		return false, true, nil
	case payloadExists && sourceExists:
		if rollbackReady {
			if err := os.RemoveAll(filepath.Dir(payloadAbs)); err != nil {
				return false, false, err
			}
		} else if destinationReady {
			if err := os.RemoveAll(sourceAbs); err != nil {
				return false, false, err
			}
			_, err := moveFilesystemTreeTracked(payloadAbs, sourceAbs, func() error {
				return s.markTrashOperationRollbackReady(op.TrashKey)
			})
			if err != nil {
				return false, false, err
			}
			_ = os.RemoveAll(filepath.Dir(payloadAbs))
		} else if err := os.RemoveAll(filepath.Dir(payloadAbs)); err != nil {
			return false, false, err
		}
		return false, true, nil
	case !payloadExists && sourceExists:
		_ = os.RemoveAll(filepath.Dir(payloadAbs))
		return false, true, nil
	default:
		return false, false, fmt.Errorf("未提交操作的源和载荷都不存在")
	}
}

func (s *Service) recoverTrashRestore(op trashOperation) (completed, rolledBack bool, err error) {
	hasEntry, err := s.trashEntryExists(op.TrashKey)
	if err != nil {
		return false, false, err
	}
	targetAbs, err := s.recoverySourcePath(op.StorageSourceID, op.RestoreRelativePath)
	if err != nil {
		return false, false, err
	}
	payloadAbs := s.trashPayloadPath(op.TrashKey)
	targetExists, err := pathExists(targetAbs)
	if err != nil {
		return false, false, err
	}
	payloadExists, err := pathExists(payloadAbs)
	if err != nil {
		return false, false, err
	}
	destinationReady, err := pathExists(s.trashOperationReadyPath(op.TrashKey))
	if err != nil {
		return false, false, err
	}
	rollbackReady, err := pathExists(s.trashOperationRollbackReadyPath(op.TrashKey))
	if err != nil {
		return false, false, err
	}
	if hasEntry {
		switch {
		case payloadExists && !targetExists:
			return false, true, nil
		case !payloadExists && targetExists:
			if err := os.MkdirAll(filepath.Dir(payloadAbs), 0o700); err != nil {
				return false, false, err
			}
			return false, true, moveFilesystemTree(targetAbs, payloadAbs)
		case payloadExists && targetExists:
			if rollbackReady {
				if err := os.RemoveAll(targetAbs); err != nil {
					return false, false, err
				}
			} else if destinationReady {
				if err := os.RemoveAll(payloadAbs); err != nil {
					return false, false, err
				}
				_, err := moveFilesystemTreeTracked(targetAbs, payloadAbs, func() error {
					return s.markTrashOperationRollbackReady(op.TrashKey)
				})
				if err != nil {
					return false, false, err
				}
			} else if err := os.RemoveAll(targetAbs); err != nil {
				return false, false, err
			}
			return false, true, nil
		default:
			return false, false, fmt.Errorf("未提交恢复的目标和载荷都不存在")
		}
	}
	switch {
	case targetExists && !payloadExists:
		_ = os.RemoveAll(filepath.Dir(payloadAbs))
		return true, false, nil
	case !targetExists && payloadExists:
		if err := os.MkdirAll(filepath.Dir(targetAbs), 0o755); err != nil {
			return false, false, err
		}
		if err := moveFilesystemTree(payloadAbs, targetAbs); err != nil {
			return false, false, err
		}
		_ = os.RemoveAll(filepath.Dir(payloadAbs))
		return true, false, nil
	case targetExists && payloadExists:
		if err := os.RemoveAll(filepath.Dir(payloadAbs)); err != nil {
			return false, false, err
		}
		return true, false, nil
	default:
		return false, false, fmt.Errorf("已提交恢复的目标和载荷都不存在")
	}
}

func (s *Service) recoverTrashPurge(op trashOperation) error {
	if err := os.RemoveAll(filepath.Dir(s.trashPayloadPath(op.TrashKey))); err != nil {
		return err
	}
	hasEntry, err := s.trashEntryExists(op.TrashKey)
	if err != nil || !hasEntry {
		return err
	}
	return s.purgeTrashMetadata(op.StorageSourceID, op.TrashKey)
}
