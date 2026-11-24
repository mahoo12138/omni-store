package imagebed

import (
	"database/sql"
	"encoding/hex"
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
	"github.com/omni-store/omnistore/internal/files"
	"github.com/omni-store/omnistore/internal/journal"
	"github.com/omni-store/omnistore/internal/models"
	"github.com/omni-store/omnistore/internal/security"
)

const imageUploadOperationVersion = 1

type imageUploadOperation struct {
	Version           int        `json:"version"`
	OperationID       string     `json:"operation_id"`
	StorageSourceID   int64      `json:"storage_source_id"`
	TempRelativePath  string     `json:"temp_relative_path"`
	FinalRelativePath string     `json:"final_relative_path"`
	ImageID           string     `json:"image_id"`
	OwnerType         string     `json:"owner_type"`
	OwnerUserID       *int64     `json:"owner_user_id,omitempty"`
	OriginalFilename  string     `json:"original_filename"`
	PublicURL         string     `json:"public_url"`
	Size              int64      `json:"size"`
	MimeType          string     `json:"mime_type"`
	Width             int        `json:"width"`
	Height            int        `json:"height"`
	Ext               string     `json:"ext"`
	ExpiresAt         *time.Time `json:"expires_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
}

// UploadRecoveryResult 描述启动时清理的中断图床上传。
type UploadRecoveryResult struct {
	CompletedUploads  int `json:"completed_uploads"`
	RolledBackUploads int `json:"rolled_back_uploads"`
}

func (s *Service) uploadOperationsDir() string {
	return filepath.Join(s.sources.DataDir(), "operations", "image-uploads")
}

func (s *Service) uploadOperationPath(operationID string) string {
	return filepath.Join(s.uploadOperationsDir(), operationID+".json")
}

func (s *Service) uploadPreparedPath(operationID string) string {
	return filepath.Join(s.uploadOperationsDir(), operationID+".prepared")
}

func validHexToken(value, prefix string, bytes int) bool {
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+bytes*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil
}

func validImageUploadTempName(name string) bool {
	const prefix = ".omnistore-upload-"
	const suffix = ".tmp"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return false
	}
	token := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	if len(token) != 16 {
		return false
	}
	_, err := hex.DecodeString(token)
	return err == nil
}

func validateImageUploadPlan(op imageUploadOperation) error {
	if op.Version != imageUploadOperationVersion || !validHexToken(op.OperationID, "iup-", 12) ||
		op.StorageSourceID <= 0 || op.CreatedAt.IsZero() {
		return fmt.Errorf("非法图床上传操作日志")
	}
	if op.OwnerType == models.ImageOwnerUser {
		if op.OwnerUserID == nil || *op.OwnerUserID <= 0 {
			return fmt.Errorf("用户图床上传缺少所有者")
		}
	} else if op.OwnerType != models.ImageOwnerAnonymous || op.OwnerUserID != nil {
		return fmt.Errorf("非法图床上传所有者")
	}
	normalized, err := security.NormalizeRelPath(op.TempRelativePath)
	if err != nil || normalized == "" || normalized != op.TempRelativePath ||
		!validImageUploadTempName(filepath.Base(filepath.FromSlash(op.TempRelativePath))) {
		return fmt.Errorf("非法图床上传临时路径")
	}
	return nil
}

func validateImageUploadOperation(op imageUploadOperation) error {
	if err := validateImageUploadPlan(op); err != nil {
		return err
	}
	if !validHexToken(op.ImageID, "img_", 16) || op.Size <= 0 || op.Width <= 0 || op.Height <= 0 ||
		op.Ext == "" || op.MimeType == "" || op.FinalRelativePath == "" || op.PublicURL == "" {
		return fmt.Errorf("图床上传准备信息不完整")
	}
	for label, value := range map[string]string{"最终": op.FinalRelativePath} {
		normalized, err := security.NormalizeRelPath(value)
		if err != nil || normalized == "" || normalized != value {
			return fmt.Errorf("非法图床上传%s路径", label)
		}
	}
	if filepath.ToSlash(filepath.Dir(filepath.FromSlash(op.TempRelativePath))) !=
		filepath.ToSlash(filepath.Dir(filepath.FromSlash(op.FinalRelativePath))) ||
		!validImageUploadTempName(filepath.Base(filepath.FromSlash(op.TempRelativePath))) {
		return fmt.Errorf("图床上传临时文件不在最终目录")
	}
	random := strings.TrimPrefix(op.ImageID, "img_")
	if filepath.Base(filepath.FromSlash(op.FinalRelativePath)) != random+"."+op.Ext ||
		!strings.HasSuffix(op.PublicURL, "/i/"+op.ImageID+"."+op.Ext) {
		return fmt.Errorf("图床上传公开标识与最终路径不一致")
	}
	return nil
}

func validateStoredImageUploadPlan(op imageUploadOperation) error {
	if err := validateImageUploadPlan(op); err != nil {
		return err
	}
	if op.FinalRelativePath != "" || op.ImageID != "" || op.PublicURL != "" || op.Size != 0 ||
		op.MimeType != "" || op.Width != 0 || op.Height != 0 || op.Ext != "" {
		return fmt.Errorf("planned 图床上传包含准备信息")
	}
	return nil
}

func sameImageUploadPlan(plan, prepared imageUploadOperation) bool {
	equalOwner := (plan.OwnerUserID == nil && prepared.OwnerUserID == nil) ||
		(plan.OwnerUserID != nil && prepared.OwnerUserID != nil && *plan.OwnerUserID == *prepared.OwnerUserID)
	return plan.Version == prepared.Version && plan.OperationID == prepared.OperationID &&
		plan.StorageSourceID == prepared.StorageSourceID && plan.TempRelativePath == prepared.TempRelativePath &&
		plan.OwnerType == prepared.OwnerType && equalOwner && plan.OriginalFilename == prepared.OriginalFilename &&
		plan.CreatedAt.Equal(prepared.CreatedAt)
}

func (s *Service) newImageUploadPlan(srcID int64, tempRel, ownerType string,
	ownerUserID *int64, originalFilename string) imageUploadOperation {
	return imageUploadOperation{
		Version: imageUploadOperationVersion, OperationID: auth.NewRandomToken("iup-", 12),
		StorageSourceID: srcID, TempRelativePath: tempRel, OwnerType: ownerType,
		OwnerUserID: ownerUserID, OriginalFilename: originalFilename, CreatedAt: time.Now().UTC(),
	}
}

func (s *Service) newImageUploadOperation(srcID int64, tempRel, finalRel, imageID, ownerType string,
	ownerUserID *int64, originalFilename, publicURL string, size int64, info *ImageInfo) imageUploadOperation {
	op := s.newImageUploadPlan(srcID, tempRel, ownerType, ownerUserID, originalFilename)
	op.FinalRelativePath, op.ImageID, op.PublicURL, op.Size = finalRel, imageID, publicURL, size
	op.MimeType, op.Width, op.Height, op.Ext = info.MimeType, info.Width, info.Height, info.Ext
	return op
}

func (s *Service) writeImageUploadOperation(op imageUploadOperation) error {
	if err := validateImageUploadOperation(op); err != nil {
		return err
	}
	plan := op
	plan.FinalRelativePath, plan.ImageID, plan.PublicURL, plan.Size = "", "", "", 0
	plan.MimeType, plan.Width, plan.Height, plan.Ext = "", 0, 0, ""
	if err := s.writeImageUploadPlan(plan); err != nil {
		return err
	}
	return s.writePreparedImageUploadOperation(op)
}

func (s *Service) writeImageUploadPlan(op imageUploadOperation) error {
	if err := validateStoredImageUploadPlan(op); err != nil {
		return err
	}
	if err := s.writeImageUploadOperationFile(s.uploadOperationPath(op.OperationID), op); err != nil {
		return err
	}
	if err := os.Remove(s.uploadPreparedPath(op.OperationID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return syncImageDirectory(s.uploadOperationsDir())
}

func (s *Service) writePreparedImageUploadOperation(op imageUploadOperation) error {
	if err := validateImageUploadOperation(op); err != nil {
		return err
	}
	handle, err := os.Open(s.uploadOperationPath(op.OperationID))
	if err != nil {
		return err
	}
	plan, decodeErr := decodeImageUploadOperation(handle)
	closeErr := handle.Close()
	if decodeErr != nil || closeErr != nil {
		return errors.Join(decodeErr, closeErr)
	}
	if err := validateStoredImageUploadPlan(plan); err != nil || !sameImageUploadPlan(plan, op) {
		return fmt.Errorf("图床上传准备信息与 planned journal 不一致")
	}
	return s.writeImageUploadOperationFile(s.uploadPreparedPath(op.OperationID), op)
}

func (s *Service) writeImageUploadOperationFile(target string, op imageUploadOperation) error {
	return journal.WriteJSONExclusive(s.uploadOperationsDir(), target, op)
}

func (s *Service) removeImageUploadOperation(operationID string) error {
	return journal.RemoveFiles(s.uploadOperationsDir(),
		s.uploadOperationPath(operationID),
		s.uploadPreparedPath(operationID),
	)
}

func syncImageDirectory(dir string) error {
	return journal.SyncDir(dir)
}

func (s *Service) rollbackUncommittedImageUpload(op imageUploadOperation, absPath string) error {
	removeErr := os.Remove(absPath)
	if errors.Is(removeErr, fs.ErrNotExist) {
		removeErr = nil
	}
	var syncErr error
	if removeErr == nil {
		syncErr = syncImageDirectory(filepath.Dir(absPath))
	}
	if removeErr == nil && syncErr == nil {
		return s.removeImageUploadOperation(op.OperationID)
	}
	return errors.Join(removeErr, syncErr)
}

func (s *Service) commitImageUpload(op imageUploadOperation, prepared *files.PreparedFileRecord) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO images
  (image_id, owner_type, owner_user_id, storage_source_id, relative_path, original_filename,
   public_url, size, mime_type, width, height, ext, expires_at, created_at)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		op.ImageID, op.OwnerType, op.OwnerUserID, op.StorageSourceID, op.FinalRelativePath,
		op.OriginalFilename, op.PublicURL, op.Size, op.MimeType, op.Width, op.Height, op.Ext,
		op.ExpiresAt, op.CreatedAt)
	if err != nil {
		return 0, err
	}
	if err := s.files.RecordPreparedFileTx(tx, prepared); err != nil {
		return 0, err
	}
	if err := s.files.AdjustSourceUsageTx(tx, op.StorageSourceID, op.Size); err != nil {
		return 0, err
	}
	rowID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return rowID, nil
}

func decodeImageUploadOperation(handle io.Reader) (imageUploadOperation, error) {
	return journal.DecodeStrictJSON[imageUploadOperation](handle)
}

func (s *Service) committedImageUpload(op imageUploadOperation) (bool, error) {
	var ownerType, originalFilename, publicURL, mimeType, ext string
	var ownerUserID sql.NullInt64
	var size int64
	var width, height int
	err := s.db.QueryRow(`SELECT owner_type, owner_user_id, COALESCE(original_filename, ''),
  public_url, size, mime_type, width, height, ext FROM images WHERE image_id = ?`, op.ImageID).
		Scan(&ownerType, &ownerUserID, &originalFilename, &publicURL, &size, &mimeType, &width, &height, &ext)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if ownerType != op.OwnerType || originalFilename != op.OriginalFilename || publicURL != op.PublicURL ||
		size != op.Size || mimeType != op.MimeType || width != op.Width || height != op.Height || ext != op.Ext {
		return false, fmt.Errorf("图床上传 %s 的已提交图片元数据与操作日志不一致", op.OperationID)
	}
	if op.OwnerType == models.ImageOwnerAnonymous && ownerUserID.Valid {
		return false, fmt.Errorf("图床上传 %s 的匿名图片出现用户所有者", op.OperationID)
	}
	if op.OwnerType == models.ImageOwnerUser && ownerUserID.Valid && ownerUserID.Int64 != *op.OwnerUserID {
		return false, fmt.Errorf("图床上传 %s 的用户所有者不一致", op.OperationID)
	}
	return true, nil
}

func checkedImageUploadPath(root, relPath string) (string, bool, error) {
	absPath, err := security.ResolveInSource(root, relPath)
	if err != nil {
		return "", false, err
	}
	info, err := os.Lstat(absPath)
	if errors.Is(err, fs.ErrNotExist) {
		return absPath, false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", false, fmt.Errorf("图床上传恢复路径 %s 不是普通文件", relPath)
	}
	return absPath, true, nil
}

// SourceUploadOperationCount 返回仍依赖该存储源根路径的中断图床上传数量。
func (s *Service) SourceUploadOperationCount(storageSourceID int64) (int, error) {
	return s.imageUploadOperationCount(func(op imageUploadOperation) bool {
		return op.StorageSourceID == storageSourceID
	})
}

// UserUploadOperationCount 返回仍引用该用户的中断图床上传数量。
func (s *Service) UserUploadOperationCount(userID int64) (int, error) {
	return s.imageUploadOperationCount(func(op imageUploadOperation) bool {
		return op.OwnerUserID != nil && *op.OwnerUserID == userID
	})
}

func (s *Service) imageUploadOperationCount(matches func(imageUploadOperation) bool) (int, error) {
	entries, err := os.ReadDir(s.uploadOperationsDir())
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
			return 0, fmt.Errorf("图床上传操作日志 %s 不能是符号链接", entry.Name())
		}
		handle, err := os.Open(filepath.Join(s.uploadOperationsDir(), entry.Name()))
		if err != nil {
			return 0, err
		}
		op, decodeErr := decodeImageUploadOperation(handle)
		closeErr := handle.Close()
		if decodeErr != nil {
			return 0, fmt.Errorf("读取图床上传操作日志 %s 失败: %w", entry.Name(), decodeErr)
		}
		if closeErr != nil {
			return 0, closeErr
		}
		if err := validateStoredImageUploadPlan(op); err != nil || entry.Name() != op.OperationID+".json" {
			return 0, fmt.Errorf("图床上传操作日志 %s 非法", entry.Name())
		}
		if matches(op) {
			count++
		}
	}
	return count, nil
}

// RecoverUploadOperations 在服务监听前按 images 是否存在作为 SQLite 提交边界：
// 已提交上传只清理日志；未提交上传删除内部临时文件或随机最终文件。任何无法
// 证明由该操作独占的状态都会阻止启动。
func (s *Service) RecoverUploadOperations() (UploadRecoveryResult, error) {
	result := UploadRecoveryResult{}
	dir := s.uploadOperationsDir()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if err := journal.CleanOrphans(dir, ".prepared"); err != nil {
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
			return result, fmt.Errorf("图床上传操作日志 %s 不能是符号链接", entry.Name())
		}
		handle, err := os.Open(filepath.Join(dir, entry.Name()))
		if err != nil {
			return result, err
		}
		op, decodeErr := decodeImageUploadOperation(handle)
		closeErr := handle.Close()
		if decodeErr != nil {
			return result, fmt.Errorf("读取图床上传操作日志 %s 失败: %w", entry.Name(), decodeErr)
		}
		if closeErr != nil {
			return result, closeErr
		}
		if err := validateStoredImageUploadPlan(op); err != nil {
			return result, fmt.Errorf("图床上传操作日志 %s 非法: %w", entry.Name(), err)
		}
		if entry.Name() != op.OperationID+".json" {
			return result, fmt.Errorf("图床上传操作日志文件名 %s 与 operation_id 不一致", entry.Name())
		}
		src, err := s.sources.GetByID(op.StorageSourceID)
		if err != nil {
			return result, fmt.Errorf("图床上传 %s 的存储源不存在: %w", op.OperationID, err)
		}
		preparedHandle, err := os.Open(s.uploadPreparedPath(op.OperationID))
		if errors.Is(err, fs.ErrNotExist) {
			tempAbs, tempExists, pathErr := checkedImageUploadPath(src.RootPath, op.TempRelativePath)
			if pathErr != nil {
				return result, pathErr
			}
			if tempExists {
				if err := os.Remove(tempAbs); err != nil {
					return result, err
				}
				if err := syncImageDirectory(filepath.Dir(tempAbs)); err != nil {
					return result, err
				}
			}
			if err := s.removeImageUploadOperation(op.OperationID); err != nil {
				return result, err
			}
			result.RolledBackUploads++
			continue
		}
		if err != nil {
			return result, err
		}
		prepared, decodeErr := decodeImageUploadOperation(preparedHandle)
		closeErr = preparedHandle.Close()
		if decodeErr != nil || closeErr != nil {
			return result, fmt.Errorf("读取图床上传 prepared journal 失败: %w", errors.Join(decodeErr, closeErr))
		}
		if err := validateImageUploadOperation(prepared); err != nil || !sameImageUploadPlan(op, prepared) {
			return result, fmt.Errorf("图床上传 %s 的 prepared journal 非法", op.OperationID)
		}
		op = prepared
		committed, err := s.committedImageUpload(op)
		if err != nil {
			return result, err
		}
		if committed {
			if err := s.removeImageUploadOperation(op.OperationID); err != nil {
				return result, err
			}
			result.CompletedUploads++
			continue
		}

		tempAbs, tempExists, err := checkedImageUploadPath(src.RootPath, op.TempRelativePath)
		if err != nil {
			return result, err
		}
		finalAbs, finalExists, err := checkedImageUploadPath(src.RootPath, op.FinalRelativePath)
		if err != nil {
			return result, err
		}
		if tempExists && finalExists {
			return result, fmt.Errorf("图床上传 %s 同时存在临时与最终文件", op.OperationID)
		}
		var recordCount int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM file_records WHERE storage_source_id = ? AND relative_path = ?`,
			op.StorageSourceID, op.FinalRelativePath).Scan(&recordCount); err != nil {
			return result, err
		}
		if recordCount != 0 {
			return result, fmt.Errorf("图床上传 %s 未提交图片但存在文件台账", op.OperationID)
		}
		removePath := ""
		if tempExists {
			removePath = tempAbs
		} else if finalExists {
			removePath = finalAbs
		}
		if removePath != "" {
			if err := os.Remove(removePath); err != nil {
				return result, fmt.Errorf("回滚图床上传 %s 文件失败: %w", op.OperationID, err)
			}
			if err := syncImageDirectory(filepath.Dir(removePath)); err != nil {
				return result, fmt.Errorf("同步图床上传 %s 目录失败: %w", op.OperationID, err)
			}
		}
		if err := s.removeImageUploadOperation(op.OperationID); err != nil {
			return result, err
		}
		result.RolledBackUploads++
	}
	return result, nil
}
