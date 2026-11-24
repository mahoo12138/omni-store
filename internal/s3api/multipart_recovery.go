package s3api

import (
	"crypto/md5"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/omni-store/omnistore/internal/files"
	"github.com/omni-store/omnistore/internal/journal"
	"github.com/omni-store/omnistore/internal/models"
	"github.com/omni-store/omnistore/internal/security"
)

const multipartCompletionVersion = 1

var multipartCompletionETagPattern = regexp.MustCompile(`^"[0-9a-f]{32}-([1-9][0-9]{0,4})"$`)

type multipartCompletion struct {
	Version              int       `json:"version"`
	UploadID             string    `json:"upload_id"`
	OwnerUserID          int64     `json:"owner_user_id"`
	StorageSourceID      int64     `json:"storage_source_id"`
	ObjectKey            string    `json:"object_key"`
	ETag                 string    `json:"etag"`
	Size                 int64     `json:"size"`
	ContentSHA256        string    `json:"content_sha256"`
	PreviousObjectExists bool      `json:"previous_object_exists"`
	PreviousSize         int64     `json:"previous_size,omitempty"`
	PreviousMTimeNano    int64     `json:"previous_mtime_unix_nano,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
}

// MultipartCompletionRecoveryResult 描述启动时完成或回滚的 Multipart 完成操作。
type MultipartCompletionRecoveryResult struct {
	CompletedUploads  int `json:"completed_uploads"`
	RolledBackUploads int `json:"rolled_back_uploads"`
}

func (s *MultipartStore) completionOperationsDir() string {
	return filepath.Join(s.dataDir, "operations", "s3-multipart-completions")
}

func (s *MultipartStore) completionOperationPath(uploadID string) string {
	return filepath.Join(s.completionOperationsDir(), uploadID+".json")
}

func validateMultipartCompletion(op multipartCompletion) error {
	etagMatch := multipartCompletionETagPattern.FindStringSubmatch(op.ETag)
	if op.Version != multipartCompletionVersion || !uploadIDPattern.MatchString(op.UploadID) ||
		op.OwnerUserID <= 0 || op.StorageSourceID <= 0 || op.Size < 0 || op.CreatedAt.IsZero() ||
		etagMatch == nil || len(op.ContentSHA256) != sha256.Size*2 {
		return fmt.Errorf("非法 Multipart 完成操作日志")
	}
	partCount, err := strconv.Atoi(etagMatch[1])
	if err != nil || partCount < 1 || partCount > MaxMultipartParts {
		return fmt.Errorf("非法 Multipart 完成分片数量")
	}
	if _, err := hex.DecodeString(op.ContentSHA256); err != nil {
		return fmt.Errorf("非法 Multipart 完成内容摘要")
	}
	if op.PreviousObjectExists {
		if op.PreviousSize < 0 || op.PreviousMTimeNano <= 0 {
			return fmt.Errorf("非法 Multipart 完成旧对象状态")
		}
	} else if op.PreviousSize != 0 || op.PreviousMTimeNano != 0 {
		return fmt.Errorf("不存在旧对象时不能包含旧对象状态")
	}
	normalized, err := security.NormalizeRelPath(op.ObjectKey)
	if err != nil || normalized == "" || normalized != op.ObjectKey {
		return fmt.Errorf("非法 Multipart 完成对象路径")
	}
	return nil
}

func (s *MultipartStore) newMultipartCompletion(uploadID string, ownerUserID, sourceID int64,
	objectKey, etag string, size int64, contentSHA256 string, previousExists bool,
	previousSize, previousMTimeNano int64) multipartCompletion {
	return multipartCompletion{
		Version: multipartCompletionVersion, UploadID: uploadID, OwnerUserID: ownerUserID,
		StorageSourceID: sourceID, ObjectKey: objectKey, ETag: etag, Size: size,
		ContentSHA256: contentSHA256, PreviousObjectExists: previousExists,
		PreviousSize: previousSize, PreviousMTimeNano: previousMTimeNano, CreatedAt: s.now().UTC(),
	}
}

func sameMultipartCompletion(left, right multipartCompletion) bool {
	return left.Version == right.Version && left.UploadID == right.UploadID &&
		left.OwnerUserID == right.OwnerUserID && left.StorageSourceID == right.StorageSourceID &&
		left.ObjectKey == right.ObjectKey && left.ETag == right.ETag && left.Size == right.Size &&
		left.ContentSHA256 == right.ContentSHA256 && left.PreviousObjectExists == right.PreviousObjectExists &&
		left.PreviousSize == right.PreviousSize && left.PreviousMTimeNano == right.PreviousMTimeNano
}

func (s *MultipartStore) previousObjectState(src *models.StorageSource, objectKey string) (bool, int64, int64, error) {
	entry, err := s.files.Stat(src, objectKey)
	if errors.Is(err, files.ErrNotFound) {
		return false, 0, 0, nil
	}
	if err != nil {
		return false, 0, 0, err
	}
	if entry.Type != files.TypeFile {
		return false, 0, 0, files.ErrUnsupported
	}
	return true, entry.Size, entry.MTime.UnixNano(), nil
}

func (s *MultipartStore) readMultipartCompletion(uploadID string) (*multipartCompletion, error) {
	path := s.completionOperationPath(uploadID)
	op, err := journal.ReadStrictJSON[multipartCompletion](path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 Multipart 完成操作日志 %s 失败: %w", filepath.Base(path), err)
	}
	if err := validateMultipartCompletion(op); err != nil {
		return nil, fmt.Errorf("Multipart 完成操作日志 %s 非法: %w", filepath.Base(path), err)
	}
	if op.UploadID != uploadID {
		return nil, fmt.Errorf("Multipart 完成操作日志文件名与 upload_id 不一致")
	}
	return &op, nil
}

func syncMultipartDirectory(dir string) error {
	return journal.SyncDir(dir)
}

func (s *MultipartStore) writeMultipartCompletion(op multipartCompletion) error {
	if err := validateMultipartCompletion(op); err != nil {
		return err
	}
	return journal.WriteJSONExclusive(s.completionOperationsDir(), s.completionOperationPath(op.UploadID), op)
}

func (s *MultipartStore) removeMultipartCompletion(uploadID string) error {
	return journal.RemoveFiles(s.completionOperationsDir(), s.completionOperationPath(uploadID))
}

func (s *MultipartStore) multipartCompletionExists(uploadID string) (bool, error) {
	op, err := s.readMultipartCompletion(uploadID)
	return op != nil, err
}

func (s *MultipartStore) hashSelectedParts(uploadID string, selected []MultipartPart) (string, error) {
	fullHash := sha256.New()
	for _, part := range selected {
		partPath := s.partPath(uploadID, part.PartNumber)
		info, err := os.Lstat(partPath)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != part.Size {
			return "", ErrInvalidPart
		}
		handle, err := os.Open(partPath)
		if err != nil {
			return "", err
		}
		partHash := md5.New()
		written, copyErr := io.Copy(io.MultiWriter(fullHash, partHash), handle)
		closeErr := handle.Close()
		if copyErr != nil || closeErr != nil {
			return "", errors.Join(copyErr, closeErr)
		}
		if written != part.Size || hex.EncodeToString(partHash.Sum(nil)) != normalizeETag(part.ETag) {
			return "", ErrInvalidPart
		}
	}
	return hex.EncodeToString(fullHash.Sum(nil)), nil
}

func (s *MultipartStore) objectMatchesCompletion(src *models.StorageSource, op multipartCompletion) (bool, os.FileInfo, func(), error) {
	handle, info, unlock, err := s.files.OpenForRead(src, op.ObjectKey)
	if errors.Is(err, files.ErrNotFound) {
		return false, nil, func() {}, nil
	}
	if err != nil {
		return false, nil, nil, err
	}
	hasher := sha256.New()
	_, copyErr := io.Copy(hasher, handle)
	closeErr := handle.Close()
	if copyErr != nil || closeErr != nil {
		unlock()
		return false, nil, nil, errors.Join(copyErr, closeErr)
	}
	return info.Size() == op.Size && hex.EncodeToString(hasher.Sum(nil)) == op.ContentSHA256, info, unlock, nil
}

func (s *MultipartStore) multipartUploadExists(op multipartCompletion) (bool, error) {
	var ownerUserID, storageSourceID int64
	var objectKey string
	err := s.db.QueryRow(`SELECT owner_user_id, storage_source_id, object_key
  FROM s3_multipart_uploads WHERE upload_id = ?`, op.UploadID).
		Scan(&ownerUserID, &storageSourceID, &objectKey)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if ownerUserID != op.OwnerUserID || storageSourceID != op.StorageSourceID || objectKey != op.ObjectKey {
		return false, fmt.Errorf("Multipart 完成日志与 Upload 状态不一致")
	}
	return true, nil
}

func (s *MultipartStore) commitMultipartCompletion(op multipartCompletion, info os.FileInfo) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO s3_object_etags
  (storage_source_id, object_key, etag, size, mtime_unix_nano, updated_at) VALUES (?, ?, ?, ?, ?, ?)
  ON CONFLICT(storage_source_id, object_key) DO UPDATE SET etag = excluded.etag, size = excluded.size,
    mtime_unix_nano = excluded.mtime_unix_nano, updated_at = excluded.updated_at`,
		op.StorageSourceID, op.ObjectKey, op.ETag, info.Size(), info.ModTime().UnixNano(), s.now().UTC()); err != nil {
		return err
	}
	result, err := tx.Exec(`DELETE FROM s3_multipart_uploads
  WHERE upload_id = ? AND owner_user_id = ? AND storage_source_id = ? AND object_key = ?`,
		op.UploadID, op.OwnerUserID, op.StorageSourceID, op.ObjectKey)
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

// resolveMultipartCompletion 返回数据库是否已经提交。提交后清理错误不能触发回滚。
func (s *MultipartStore) resolveMultipartCompletion(op multipartCompletion, objectReady bool) (bool, error) {
	uploadExists, err := s.multipartUploadExists(op)
	if err != nil {
		return false, err
	}
	if !uploadExists {
		cleanupErr := errors.Join(os.RemoveAll(s.uploadDir(op.UploadID)), s.removeMultipartCompletion(op.UploadID))
		return true, cleanupErr
	}
	src, err := s.files.StorageSourceByID(op.StorageSourceID)
	if err != nil {
		return false, err
	}
	matches, info, releaseObject, err := s.objectMatchesCompletion(src, op)
	if err != nil {
		return false, err
	}
	defer releaseObject()
	if !matches {
		return false, s.removeMultipartCompletion(op.UploadID)
	}
	if !objectReady && op.PreviousObjectExists && info.Size() == op.PreviousSize &&
		info.ModTime().UnixNano() == op.PreviousMTimeNano {
		// 最终内容虽然一致，但无法证明核心上传已经替换过旧对象；保留 Upload/Part 供重试。
		return false, s.removeMultipartCompletion(op.UploadID)
	}
	if err := s.files.RecordFile(src, op.ObjectKey, models.FileOwnerUser, &op.OwnerUserID, &op.OwnerUserID); err != nil {
		return false, err
	}
	if err := s.commitMultipartCompletion(op, info); err != nil {
		return false, err
	}
	cleanupErr := errors.Join(os.RemoveAll(s.uploadDir(op.UploadID)), s.removeMultipartCompletion(op.UploadID))
	return true, cleanupErr
}

// SourceCompletionOperationCount 返回仍依赖该存储源的 Multipart 完成日志数量。
func (s *MultipartStore) SourceCompletionOperationCount(storageSourceID int64) (int, error) {
	return s.completionOperationCount(func(op multipartCompletion) bool {
		return op.StorageSourceID == storageSourceID
	})
}

// UserCompletionOperationCount 返回仍引用该用户的 Multipart 完成日志数量。
func (s *MultipartStore) UserCompletionOperationCount(userID int64) (int, error) {
	return s.completionOperationCount(func(op multipartCompletion) bool {
		return op.OwnerUserID == userID
	})
}

func (s *MultipartStore) completionOperationCount(matches func(multipartCompletion) bool) (int, error) {
	entries, err := os.ReadDir(s.completionOperationsDir())
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
		uploadID := strings.TrimSuffix(entry.Name(), ".json")
		op, err := s.readMultipartCompletion(uploadID)
		if err != nil {
			return 0, err
		}
		if matches(*op) {
			count++
		}
	}
	return count, nil
}

func (s *MultipartStore) cleanupMultipartCompletionTemps(_ []os.DirEntry) error {
	return journal.CleanOrphans(s.completionOperationsDir())
}

// RecoverMultipartCompletions 在服务监听前完成或回滚中断的 Multipart 完成操作。
func (s *MultipartStore) RecoverMultipartCompletions() (MultipartCompletionRecoveryResult, error) {
	result := MultipartCompletionRecoveryResult{}
	dir := s.completionOperationsDir()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if err := s.cleanupMultipartCompletionTemps(entries); err != nil {
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
		uploadID := strings.TrimSuffix(entry.Name(), ".json")
		op, err := s.readMultipartCompletion(uploadID)
		if err != nil {
			return result, err
		}
		unlock := s.locks.Lock("multipart:" + uploadID)
		completed, recoveryErr := s.resolveMultipartCompletion(*op, false)
		unlock()
		if recoveryErr != nil {
			return result, recoveryErr
		}
		if completed {
			result.CompletedUploads++
		} else {
			result.RolledBackUploads++
		}
	}
	return result, nil
}
