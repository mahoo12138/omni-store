package files

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/omni-store/omnistore/internal/auth"
	"github.com/omni-store/omnistore/internal/journal"
	"github.com/omni-store/omnistore/internal/models"
	"github.com/omni-store/omnistore/internal/security"
)

const copyOperationVersion = 1

type copyOperation struct {
	Version               int    `json:"version"`
	OperationID           string `json:"operation_id"`
	SourceStorageSourceID int64  `json:"source_storage_source_id"`
	TargetStorageSourceID int64  `json:"target_storage_source_id"`
	TargetRelativePath    string `json:"target_relative_path"`
	StagingRelativePath   string `json:"staging_relative_path"`
	IsDirectory           bool   `json:"is_directory"`
}

// CopyRecoveryResult 描述启动时清理的中断复制。
type CopyRecoveryResult struct {
	CompletedCopies  int `json:"completed_copies"`
	RolledBackCopies int `json:"rolled_back_copies"`
}

func (s *Service) copyOperationsDir() string {
	return filepath.Join(s.sources.DataDir(), "operations", "copies")
}

func (s *Service) copyOperationPath(operationID string) string {
	return filepath.Join(s.copyOperationsDir(), operationID+".json")
}

func (s *Service) copyDatabaseReadyPath(operationID string) string {
	return filepath.Join(s.copyOperationsDir(), operationID+".database-ready")
}

func validCopyOperationID(value string) bool {
	if !strings.HasPrefix(value, "cpy-") || len(value) != len("cpy-")+24 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "cpy-"))
	return err == nil
}

func validateCopyOperation(op copyOperation) error {
	if op.Version != copyOperationVersion || !validCopyOperationID(op.OperationID) ||
		op.SourceStorageSourceID <= 0 || op.TargetStorageSourceID <= 0 {
		return fmt.Errorf("非法复制操作日志")
	}
	for _, value := range []string{op.TargetRelativePath, op.StagingRelativePath} {
		normalized, err := security.NormalizeRelPath(value)
		if err != nil || normalized == "" || normalized != value {
			return fmt.Errorf("非法复制路径")
		}
	}
	token := strings.TrimPrefix(op.OperationID, "cpy-")
	if path.Base(op.StagingRelativePath) != ".omnistore-copy-"+token+".staging" ||
		path.Dir(op.StagingRelativePath) != path.Dir(op.TargetRelativePath) {
		return fmt.Errorf("复制 staging 必须与目标同级")
	}
	return nil
}

func (s *Service) newCopyOperation(sourceID, targetID int64, targetRel string, isDir bool) copyOperation {
	operationID := auth.NewRandomToken("cpy-", 12)
	stagingName := ".omnistore-copy-" + strings.TrimPrefix(operationID, "cpy-") + ".staging"
	stagingRel := stagingName
	if parent := path.Dir(targetRel); parent != "." {
		stagingRel = parent + "/" + stagingName
	}
	return copyOperation{
		Version: copyOperationVersion, OperationID: operationID,
		SourceStorageSourceID: sourceID, TargetStorageSourceID: targetID,
		TargetRelativePath: targetRel, StagingRelativePath: stagingRel, IsDirectory: isDir,
	}
}

func (s *Service) writeCopyOperation(op copyOperation) error {
	if err := validateCopyOperation(op); err != nil {
		return err
	}
	dir := s.copyOperationsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建复制日志目录失败: %w", err)
	}
	if _, err := os.Lstat(s.copyOperationPath(op.OperationID)); err == nil {
		return fmt.Errorf("复制 %s 尚未恢复", op.OperationID)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Remove(s.copyDatabaseReadyPath(op.OperationID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
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
	if err := os.Link(tempPath, s.copyOperationPath(op.OperationID)); err != nil {
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

func decodeCopyOperation(reader io.Reader) (copyOperation, error) {
	var op copyOperation
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&op); err != nil {
		return copyOperation{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return copyOperation{}, fmt.Errorf("操作日志包含额外 JSON 值")
		}
		return copyOperation{}, err
	}
	return op, validateCopyOperation(op)
}

func (s *Service) readCopyOperation(absPath string) (copyOperation, error) {
	handle, err := os.Open(absPath)
	if err != nil {
		return copyOperation{}, err
	}
	op, decodeErr := decodeCopyOperation(handle)
	closeErr := handle.Close()
	return op, errors.Join(decodeErr, closeErr)
}

func (s *Service) markCopyDatabaseReady(operationID string) error {
	return createUploadMarker(s.copyDatabaseReadyPath(operationID), s.copyOperationsDir())
}

func (s *Service) removeCopyOperation(operationID string) error {
	for _, item := range []string{s.copyOperationPath(operationID), s.copyDatabaseReadyPath(operationID)} {
		if err := os.Remove(item); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return syncDirectory(s.copyOperationsDir())
}

func (s *Service) copyOperationPaths(op copyOperation, target *models.StorageSource) (string, string, error) {
	stagingAbs, err := security.ResolveInSource(target.RootPath, op.StagingRelativePath)
	if err != nil {
		return "", "", err
	}
	targetAbs, err := security.ResolveInSource(target.RootPath, op.TargetRelativePath)
	if err != nil {
		return "", "", err
	}
	if filepath.Dir(stagingAbs) != filepath.Dir(targetAbs) {
		return "", "", fmt.Errorf("复制 staging 不在目标同级目录")
	}
	return stagingAbs, targetAbs, nil
}

func (s *Service) rollbackCopyOperation(op copyOperation, target *models.StorageSource, removeTarget bool) error {
	stagingAbs, targetAbs, err := s.copyOperationPaths(op, target)
	if err != nil {
		return err
	}
	var errs []error
	if err := os.RemoveAll(stagingAbs); err != nil {
		errs = append(errs, err)
	}
	if removeTarget {
		if err := os.RemoveAll(targetAbs); err != nil {
			errs = append(errs, err)
		}
		if err := s.deleteFileRecords(target.ID, op.TargetRelativePath, op.IsDirectory); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		errs = append(errs, syncDirectory(filepath.Dir(targetAbs)))
	}
	return errors.Join(errs...)
}

// SourceCopyOperationCount 返回仍依赖该来源的复制日志数量。
func (s *Service) SourceCopyOperationCount(storageSourceID int64) (int, error) {
	entries, err := os.ReadDir(s.copyOperationsDir())
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
			return 0, fmt.Errorf("复制日志 %s 不能是符号链接", entry.Name())
		}
		op, err := s.readCopyOperation(filepath.Join(s.copyOperationsDir(), entry.Name()))
		if err != nil || entry.Name() != op.OperationID+".json" {
			return 0, fmt.Errorf("读取复制日志 %s 失败: %w", entry.Name(), err)
		}
		if op.SourceStorageSourceID == storageSourceID || op.TargetStorageSourceID == storageSourceID {
			count++
		}
	}
	return count, nil
}

// RecoverCopyOperations 在监听服务前回滚未提交复制，或清理已提交复制日志。
func (s *Service) RecoverCopyOperations() (CopyRecoveryResult, error) {
	result := CopyRecoveryResult{}
	dir := s.copyOperationsDir()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if err := journal.CleanOrphans(dir, ".database-ready"); err != nil {
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
			return result, fmt.Errorf("复制日志 %s 不能是符号链接", entry.Name())
		}
		op, err := s.readCopyOperation(filepath.Join(dir, entry.Name()))
		if err != nil || entry.Name() != op.OperationID+".json" {
			return result, fmt.Errorf("读取复制日志 %s 失败: %w", entry.Name(), err)
		}
		if _, err := s.sources.GetByID(op.SourceStorageSourceID); err != nil {
			return result, fmt.Errorf("复制 %s 的源存储源不存在: %w", op.OperationID, err)
		}
		target, err := s.sources.GetByID(op.TargetStorageSourceID)
		if err != nil {
			return result, fmt.Errorf("复制 %s 的目标存储源不存在: %w", op.OperationID, err)
		}
		databaseReady, err := uploadMarkerExists(s.copyDatabaseReadyPath(op.OperationID))
		if err != nil {
			return result, err
		}
		if databaseReady {
			_, targetAbs, err := s.copyOperationPaths(op, target)
			if err != nil {
				return result, err
			}
			info, err := os.Lstat(targetAbs)
			if err != nil || info.Mode()&os.ModeSymlink != 0 || info.IsDir() != op.IsDirectory {
				return result, fmt.Errorf("已提交复制 %s 的目标不存在或类型不符", op.OperationID)
			}
			if err := s.rollbackCopyOperation(op, target, false); err != nil {
				return result, err
			}
			if err := s.removeCopyOperation(op.OperationID); err != nil {
				return result, err
			}
			result.CompletedCopies++
			continue
		}
		if err := s.rollbackCopyOperation(op, target, true); err != nil {
			return result, err
		}
		if err := s.removeCopyOperation(op.OperationID); err != nil {
			return result, err
		}
		result.RolledBackCopies++
	}
	return result, nil
}
