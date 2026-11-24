package s3api

import (
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/omni-store/omnistore/internal/auth"
	"github.com/omni-store/omnistore/internal/db"
	"github.com/omni-store/omnistore/internal/files"
	"github.com/omni-store/omnistore/internal/lifecycle"
	"github.com/omni-store/omnistore/internal/locks"
	"github.com/omni-store/omnistore/internal/models"
)

const (
	MinMultipartPartSize = 5 * 1024 * 1024
	MaxMultipartPartSize = int64(5 * 1024 * 1024 * 1024)
	MaxMultipartParts    = 10000
	MultipartMaxAge      = 24 * time.Hour
)

var (
	ErrNoSuchUpload     = errors.New("Multipart Upload 不存在")
	ErrInvalidPart      = errors.New("Multipart Part 不存在或 ETag 不匹配")
	ErrInvalidPartOrder = errors.New("Multipart Part 必须按 PartNumber 严格递增")
	ErrEntityTooSmall   = errors.New("除最后一片外，每个 Part 至少为 5 MiB")
	ErrEntityTooLarge   = errors.New("Multipart 数据超过上传大小限制")
)

var uploadIDPattern = regexp.MustCompile(`^mpu_[0-9a-f]{48}$`)

type MultipartUpload struct {
	UploadID        string
	OwnerUserID     int64
	StorageSourceID int64
	ObjectKey       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type MultipartPart struct {
	PartNumber int
	ETag       string
	Size       int64
	CreatedAt  time.Time
}

type CompletedPart struct {
	PartNumber int
	ETag       string
}

type MultipartCleanupResult struct {
	UploadsRemoved int
	OrphansRemoved int
}

// MultipartStore 管理上传状态、临时分片与最终原子合并。
type MultipartStore struct {
	db        *sql.DB
	dataDir   string
	root      string
	files     *files.Service
	maxObject int64
	locks     *locks.Manager
	lifecycle *lifecycle.Manager
	now       func() time.Time
}

// SetLifecycle 注入实例级生命周期读写锁管理器。
func (s *MultipartStore) SetLifecycle(mgr *lifecycle.Manager) {
	s.lifecycle = mgr
}

func (s *MultipartStore) lifecycleManager() *lifecycle.Manager {
	if s.lifecycle != nil {
		return s.lifecycle
	}
	return lifecycle.Default()
}

func NewMultipartStore(db *sql.DB, dataDir string, fileService *files.Service, maxFileSizeMB int64) *MultipartStore {
	return &MultipartStore{
		db: db, dataDir: dataDir, root: filepath.Join(dataDir, "tmp", "multipart"), files: fileService,
		maxObject: maxFileSizeMB * 1024 * 1024, locks: locks.NewManager(), now: time.Now,
	}
}

func (s *MultipartStore) guardLifecycle(userID, storageSourceID int64) (func(), error) {
	release := s.lifecycleManager().Read(lifecycle.Source(storageSourceID), lifecycle.User(userID))
	var found int
	if err := s.db.QueryRow(`SELECT 1 FROM users u, storage_sources source
  WHERE u.id = ? AND source.id = ?`, userID, storageSourceID).Scan(&found); err != nil {
		release()
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoSuchUpload
		}
		return nil, err
	}
	return release, nil
}

func (s *MultipartStore) Create(userID, storageSourceID int64, objectKey string) (*MultipartUpload, error) {
	releaseLifecycle, err := s.guardLifecycle(userID, storageSourceID)
	if err != nil {
		return nil, err
	}
	defer releaseLifecycle()
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return nil, fmt.Errorf("创建 Multipart 临时目录失败: %w", err)
	}
	now := s.now().UTC()
	for range 5 {
		uploadID := "mpu_" + auth.NewRandomToken("", 24)
		dir := s.uploadDir(uploadID)
		if err := os.Mkdir(dir, 0o700); err != nil {
			if os.IsExist(err) {
				continue
			}
			return nil, err
		}
		_, err := s.db.Exec(`INSERT INTO s3_multipart_uploads
  (upload_id, owner_user_id, storage_source_id, object_key, created_at, updated_at)
  VALUES (?, ?, ?, ?, ?, ?)`, uploadID, userID, storageSourceID, objectKey, now, now)
		if err == nil {
			return &MultipartUpload{UploadID: uploadID, OwnerUserID: userID, StorageSourceID: storageSourceID, ObjectKey: objectKey, CreatedAt: now, UpdatedAt: now}, nil
		}
		_ = os.RemoveAll(dir)
		if !db.IsUniqueConstraint(err, "s3_multipart_uploads.upload_id") {
			return nil, err
		}
	}
	return nil, fmt.Errorf("生成 upload_id 失败")
}

func (s *MultipartStore) Get(userID, storageSourceID int64, objectKey, uploadID string) (*MultipartUpload, error) {
	if !uploadIDPattern.MatchString(uploadID) {
		return nil, ErrNoSuchUpload
	}
	var upload MultipartUpload
	err := s.db.QueryRow(`SELECT upload_id, owner_user_id, storage_source_id, object_key, created_at, updated_at
  FROM s3_multipart_uploads WHERE upload_id = ? AND owner_user_id = ? AND storage_source_id = ? AND object_key = ?`,
		uploadID, userID, storageSourceID, objectKey).Scan(&upload.UploadID, &upload.OwnerUserID,
		&upload.StorageSourceID, &upload.ObjectKey, &upload.CreatedAt, &upload.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSuchUpload
	}
	return &upload, err
}

func (s *MultipartStore) UploadPart(userID, storageSourceID int64, objectKey, uploadID string, partNumber int, body io.Reader) (*MultipartPart, error) {
	releaseLifecycle, err := s.guardLifecycle(userID, storageSourceID)
	if err != nil {
		return nil, err
	}
	defer releaseLifecycle()
	if partNumber < 1 || partNumber > MaxMultipartParts {
		return nil, fmt.Errorf("partNumber 必须为 1-10000")
	}
	unlock := s.locks.Lock("multipart:" + uploadID)
	defer unlock()
	upload, err := s.Get(userID, storageSourceID, objectKey, uploadID)
	if err != nil {
		return nil, err
	}
	if err := s.resolveMultipartPartOperationsForUpload(uploadID); err != nil {
		return nil, err
	}
	upload, err = s.Get(userID, storageSourceID, objectKey, uploadID)
	if err != nil {
		return nil, err
	}
	dir := s.uploadDir(uploadID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, ".part-*.tmp")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	md5Hash := md5.New()
	limit := MaxMultipartPartSize
	if s.maxObject > 0 && s.maxObject < limit {
		limit = s.maxObject
	}
	written, err := io.Copy(tmp, io.TeeReader(io.LimitReader(body, limit+1), md5Hash))
	if err != nil {
		cleanup()
		return nil, err
	}
	if written > limit {
		cleanup()
		return nil, ErrEntityTooLarge
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}
	if err := syncMultipartDirectory(dir); err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}

	var otherSize int64
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(size), 0) FROM s3_multipart_parts
  WHERE upload_id = ? AND part_number <> ?`, uploadID, partNumber).Scan(&otherSize); err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}
	if s.maxObject > 0 && otherSize+written > s.maxObject {
		_ = os.Remove(tmpPath)
		return nil, ErrEntityTooLarge
	}

	etag := `"` + hex.EncodeToString(md5Hash.Sum(nil)) + `"`
	previous, err := s.currentMultipartPart(uploadID, partNumber)
	if err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}
	finalPath := s.partPath(uploadID, partNumber)
	backupPath := finalPath + ".previous"
	finalExists, finalInfo, err := checkedMultipartPartFile(finalPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}
	backupExists, _, err := checkedMultipartPartFile(backupPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}
	if previous != nil {
		matches := false
		if finalExists {
			matches, err = multipartPartFileMatches(finalPath, finalInfo, previous.ETag, previous.Size)
		}
		if err != nil || !matches {
			_ = os.Remove(tmpPath)
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("Multipart 旧分片文件缺失或摘要不一致")
		}
	} else if finalExists {
		if err := os.Remove(finalPath); err != nil {
			_ = os.Remove(tmpPath)
			return nil, err
		}
	}
	if backupExists {
		if err := os.Remove(backupPath); err != nil {
			_ = os.Remove(tmpPath)
			return nil, err
		}
	}
	if (previous == nil && finalExists) || backupExists {
		if err := syncMultipartDirectory(dir); err != nil {
			_ = os.Remove(tmpPath)
			return nil, err
		}
	}

	now := s.now().UTC()
	op := s.newMultipartPartOperation(*upload, partNumber, filepath.Base(tmpPath), etag, written, now, previous)
	if err := s.writeMultipartPartOperation(op); err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}
	rollback := func(operationErr error) error {
		if rollbackErr := s.rollbackMultipartPartOperation(op); rollbackErr != nil {
			return errors.Join(operationErr, fmt.Errorf("回滚 Multipart 分片失败: %w", rollbackErr))
		}
		return operationErr
	}
	if previous != nil {
		if err := os.Rename(finalPath, backupPath); err != nil {
			return nil, rollback(err)
		}
		if err := syncMultipartDirectory(dir); err != nil {
			return nil, rollback(err)
		}
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return nil, rollback(err)
	}
	if err := syncMultipartDirectory(dir); err != nil {
		return nil, rollback(err)
	}
	if err := s.commitMultipartPartOperation(op); err != nil {
		return nil, rollback(err)
	}
	// 数据库已经提交后，清理失败交给启动恢复，不能把成功的分片报告为失败。
	_ = s.cleanupCommittedMultipartPart(op)
	return &MultipartPart{PartNumber: partNumber, ETag: etag, Size: written, CreatedAt: now}, nil
}

func (s *MultipartStore) ListParts(userID, storageSourceID int64, objectKey, uploadID string) ([]MultipartPart, error) {
	if _, err := s.Get(userID, storageSourceID, objectKey, uploadID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT part_number, etag, size, created_at FROM s3_multipart_parts
  WHERE upload_id = ? ORDER BY part_number`, uploadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	parts := []MultipartPart{}
	for rows.Next() {
		var part MultipartPart
		if err := rows.Scan(&part.PartNumber, &part.ETag, &part.Size, &part.CreatedAt); err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	return parts, rows.Err()
}

func (s *MultipartStore) Complete(userID int64, src *models.StorageSource, objectKey, uploadID string, requested []CompletedPart) (string, int64, error) {
	releaseLifecycle, err := s.guardLifecycle(userID, src.ID)
	if err != nil {
		return "", 0, err
	}
	lifecycleHeld := true
	defer func() {
		if lifecycleHeld {
			releaseLifecycle()
		}
	}()
	unlock := s.locks.Lock("multipart:" + uploadID)
	defer unlock()
	if _, err := s.Get(userID, src.ID, objectKey, uploadID); err != nil {
		return "", 0, err
	}
	if err := s.resolveMultipartPartOperationsForUpload(uploadID); err != nil {
		return "", 0, err
	}
	if _, err := s.Get(userID, src.ID, objectKey, uploadID); err != nil {
		return "", 0, err
	}
	if len(requested) == 0 || len(requested) > MaxMultipartParts {
		return "", 0, ErrInvalidPart
	}
	for i, part := range requested {
		if part.PartNumber < 1 || part.PartNumber > MaxMultipartParts {
			return "", 0, ErrInvalidPart
		}
		if i > 0 && requested[i-1].PartNumber >= part.PartNumber {
			return "", 0, ErrInvalidPartOrder
		}
	}

	stored, err := s.partsByNumber(uploadID)
	if err != nil {
		return "", 0, err
	}
	selected := make([]MultipartPart, len(requested))
	var total int64
	combinedMD5 := md5.New()
	for i, requestedPart := range requested {
		part, ok := stored[requestedPart.PartNumber]
		if !ok || normalizeETag(part.ETag) != normalizeETag(requestedPart.ETag) {
			return "", 0, ErrInvalidPart
		}
		if i < len(requested)-1 && part.Size < MinMultipartPartSize {
			return "", 0, ErrEntityTooSmall
		}
		digest, err := hex.DecodeString(normalizeETag(part.ETag))
		if err != nil || len(digest) != md5.Size {
			return "", 0, ErrInvalidPart
		}
		_, _ = combinedMD5.Write(digest)
		total += part.Size
		selected[i] = part
	}
	if s.maxObject > 0 && total > s.maxObject {
		return "", 0, ErrEntityTooLarge
	}
	etag := `"` + hex.EncodeToString(combinedMD5.Sum(nil)) + "-" + strconv.Itoa(len(selected)) + `"`
	contentSHA256, err := s.hashSelectedParts(uploadID, selected)
	if err != nil {
		return "", 0, err
	}
	previousExists, previousSize, previousMTimeNano, err := s.previousObjectState(src, objectKey)
	if err != nil {
		return "", 0, err
	}
	op := s.newMultipartCompletion(uploadID, userID, src.ID, objectKey, etag, total, contentSHA256,
		previousExists, previousSize, previousMTimeNano)
	if existing, err := s.readMultipartCompletion(uploadID); err != nil {
		return "", 0, err
	} else if existing != nil {
		if !sameMultipartCompletion(*existing, op) {
			return "", 0, fmt.Errorf("Multipart Upload 存在不一致的待恢复完成操作")
		}
		completed, recoveryErr := s.resolveMultipartCompletion(*existing, false)
		if completed {
			return etag, total, nil
		}
		if recoveryErr != nil {
			return "", 0, recoveryErr
		}
	}
	if err := s.writeMultipartCompletion(op); err != nil {
		return "", 0, err
	}
	// 普通上传会取得同一来源和用户的共享生命周期锁。完成日志已经
	// 落盘，释放当前锁后删除流程会通过日志计数拒绝删除，避免嵌套
	// RWMutex 在等待写锁时自锁。
	releaseLifecycle()
	lifecycleHeld = false
	createdParents, err := s.files.EnsureObjectParentsCreated(src, objectKey)
	if err != nil {
		_ = s.removeMultipartCompletion(uploadID)
		return "", 0, err
	}
	reader := &sequentialPartReader{store: s, uploadID: uploadID, parts: selected}
	dir, filename := path.Split(objectKey)
	dir = strings.TrimSuffix(dir, "/")
	_, written, err := s.files.UploadWithLockTokens(src, dir, filename, reader, true, nil, &userID)
	_ = reader.Close()
	if err != nil {
		completed, recoveryErr := s.resolveMultipartCompletion(op, false)
		if completed {
			return etag, total, nil
		}
		s.files.CleanupEmptyDirs(src, createdParents)
		if recoveryErr != nil {
			return "", 0, errors.Join(err, recoveryErr)
		}
		return "", 0, err
	}
	if written != total {
		return "", 0, fmt.Errorf("Multipart 合并大小不匹配")
	}
	completed, err := s.resolveMultipartCompletion(op, true)
	if !completed {
		if err == nil {
			err = fmt.Errorf("Multipart 最终对象与已提交分片不一致")
		}
		return "", 0, err
	}
	// 数据库已提交后清理失败留给启动恢复，不能诱发客户端重复完成。
	return etag, total, nil
}

// RememberObjectETag 保存 Multipart 完成后的非普通 MD5 ETag。
func (s *MultipartStore) RememberObjectETag(storageSourceID int64, objectKey, etag string, size int64, mtime time.Time) error {
	_, err := s.db.Exec(`INSERT INTO s3_object_etags
	  (storage_source_id, object_key, etag, size, mtime_unix_nano, updated_at) VALUES (?, ?, ?, ?, ?, ?)
  ON CONFLICT(storage_source_id, object_key) DO UPDATE SET etag = excluded.etag, size = excluded.size,
    mtime_unix_nano = excluded.mtime_unix_nano, updated_at = excluded.updated_at`,
		storageSourceID, objectKey, etag, size, mtime.UnixNano(), s.now().UTC())
	return err
}

// ObjectETag 返回仍与物理文件 size + mtime 匹配的 Multipart ETag。
func (s *MultipartStore) ObjectETag(storageSourceID int64, objectKey string, size int64, mtime time.Time) (string, bool, error) {
	var etag string
	var storedSize, storedMTime int64
	err := s.db.QueryRow(`SELECT etag, size, mtime_unix_nano FROM s3_object_etags
  WHERE storage_source_id = ? AND object_key = ?`, storageSourceID, objectKey).Scan(&etag, &storedSize, &storedMTime)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if storedSize != size || storedMTime != mtime.UnixNano() {
		if err := s.ForgetObjectETag(storageSourceID, objectKey); err != nil {
			return "", false, err
		}
		return "", false, nil
	}
	return etag, true, nil
}

// ForgetObjectETag 移除被普通 PUT、DELETE 或外部修改取代的 Multipart ETag。
func (s *MultipartStore) ForgetObjectETag(storageSourceID int64, objectKey string) error {
	_, err := s.db.Exec(`DELETE FROM s3_object_etags WHERE storage_source_id = ? AND object_key = ?`, storageSourceID, objectKey)
	return err
}

func (s *MultipartStore) Abort(userID, storageSourceID int64, objectKey, uploadID string) error {
	releaseLifecycle, err := s.guardLifecycle(userID, storageSourceID)
	if err != nil {
		return err
	}
	defer releaseLifecycle()
	unlock := s.locks.Lock("multipart:" + uploadID)
	defer unlock()
	if _, err := s.Get(userID, storageSourceID, objectKey, uploadID); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM s3_multipart_uploads WHERE upload_id = ?`, uploadID); err != nil {
		return err
	}
	return errors.Join(os.RemoveAll(s.uploadDir(uploadID)), s.removeMultipartCompletion(uploadID),
		s.removeMultipartPartOperationsForUpload(uploadID))
}

func (s *MultipartStore) CleanupExpired(maxAge time.Duration) (MultipartCleanupResult, error) {
	if maxAge <= 0 {
		maxAge = MultipartMaxAge
	}
	cutoff := s.now().UTC().Add(-maxAge)
	rows, err := s.db.Query(`SELECT upload_id FROM s3_multipart_uploads WHERE updated_at < ?`, cutoff)
	if err != nil {
		return MultipartCleanupResult{}, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return MultipartCleanupResult{}, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return MultipartCleanupResult{}, err
	}
	result := MultipartCleanupResult{}
	for _, id := range ids {
		unlock := s.locks.Lock("multipart:" + id)
		pending, pendingErr := s.multipartCompletionExists(id)
		if pendingErr != nil {
			unlock()
			return result, pendingErr
		}
		if pending {
			unlock()
			continue
		}
		partOperationCount, pendingErr := s.multipartPartOperationCountForUpload(id)
		if pendingErr != nil {
			unlock()
			return result, pendingErr
		}
		if partOperationCount > 0 {
			unlock()
			continue
		}
		res, deleteErr := s.db.Exec(`DELETE FROM s3_multipart_uploads WHERE upload_id = ? AND updated_at < ?`, id, cutoff)
		if deleteErr == nil {
			if n, _ := res.RowsAffected(); n > 0 {
				_ = os.RemoveAll(s.uploadDir(id))
				result.UploadsRemoved++
			}
		}
		unlock()
		if deleteErr != nil {
			return result, deleteErr
		}
	}

	entries, err := os.ReadDir(s.root)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if !uploadIDPattern.MatchString(entry.Name()) {
			continue
		}
		pending, pendingErr := s.multipartCompletionExists(entry.Name())
		if pendingErr != nil {
			return result, pendingErr
		}
		if pending {
			continue
		}
		partOperationCount, pendingErr := s.multipartPartOperationCountForUpload(entry.Name())
		if pendingErr != nil {
			return result, pendingErr
		}
		if partOperationCount > 0 {
			continue
		}
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM s3_multipart_uploads WHERE upload_id = ?`, entry.Name()).Scan(&count); err != nil {
			return result, err
		}
		if count > 0 {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			if err := os.RemoveAll(s.uploadDir(entry.Name())); err == nil {
				result.OrphansRemoved++
			}
		}
	}
	return result, nil
}

func (s *MultipartStore) partsByNumber(uploadID string) (map[int]MultipartPart, error) {
	parts, err := s.listPartsUnchecked(uploadID)
	if err != nil {
		return nil, err
	}
	out := make(map[int]MultipartPart, len(parts))
	for _, part := range parts {
		out[part.PartNumber] = part
	}
	return out, nil
}

func (s *MultipartStore) listPartsUnchecked(uploadID string) ([]MultipartPart, error) {
	rows, err := s.db.Query(`SELECT part_number, etag, size, created_at FROM s3_multipart_parts WHERE upload_id = ?`, uploadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	parts := []MultipartPart{}
	for rows.Next() {
		var part MultipartPart
		if err := rows.Scan(&part.PartNumber, &part.ETag, &part.Size, &part.CreatedAt); err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })
	return parts, rows.Err()
}

func normalizeETag(value string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(value), `"`))
}

func (s *MultipartStore) uploadDir(uploadID string) string {
	return filepath.Join(s.root, uploadID)
}

func (s *MultipartStore) partPath(uploadID string, partNumber int) string {
	return filepath.Join(s.uploadDir(uploadID), fmt.Sprintf("%05d.part", partNumber))
}

type sequentialPartReader struct {
	store    *MultipartStore
	uploadID string
	parts    []MultipartPart
	index    int
	current  *os.File
}

func (r *sequentialPartReader) Read(p []byte) (int, error) {
	for {
		if r.current == nil {
			if r.index >= len(r.parts) {
				return 0, io.EOF
			}
			part := r.parts[r.index]
			file, err := os.Open(r.store.partPath(r.uploadID, part.PartNumber))
			if err != nil {
				return 0, err
			}
			r.current = file
		}
		n, err := r.current.Read(p)
		if errors.Is(err, io.EOF) {
			_ = r.current.Close()
			r.current = nil
			r.index++
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (r *sequentialPartReader) Close() error {
	if r.current != nil {
		return r.current.Close()
	}
	return nil
}
