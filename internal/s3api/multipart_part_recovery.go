package s3api

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/omni-store/omnistore/internal/journal"
	"github.com/omni-store/omnistore/internal/security"
)

const multipartPartOperationVersion = 1

var (
	multipartPartTempNamePattern = regexp.MustCompile(`^\.part-[0-9]+\.tmp$`)
	multipartPartETagPattern     = regexp.MustCompile(`^"[0-9a-f]{32}"$`)
)

type multipartPartOperation struct {
	Version           int       `json:"version"`
	UploadID          string    `json:"upload_id"`
	OwnerUserID       int64     `json:"owner_user_id"`
	StorageSourceID   int64     `json:"storage_source_id"`
	ObjectKey         string    `json:"object_key"`
	PartNumber        int       `json:"part_number"`
	TempName          string    `json:"temp_name"`
	ETag              string    `json:"etag"`
	Size              int64     `json:"size"`
	CreatedAt         time.Time `json:"created_at"`
	PreviousExists    bool      `json:"previous_exists"`
	PreviousETag      string    `json:"previous_etag,omitempty"`
	PreviousSize      int64     `json:"previous_size,omitempty"`
	PreviousCreatedAt time.Time `json:"previous_created_at,omitempty"`
}

// MultipartPartRecoveryResult 描述启动时完成或回滚的分片上传。
type MultipartPartRecoveryResult struct {
	CompletedParts  int `json:"completed_parts"`
	RolledBackParts int `json:"rolled_back_parts"`
}

func (s *MultipartStore) partOperationsDir() string {
	return filepath.Join(s.dataDir, "operations", "s3-multipart-parts")
}

func multipartPartOperationName(uploadID string, partNumber int) string {
	return fmt.Sprintf("%s-%05d", uploadID, partNumber)
}

func (s *MultipartStore) partOperationPath(uploadID string, partNumber int) string {
	return filepath.Join(s.partOperationsDir(), multipartPartOperationName(uploadID, partNumber)+".json")
}

func validateMultipartPartOperation(op multipartPartOperation) error {
	if op.Version != multipartPartOperationVersion || !uploadIDPattern.MatchString(op.UploadID) ||
		op.OwnerUserID <= 0 || op.StorageSourceID <= 0 || op.PartNumber < 1 ||
		op.PartNumber > MaxMultipartParts || !multipartPartTempNamePattern.MatchString(op.TempName) ||
		op.Size < 0 || op.CreatedAt.IsZero() || !multipartPartETagPattern.MatchString(op.ETag) {
		return fmt.Errorf("非法 Multipart 分片操作日志")
	}
	normalized, err := security.NormalizeRelPath(op.ObjectKey)
	if err != nil || normalized == "" || normalized != op.ObjectKey {
		return fmt.Errorf("非法 Multipart 分片对象路径")
	}
	if op.PreviousExists {
		if !multipartPartETagPattern.MatchString(op.PreviousETag) || op.PreviousSize < 0 || op.PreviousCreatedAt.IsZero() {
			return fmt.Errorf("非法 Multipart 旧分片状态")
		}
	} else if op.PreviousETag != "" || op.PreviousSize != 0 || !op.PreviousCreatedAt.IsZero() {
		return fmt.Errorf("不存在旧分片时不能包含旧分片状态")
	}
	return nil
}

func (s *MultipartStore) newMultipartPartOperation(upload MultipartUpload, partNumber int, tempName,
	etag string, size int64, createdAt time.Time, previous *MultipartPart) multipartPartOperation {
	op := multipartPartOperation{
		Version: multipartPartOperationVersion, UploadID: upload.UploadID, OwnerUserID: upload.OwnerUserID,
		StorageSourceID: upload.StorageSourceID, ObjectKey: upload.ObjectKey, PartNumber: partNumber,
		TempName: tempName, ETag: etag, Size: size, CreatedAt: createdAt,
	}
	if previous != nil {
		op.PreviousExists = true
		op.PreviousETag = previous.ETag
		op.PreviousSize = previous.Size
		op.PreviousCreatedAt = previous.CreatedAt
	}
	return op
}

func (s *MultipartStore) writeMultipartPartOperation(op multipartPartOperation) error {
	if err := validateMultipartPartOperation(op); err != nil {
		return err
	}
	return journal.WriteJSONExclusive(s.partOperationsDir(), s.partOperationPath(op.UploadID, op.PartNumber), op)
}

func (s *MultipartStore) removeMultipartPartOperation(uploadID string, partNumber int) error {
	return journal.RemoveFiles(s.partOperationsDir(), s.partOperationPath(uploadID, partNumber))
}

func (s *MultipartStore) readMultipartPartOperation(path string) (multipartPartOperation, error) {
	op, err := journal.ReadStrictJSON[multipartPartOperation](path)
	if err != nil {
		return multipartPartOperation{}, fmt.Errorf("读取 Multipart 分片操作日志 %s 失败: %w", filepath.Base(path), err)
	}
	if err := validateMultipartPartOperation(op); err != nil {
		return multipartPartOperation{}, fmt.Errorf("Multipart 分片操作日志 %s 非法: %w", filepath.Base(path), err)
	}
	if filepath.Base(path) != multipartPartOperationName(op.UploadID, op.PartNumber)+".json" {
		return multipartPartOperation{}, fmt.Errorf("Multipart 分片操作日志文件名与内容不一致")
	}
	return op, nil
}

func (s *MultipartStore) multipartPartOperationPaths(op multipartPartOperation) (temp, final, backup string) {
	dir := s.uploadDir(op.UploadID)
	return filepath.Join(dir, op.TempName), s.partPath(op.UploadID, op.PartNumber),
		s.partPath(op.UploadID, op.PartNumber) + ".previous"
}

func checkedMultipartPartFile(path string) (bool, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false, nil, fmt.Errorf("Multipart 分片恢复路径 %s 不是普通文件", path)
	}
	return true, info, nil
}

func multipartPartFileMatches(path string, info os.FileInfo, etag string, size int64) (bool, error) {
	if info == nil || info.Size() != size {
		return false, nil
	}
	handle, err := os.Open(path)
	if err != nil {
		return false, err
	}
	actual, readErr := etagReader(handle)
	closeErr := handle.Close()
	if readErr != nil || closeErr != nil {
		return false, errors.Join(readErr, closeErr)
	}
	return normalizeETag(actual) == normalizeETag(etag), nil
}

func (s *MultipartStore) currentMultipartPart(uploadID string, partNumber int) (*MultipartPart, error) {
	var part MultipartPart
	err := s.db.QueryRow(`SELECT part_number, etag, size, created_at FROM s3_multipart_parts
  WHERE upload_id = ? AND part_number = ?`, uploadID, partNumber).
		Scan(&part.PartNumber, &part.ETag, &part.Size, &part.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &part, err
}

func sameMultipartPart(left MultipartPart, etag string, size int64, createdAt time.Time) bool {
	return normalizeETag(left.ETag) == normalizeETag(etag) && left.Size == size &&
		left.CreatedAt.Equal(createdAt)
}

func (s *MultipartStore) commitMultipartPartOperation(op multipartPartOperation) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO s3_multipart_parts (upload_id, part_number, etag, size, created_at)
  VALUES (?, ?, ?, ?, ?)
  ON CONFLICT(upload_id, part_number) DO UPDATE SET etag = excluded.etag,
    size = excluded.size, created_at = excluded.created_at`,
		op.UploadID, op.PartNumber, op.ETag, op.Size, op.CreatedAt); err != nil {
		return err
	}
	result, err := tx.Exec(`UPDATE s3_multipart_uploads SET updated_at = ?
  WHERE upload_id = ? AND owner_user_id = ? AND storage_source_id = ? AND object_key = ?`,
		op.CreatedAt, op.UploadID, op.OwnerUserID, op.StorageSourceID, op.ObjectKey)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return err
		}
		return fmt.Errorf("Multipart Upload 状态已变化")
	}
	return tx.Commit()
}

func (s *MultipartStore) cleanupCommittedMultipartPart(op multipartPartOperation) error {
	temp, _, backup := s.multipartPartOperationPaths(op)
	for _, path := range []string{temp, backup} {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := syncMultipartDirectory(s.uploadDir(op.UploadID)); err != nil {
		return err
	}
	return s.removeMultipartPartOperation(op.UploadID, op.PartNumber)
}

func (s *MultipartStore) rollbackMultipartPartOperation(op multipartPartOperation) error {
	temp, final, backup := s.multipartPartOperationPaths(op)
	tempExists, _, err := checkedMultipartPartFile(temp)
	if err != nil {
		return err
	}
	finalExists, finalInfo, err := checkedMultipartPartFile(final)
	if err != nil {
		return err
	}
	backupExists, backupInfo, err := checkedMultipartPartFile(backup)
	if err != nil {
		return err
	}
	finalIsNew := false
	if finalExists {
		finalIsNew, err = multipartPartFileMatches(final, finalInfo, op.ETag, op.Size)
		if err != nil {
			return err
		}
	}
	if op.PreviousExists {
		if backupExists {
			backupIsPrevious, err := multipartPartFileMatches(backup, backupInfo, op.PreviousETag, op.PreviousSize)
			if err != nil {
				return err
			}
			if !backupIsPrevious || (finalExists && !finalIsNew) {
				return fmt.Errorf("Multipart 分片 %s 无法安全回滚旧内容", multipartPartOperationName(op.UploadID, op.PartNumber))
			}
		} else if !finalExists {
			return fmt.Errorf("Multipart 分片 %s 的旧内容不存在", multipartPartOperationName(op.UploadID, op.PartNumber))
		} else if finalIsNew {
			return fmt.Errorf("Multipart 分片 %s 的旧备份不存在", multipartPartOperationName(op.UploadID, op.PartNumber))
		}
	} else if finalExists && !finalIsNew {
		return fmt.Errorf("Multipart 新分片 %s 出现未知最终内容", multipartPartOperationName(op.UploadID, op.PartNumber))
	}
	if tempExists {
		if err := os.Remove(temp); err != nil {
			return err
		}
	}
	if backupExists {
		if finalExists {
			if err := os.Remove(final); err != nil {
				return err
			}
		}
		if err := os.Rename(backup, final); err != nil {
			return err
		}
	} else if !op.PreviousExists && finalExists {
		if err := os.Remove(final); err != nil {
			return err
		}
	}
	if err := syncMultipartDirectory(s.uploadDir(op.UploadID)); err != nil {
		return err
	}
	return s.removeMultipartPartOperation(op.UploadID, op.PartNumber)
}

func (s *MultipartStore) resolveMultipartPartOperation(op multipartPartOperation) (bool, error) {
	uploadExists, err := s.multipartPartUploadExists(op)
	if err != nil {
		return false, err
	}
	if !uploadExists {
		return true, errors.Join(os.RemoveAll(s.uploadDir(op.UploadID)),
			s.removeMultipartPartOperation(op.UploadID, op.PartNumber))
	}
	current, err := s.currentMultipartPart(op.UploadID, op.PartNumber)
	if err != nil {
		return false, err
	}
	if current != nil && sameMultipartPart(*current, op.ETag, op.Size, op.CreatedAt) {
		_, final, _ := s.multipartPartOperationPaths(op)
		exists, info, err := checkedMultipartPartFile(final)
		if err != nil {
			return false, err
		}
		matches := false
		if exists {
			matches, err = multipartPartFileMatches(final, info, op.ETag, op.Size)
		}
		if err != nil || !matches {
			if err != nil {
				return false, err
			}
			return false, fmt.Errorf("已提交 Multipart 分片 %s 的文件缺失或摘要不一致",
				multipartPartOperationName(op.UploadID, op.PartNumber))
		}
		return true, s.cleanupCommittedMultipartPart(op)
	}
	if op.PreviousExists {
		if current == nil || !sameMultipartPart(*current, op.PreviousETag, op.PreviousSize, op.PreviousCreatedAt) {
			return false, fmt.Errorf("Multipart 分片 %s 的数据库状态与操作日志不一致",
				multipartPartOperationName(op.UploadID, op.PartNumber))
		}
	} else if current != nil {
		return false, fmt.Errorf("Multipart 新分片 %s 已出现未知数据库状态",
			multipartPartOperationName(op.UploadID, op.PartNumber))
	}
	temp, final, backup := s.multipartPartOperationPaths(op)
	tempExists, _, err := checkedMultipartPartFile(temp)
	if err != nil {
		return false, err
	}
	finalExists, finalInfo, err := checkedMultipartPartFile(final)
	if err != nil {
		return false, err
	}
	backupExists, _, err := checkedMultipartPartFile(backup)
	if err != nil {
		return false, err
	}
	finalIsNew := false
	if finalExists {
		finalIsNew, err = multipartPartFileMatches(final, finalInfo, op.ETag, op.Size)
		if err != nil {
			return false, err
		}
	}
	if !tempExists && finalExists && finalIsNew {
		if err := s.commitMultipartPartOperation(op); err != nil {
			return false, err
		}
		return true, s.cleanupCommittedMultipartPart(op)
	}
	if !op.PreviousExists && !tempExists && !finalExists && !backupExists {
		return false, s.removeMultipartPartOperation(op.UploadID, op.PartNumber)
	}
	canRollback := tempExists || (backupExists && !finalExists) ||
		(op.PreviousExists && finalExists && !finalIsNew && !backupExists)
	if canRollback {
		return false, s.rollbackMultipartPartOperation(op)
	}
	return false, fmt.Errorf("Multipart 分片 %s 处于歧义状态，已保留现场",
		multipartPartOperationName(op.UploadID, op.PartNumber))
}

func (s *MultipartStore) multipartPartUploadExists(op multipartPartOperation) (bool, error) {
	var ownerID, sourceID int64
	var objectKey string
	err := s.db.QueryRow(`SELECT owner_user_id, storage_source_id, object_key
  FROM s3_multipart_uploads WHERE upload_id = ?`, op.UploadID).Scan(&ownerID, &sourceID, &objectKey)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if ownerID != op.OwnerUserID || sourceID != op.StorageSourceID || objectKey != op.ObjectKey {
		return false, fmt.Errorf("Multipart 分片日志与 Upload 状态不一致")
	}
	return true, nil
}

func (s *MultipartStore) multipartPartOperationFiles(uploadID string) ([]string, error) {
	entries, err := os.ReadDir(s.partOperationsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	prefix := uploadID + "-"
	paths := []string{}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) && strings.HasSuffix(entry.Name(), ".json") {
			paths = append(paths, filepath.Join(s.partOperationsDir(), entry.Name()))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func (s *MultipartStore) resolveMultipartPartOperationsForUpload(uploadID string) error {
	paths, err := s.multipartPartOperationFiles(uploadID)
	if err != nil {
		return err
	}
	for _, path := range paths {
		op, err := s.readMultipartPartOperation(path)
		if err != nil {
			return err
		}
		if _, err := s.resolveMultipartPartOperation(op); err != nil {
			return err
		}
	}
	return nil
}

func (s *MultipartStore) removeMultipartPartOperationsForUpload(uploadID string) error {
	paths, err := s.multipartPartOperationFiles(uploadID)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if len(paths) > 0 {
		return syncMultipartDirectory(s.partOperationsDir())
	}
	return nil
}

func (s *MultipartStore) multipartPartOperationCountForUpload(uploadID string) (int, error) {
	paths, err := s.multipartPartOperationFiles(uploadID)
	return len(paths), err
}

// SourcePartOperationCount 返回仍依赖该存储源的中断分片操作数量。
func (s *MultipartStore) SourcePartOperationCount(storageSourceID int64) (int, error) {
	return s.partOperationCount(func(op multipartPartOperation) bool {
		return op.StorageSourceID == storageSourceID
	})
}

// UserPartOperationCount 返回仍引用该用户的中断分片操作数量。
func (s *MultipartStore) UserPartOperationCount(userID int64) (int, error) {
	return s.partOperationCount(func(op multipartPartOperation) bool {
		return op.OwnerUserID == userID
	})
}

func (s *MultipartStore) partOperationCount(matches func(multipartPartOperation) bool) (int, error) {
	entries, err := os.ReadDir(s.partOperationsDir())
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
		op, err := s.readMultipartPartOperation(filepath.Join(s.partOperationsDir(), entry.Name()))
		if err != nil {
			return 0, err
		}
		if matches(op) {
			count++
		}
	}
	return count, nil
}

func (s *MultipartStore) cleanupMultipartPartOperationTemps(_ []os.DirEntry) error {
	return journal.CleanOrphans(s.partOperationsDir())
}

// RecoverMultipartPartOperations 在监听服务前恢复中断的 UploadPart 文件替换。
func (s *MultipartStore) RecoverMultipartPartOperations() (MultipartPartRecoveryResult, error) {
	result := MultipartPartRecoveryResult{}
	dir := s.partOperationsDir()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if err := s.cleanupMultipartPartOperationTemps(entries); err != nil {
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
		op, err := s.readMultipartPartOperation(filepath.Join(dir, entry.Name()))
		if err != nil {
			return result, err
		}
		unlock := s.locks.Lock("multipart:" + op.UploadID)
		completed, recoveryErr := s.resolveMultipartPartOperation(op)
		unlock()
		if recoveryErr != nil {
			return result, recoveryErr
		}
		if completed {
			result.CompletedParts++
		} else {
			result.RolledBackParts++
		}
	}
	return result, nil
}
