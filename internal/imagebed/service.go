package imagebed

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/omni-store/omnistore/internal/auth"
	"github.com/omni-store/omnistore/internal/files"
	"github.com/omni-store/omnistore/internal/lifecycle"
	"github.com/omni-store/omnistore/internal/locks"
	"github.com/omni-store/omnistore/internal/managedroot"
	"github.com/omni-store/omnistore/internal/models"
	"github.com/omni-store/omnistore/internal/security"
	"github.com/omni-store/omnistore/internal/sources"
)

var (
	// ErrNoTarget 图床能力未开启或未绑定存储源。
	ErrNoTarget = errors.New("图床未开启或未绑定存储源")
	// ErrTargetInvalid 目标存储源不可用。
	ErrTargetInvalid = errors.New("图床目标不可用")
	// ErrNotFound 图片不存在。
	ErrNotFound = errors.New("图片不存在")
	// ErrAnonymousDisabled 匿名图床未开启。
	ErrAnonymousDisabled = errors.New("匿名公共图床未开启")
	// ErrRetentionInvalid 保留天数非法。
	ErrRetentionInvalid = errors.New("保留天数必须是非负整数")
)

// 系统设置 key（README §22.9）。2.0 起图床目标由 Site Capability 绑定决定。
const (
	SettingAnonymousEnabled = "anonymous_image_bed_enabled"
	// 保留策略（IMG-04）：默认 0 = 不自动过期；匿名与登录用户相互独立。
	SettingRetentionDays          = "image_bed_retention_days"
	SettingAnonymousRetentionDays = "image_bed_anonymous_retention_days"
)

// 托管命名空间布局：新图统一写入 `.omnistore/image-bed/`。
const (
	managedScopeDir  = "image-bed"
	ledgerScopeValue = models.FileRecordScopeImageBed
)

// capabilitiesLookup 是图床需要的 Site Capability 窄接口。
type capabilitiesLookup interface {
	ResolveSource(capability string) (*models.StorageSource, error)
}

// Service 提供图床能力。
type Service struct {
	db             *sql.DB
	publicURL      string
	thumbnailCache string
	sources        *sources.Service
	capabilities   capabilitiesLookup
	managedRoots   *managedroot.Service
	files          *files.Service
	locks          *locks.Manager
	lifecycle      *lifecycle.Manager
	thumbnailLocks *locks.Manager
	thumbnailSlot  chan struct{}
}

// SetLifecycle 注入实例级生命周期读写锁管理器。
func (s *Service) SetLifecycle(mgr *lifecycle.Manager) {
	s.lifecycle = mgr
}

func (s *Service) lifecycleManager() *lifecycle.Manager {
	if s.lifecycle != nil {
		return s.lifecycle
	}
	return lifecycle.Default()
}

// NewService 创建图床服务。
func NewService(db *sql.DB, publicURL, thumbnailCache string, srcSvc *sources.Service, capSvc capabilitiesLookup, fileSvc *files.Service) (*Service, error) {
	if thumbnailCache == "" {
		return nil, errors.New("缩略图缓存目录不能为空")
	}
	if err := os.MkdirAll(thumbnailCache, 0o755); err != nil {
		return nil, fmt.Errorf("创建缩略图缓存目录失败: %w", err)
	}
	return &Service{
		db: db, publicURL: strings.TrimRight(publicURL, "/"), thumbnailCache: thumbnailCache,
		sources: srcSvc, capabilities: capSvc, managedRoots: managedroot.NewService(db),
		files: fileSvc, locks: fileSvc.Locks(), thumbnailLocks: locks.NewManager(),
		thumbnailSlot: make(chan struct{}, 1),
	}, nil
}

// --- 图床目标 ---

// currentTarget 返回图床能力当前绑定的存储源。
// 服务授权只在自己托管范围内有效，不要求用户对该源有路径 Policy。
func (s *Service) currentTarget() (*models.StorageSource, error) {
	src, err := s.capabilities.ResolveSource("image_bed")
	if err != nil {
		return nil, ErrNoTarget
	}
	return src, nil
}

// ThumbnailCacheDir 暴露缩略图缓存目录（运维统计用）。
func (s *Service) ThumbnailCacheDir() string {
	return s.thumbnailCache
}

// CurrentTarget 暴露当前绑定目标给状态查询入口。
func (s *Service) CurrentTarget() (*models.StorageSource, error) {
	return s.currentTarget()
}

// imageIsManaged 判断图片是否位于 2.0 托管命名空间布局。
// 旧布局图片记录继续按普通路径读取/删除，不重新映射到新根（IMG-03）。
func imageIsManaged(relativePath string) bool {
	return security.ContainsManagedNamespace(relativePath)
}

// openImageOriginal 按记录布局选择读取原语。
func (s *Service) openImageOriginal(src *models.StorageSource, relativePath string) (*os.File, os.FileInfo, func(), error) {
	if imageIsManaged(relativePath) {
		return s.files.OpenManagedForRead(src, relativePath, managedScopeDir)
	}
	f, info, unlock, err := s.files.OpenForRead(src, relativePath)
	return f, info, unlock, err
}

// checkTarget 返回图床能力绑定的存储源。2.0 起用户不再选择源。
func (s *Service) checkTarget() (*models.StorageSource, error) {
	return s.currentTarget()
}

func (s *Service) userImageRelDir(user *models.User, now time.Time) string {
	return fmt.Sprintf("%s/%s/users/%s/%04d/%02d", security.ManagedNamespaceSegment, managedScopeDir,
		user.UserPublicID, now.Year(), int(now.Month()))
}

func anonymousImageRelDir(now time.Time) string {
	return fmt.Sprintf("%s/%s/anonymous/%04d/%02d", security.ManagedNamespaceSegment, managedScopeDir,
		now.Year(), int(now.Month()))
}

// --- 上传 ---

// UploadForUser 登录用户上传图片。目标由 Site Capability 绑定决定（README §17.3）。
func (s *Service) UploadForUser(user *models.User, originalFilename string, body io.Reader) (*models.Image, error) {
	now := time.Now().UTC()
	relDir := s.userImageRelDir(user, now)
	src, err := s.checkTarget()
	if err != nil {
		return nil, err
	}
	return s.upload(src, relDir, originalFilename, models.ImageOwnerUser, &user.ID, body)
}

// AnonymousSettings 是匿名图床配置。
type AnonymousSettings struct {
	Enabled bool   `json:"enabled"`
	Key     string `json:"key"`
}

// GetAnonymousSettings 读取匿名图床配置（README §17.5，默认关闭）。
// 目标由 Site Capability 绑定决定；Enabled 只代表入口开关。
func (s *Service) GetAnonymousSettings() (*AnonymousSettings, error) {
	out := &AnonymousSettings{}
	var value string
	err := s.db.QueryRow(`SELECT value FROM system_settings WHERE key = ?`, SettingAnonymousEnabled).Scan(&value)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	out.Enabled = value == "1" || strings.EqualFold(value, "true")
	if !out.Enabled {
		return out, nil
	}
	src, err := s.capabilities.ResolveSource("image_bed")
	if err != nil {
		// 绑定不可用时入口保持开启状态但不可上传。
		out.Key = ""
		return out, nil
	}
	out.Key = src.Key
	return out, nil
}

// SetAnonymousSettings 更新匿名图床入口开关（仅超级管理员入口调用）。
func (s *Service) SetAnonymousSettings(enabled bool) error {
	value := "false"
	if enabled {
		value = "true"
	}
	_, err := s.db.Exec(`INSERT INTO system_settings (key, value, updated_at) VALUES (?, ?, ?)
  ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		SettingAnonymousEnabled, value, time.Now().UTC())
	return err
}

// UploadAnonymous 匿名上传图片（README §17.5）。调用方负责限流。
func (s *Service) UploadAnonymous(originalFilename string, body io.Reader) (*models.Image, error) {
	settings, err := s.GetAnonymousSettings()
	if err != nil {
		return nil, err
	}
	if !settings.Enabled {
		return nil, ErrAnonymousDisabled
	}
	src, err := s.currentTarget()
	if err != nil {
		return nil, ErrAnonymousDisabled
	}

	now := time.Now().UTC()
	return s.upload(src, anonymousImageRelDir(now), originalFilename, models.ImageOwnerAnonymous, nil, body)
}

// RetentionSettings 返回图床保留策略（天；0 = 不自动过期）。
func (s *Service) RetentionSettings() (userDays, anonymousDays int, err error) {
	rows, err := s.db.Query(`SELECT key, value FROM system_settings WHERE key IN (?, ?)`,
		SettingRetentionDays, SettingAnonymousRetentionDays)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return 0, 0, err
		}
		parsed, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil || parsed < 0 {
			continue
		}
		switch key {
		case SettingRetentionDays:
			userDays = parsed
		case SettingAnonymousRetentionDays:
			anonymousDays = parsed
		}
	}
	return userDays, anonymousDays, rows.Err()
}

// SetRetentionSettings 更新保留策略；负数视为非法。
func (s *Service) SetRetentionSettings(userDays, anonymousDays int) error {
	if userDays < 0 || anonymousDays < 0 {
		return ErrRetentionInvalid
	}
	now := time.Now().UTC()
	set := func(key string, days int) error {
		_, err := s.db.Exec(`INSERT INTO system_settings (key, value, updated_at) VALUES (?, ?, ?)
  ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			key, strconv.Itoa(days), now)
		return err
	}
	if err := set(SettingRetentionDays, userDays); err != nil {
		return err
	}
	return set(SettingAnonymousRetentionDays, anonymousDays)
}

// expiresAtFor 按归属类型计算绝对失效时间；0 天表示不过期（返回 nil）。
func (s *Service) expiresAtFor(ownerType string, uploadedAt time.Time) *time.Time {
	userDays, anonymousDays, err := s.RetentionSettings()
	if err != nil {
		return nil
	}
	days := userDays
	if ownerType == models.ImageOwnerAnonymous {
		days = anonymousDays
	}
	if days <= 0 {
		return nil
	}
	expires := uploadedAt.AddDate(0, 0, days)
	return &expires
}

// upload 是公共上传流程（README §17.7）：
// 临时文件 -> 真实格式校验 -> 以服务端识别结果决定扩展名 -> 原子重命名 -> 写 Images 表。
func (s *Service) upload(src *models.StorageSource, relDir, originalFilename, ownerType string, ownerUserID *int64, body io.Reader) (*models.Image, error) {
	keys := []lifecycle.Key{lifecycle.Source(src.ID)}
	if ownerUserID != nil {
		keys = append(keys, lifecycle.User(*ownerUserID))
	}
	releaseLifecycle := s.lifecycleManager().Read(keys...)
	defer releaseLifecycle()
	current, err := s.sources.GetByID(src.ID)
	if err != nil || current.Key != src.Key {
		return nil, ErrTargetInvalid
	}
	if ownerUserID != nil {
		var found int
		if err := s.db.QueryRow(`SELECT 1 FROM users WHERE id = ?`, *ownerUserID).Scan(&found); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrTargetInvalid
			}
			return nil, err
		}
	}
	relDir, err = security.NormalizeRelPath(relDir)
	if err != nil {
		return nil, err
	}
	// 写入托管命名空间前核验托管根所有权；未知同名目录会阻断上传。
	if err := s.managedRoots.Ensure(src); err != nil {
		return nil, err
	}
	quotaOwnerUserID := ownerUserID
	if ownerType != models.ImageOwnerUser {
		quotaOwnerUserID = nil
	}
	// Phase 1（无锁）：乐观上限流式写临时文件并校验真实图片格式，避免慢上传持有配额写锁。
	optimisticMaxBytes, optimisticLimited, err := s.files.OptimisticQuotaBound(src, "", quotaOwnerUserID)
	if err != nil {
		return nil, err
	}

	// 托管子树整体被系统强制排除，内部布局由图床服务定义，无需排除规则匹配。
	absDir, err := security.ResolveInSource(src.RootPath, relDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建图床目录失败: %w", err)
	}

	// 写临时文件（README §14.3 同目录临时文件）。
	tmpPath := filepath.Join(absDir, ".omnistore-upload-"+auth.NewRandomToken("", 8)+".tmp")
	tempRelPath := relDir + "/" + filepath.Base(tmpPath)
	plan := s.newImageUploadPlan(src.ID, tempRelPath, ownerType, ownerUserID, originalFilename)
	if err := s.writeImageUploadPlan(plan); err != nil {
		return nil, fmt.Errorf("记录图床上传计划失败: %w", err)
	}
	keepJournal := true
	defer func() {
		if keepJournal {
			_ = s.removeImageUploadOperation(plan.OperationID)
		}
	}()
	tmp, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("创建临时文件失败: %w", err)
	}
	reader := body
	const maxInt64 = int64(^uint64(0) >> 1)
	if optimisticLimited && optimisticMaxBytes < maxInt64 {
		reader = io.LimitReader(body, optimisticMaxBytes+1)
	}
	size, err := io.Copy(tmp, reader)
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("写入失败: %w", err)
	}
	if optimisticLimited && size > optimisticMaxBytes {
		os.Remove(tmpPath)
		return nil, files.ErrQuotaExceeded
	}
	if err := syncImageDirectory(absDir); err != nil {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("同步图床临时目录失败: %w", err)
	}

	// 真实格式校验。
	info, err := ValidateImageFile(tmpPath)
	if err != nil {
		os.Remove(tmpPath)
		return nil, err
	}

	// Phase 2（短临界区）：获取配额锁复核精确额度后原子发布与落库。
	quotaGuard, err := s.files.BeginQuotaWriteForUser(src, "", quotaOwnerUserID)
	if err != nil {
		os.Remove(tmpPath)
		return nil, err
	}
	defer quotaGuard.Close()
	if exactMax, exactLimited := quotaGuard.MaxBytes(); exactLimited && size > exactMax {
		os.Remove(tmpPath)
		return nil, files.ErrQuotaExceeded
	}

	// image_id 与文件名使用不可预测随机数（README §17.9，128-bit）。
	// 最终文件完整落盘后，images 与 file_records 在同一个 SQLite 事务中提交。
	for range 5 {
		random := auth.NewRandomToken("", 16)
		imageID := "img_" + random
		filename := random + "." + info.Ext
		relPath := relDir + "/" + filename
		publicURL := fmt.Sprintf("%s/i/%s.%s", s.publicURL, imageID, info.Ext)

		unlock := s.locks.Lock(locks.Key(src.Key, relPath))
		absPath := filepath.Join(absDir, filename)
		if _, statErr := os.Lstat(absPath); statErr == nil {
			unlock()
			continue // 文件名撞车，重新生成
		} else if !os.IsNotExist(statErr) {
			unlock()
			_ = os.Remove(tmpPath)
			return nil, fmt.Errorf("检查图床最终路径失败: %w", statErr)
		}
		op := plan
		op.FinalRelativePath, op.ImageID, op.PublicURL, op.Size = relPath, imageID, publicURL, size
		op.MimeType, op.Width, op.Height, op.Ext = info.MimeType, info.Width, info.Height, info.Ext
		if err := s.writePreparedImageUploadOperation(op); err != nil {
			unlock()
			_ = os.Remove(tmpPath)
			return nil, fmt.Errorf("记录图床上传准备状态失败: %w", err)
		}
		keepJournal = false
		if err := os.Rename(tmpPath, absPath); err != nil {
			unlock()
			cleanupErr := s.removeImageUploadOperation(op.OperationID)
			_ = os.Remove(tmpPath)
			return nil, errors.Join(fmt.Errorf("落盘失败: %w", err), cleanupErr)
		}
		if err := syncImageDirectory(absDir); err != nil {
			rollbackErr := s.rollbackUncommittedImageUpload(op, absPath)
			unlock()
			return nil, errors.Join(fmt.Errorf("同步图床目录失败: %w", err), rollbackErr)
		}
		fileOwnerType := models.FileOwnerAnonymous
		if ownerType == models.ImageOwnerUser {
			fileOwnerType = models.FileOwnerUser
		}
		prepared, err := s.files.PrepareManagedFileRecord(src, relPath, managedScopeDir, fileOwnerType, ownerUserID, ownerUserID)
		if err != nil {
			rollbackErr := s.rollbackUncommittedImageUpload(op, absPath)
			unlock()
			return nil, errors.Join(fmt.Errorf("准备文件台账失败: %w", err), rollbackErr)
		}
		op.ExpiresAt = s.expiresAtFor(ownerType, op.CreatedAt)
		rowID, err := s.commitImageUpload(op, prepared)
		if err != nil {
			rollbackErr := s.rollbackUncommittedImageUpload(op, absPath)
			unlock()
			return nil, errors.Join(fmt.Errorf("提交图片与文件台账失败: %w", err), rollbackErr)
		}
		// 数据库已经提交即视为成功；日志清理失败时保留给启动恢复，不把已成功上传
		// 误报为失败，避免客户端重试制造重复图片。
		_ = s.removeImageUploadOperation(op.OperationID)
		unlock()
		return s.decorateImage(&models.Image{
			ID: rowID, ImageID: op.ImageID, OwnerType: op.OwnerType, OwnerUserID: op.OwnerUserID,
			StorageSourceID: op.StorageSourceID, RelativePath: op.FinalRelativePath,
			OriginalFilename: op.OriginalFilename, PublicURL: op.PublicURL, Size: op.Size,
			MimeType: op.MimeType, Width: op.Width, Height: op.Height, Ext: op.Ext,
			ExpiresAt: op.ExpiresAt, CreatedAt: op.CreatedAt,
		}, nil)
	}
	os.Remove(tmpPath)
	return nil, fmt.Errorf("生成 image_id 失败")
}

// --- 查询 / 访问 ---

const imageColumns = `id, image_id, owner_type, owner_user_id, storage_source_id, relative_path,
  COALESCE(original_filename, ''), public_url, size, mime_type, width, height, ext, expires_at, created_at`

func scanImage(row interface{ Scan(...any) error }) (*models.Image, error) {
	var m models.Image
	var expiresAt sql.NullTime
	err := row.Scan(&m.ID, &m.ImageID, &m.OwnerType, &m.OwnerUserID, &m.StorageSourceID, &m.RelativePath,
		&m.OriginalFilename, &m.PublicURL, &m.Size, &m.MimeType, &m.Width, &m.Height, &m.Ext,
		&expiresAt, &m.CreatedAt)
	if expiresAt.Valid {
		m.ExpiresAt = &expiresAt.Time
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *Service) getByRowID(id int64) (*models.Image, error) {
	img, err := scanImage(s.db.QueryRow(`SELECT `+imageColumns+` FROM images WHERE id = ?`, id))
	return s.decorateImage(img, err)
}

// Get 按 image_id 查询图片记录。
func (s *Service) Get(imageID string) (*models.Image, error) {
	img, err := scanImage(s.db.QueryRow(`SELECT `+imageColumns+` FROM images WHERE image_id = ? AND trash_key IS NULL`, imageID))
	return s.decorateImage(img, err)
}

func (s *Service) decorateImage(img *models.Image, err error) (*models.Image, error) {
	if err != nil {
		return nil, err
	}
	img.ThumbnailURL = fmt.Sprintf("%s/t/%s.jpg", s.publicURL, img.ImageID)
	return img, nil
}

// OpenImage 打开图片文件用于公开访问（README §17.8）。
// ext 必须与记录一致，存储源禁用时返回 ErrNotFound（README §17.13）。
func (s *Service) OpenImage(imageID, ext string) (*models.Image, *os.File, os.FileInfo, func(), error) {
	img, err := s.Get(imageID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if img.Ext != ext {
		return nil, nil, nil, nil, ErrNotFound
	}
	if img.ExpiresAt != nil && !time.Now().Before(*img.ExpiresAt) {
		// 保留期已过的图片不再公开提供。
		return nil, nil, nil, nil, ErrNotFound
	}
	src, err := s.sources.GetByID(img.StorageSourceID)
	if err != nil || src.IsDisabled {
		return nil, nil, nil, nil, ErrNotFound
	}

	f, info, unlock, err := s.openImageOriginal(src, img.RelativePath)
	if err != nil {
		return nil, nil, nil, nil, ErrNotFound
	}
	return img, f, info, unlock, nil
}

// ListForOwner 返回历史墙（按用户隔离，README §17.11）。
// ownerUserID 为 nil 时返回匿名图片（管理员入口）。
func (s *Service) ListForOwner(ownerUserID *int64, page, pageSize int) ([]*models.Image, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}

	where := `owner_type = 'anonymous'`
	args := []any{}
	if ownerUserID != nil {
		where = `owner_type = 'user' AND owner_user_id = ?`
		args = append(args, *ownerUserID)
	}
	where = `trash_key IS NULL AND (` + where + `)`

	var total int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM images WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `SELECT ` + imageColumns + ` FROM images WHERE ` + where + ` ORDER BY id DESC LIMIT ? OFFSET ?`
	rows, err := s.db.Query(query, append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []*models.Image{}
	for rows.Next() {
		img, err := scanImage(rows)
		if err != nil {
			return nil, 0, err
		}
		img, err = s.decorateImage(img, nil)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, img)
	}
	return out, total, rows.Err()
}

// DeleteByUser 用户删除自己的图片（README §17.12）。
func (s *Service) DeleteByUser(user *models.User, imageID string) error {
	img, err := s.Get(imageID)
	if err != nil {
		return err
	}
	if img.OwnerType != models.ImageOwnerUser || img.OwnerUserID == nil || *img.OwnerUserID != user.ID {
		return ErrNotFound // 不暴露他人图片存在性
	}
	// 图床服务授权只作用于托管范围：所有权校验即可删除，不要求源路径 Policy。
	return s.deletePhysicalAndRecord(img)
}

// DeleteByAdmin 管理员删除匿名图片（README §17.11）。
func (s *Service) DeleteByAdmin(imageID string) error {
	img, err := s.Get(imageID)
	if err != nil {
		return err
	}
	return s.deletePhysicalAndRecord(img)
}

func (s *Service) deletePhysicalAndRecord(img *models.Image) error {
	src, err := s.sources.GetByID(img.StorageSourceID)
	if err == nil && !src.IsDisabled {
		// 按布局选择删除原语；物理文件不存在时按已删除处理。
		var delErr error
		if imageIsManaged(img.RelativePath) {
			delErr = s.files.DeleteManaged(src, img.RelativePath, managedScopeDir)
		} else {
			delErr = s.files.Delete(src, img.RelativePath)
		}
		if delErr != nil && !errors.Is(delErr, files.ErrNotFound) && !errors.Is(delErr, files.ErrPathExcluded) {
			return delErr
		}
	}
	_, err = s.db.Exec(`DELETE FROM images WHERE id = ?`, img.ID)
	if err == nil {
		s.removeThumbnailFiles(img.ImageID)
	}
	return err
}
