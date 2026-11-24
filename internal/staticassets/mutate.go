package staticassets

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/omni-store/omnistore/internal/audit"
	"github.com/omni-store/omnistore/internal/capabilities"
	"github.com/omni-store/omnistore/internal/models"
	"github.com/omni-store/omnistore/internal/security"
)

// ConfigureInput 是首次配置发布空间的输入。
type ConfigureInput struct {
	SourceKey          string
	PublishRoot        string
	Enabled            *bool
	CacheMode          string
	CorsMode           string
	PublicOrigin       string
	AllowedOrigins     []string
	ConfirmPublishRoot bool
}

// UpdateInput 修改指定源的协议策略，不包含发布位置修改。
type UpdateInput struct {
	SourceKey        string
	Enabled          *bool
	CacheMode        *string
	CorsMode         *string
	PublicOrigin     *string
	AllowedOrigins   *[]string
	ExpectedRevision *int64
}

// RebindInput 是专用重绑输入：reset 生成新公开标识；relocate 核验后保留。
type RebindInput struct {
	SourceKey          string
	PublishRoot        string
	Mode               string // reset | relocate
	ConfirmPublishRoot bool
	ConfirmRelocate    bool
	ExpectedRevision   *int64
}

// PreflightInput 是发布目录预检输入。
type PreflightInput struct {
	SourceKey   string
	PublishRoot string
}

// PreflightResult 是发布目录预检结果；预检不是长期授权。
type PreflightResult struct {
	SourceKey       string   `json:"source_key"`
	PublishRoot     string   `json:"publish_root"`
	RootExists      bool     `json:"root_exists"`
	SampleEntries   []string `json:"sample_entries"`
	SampleTruncated bool     `json:"sample_truncated"`
	SupportedTypes  []string `json:"supported_types"`
	Warnings        []string `json:"warnings"`
}

// Configure 为一个源创建独立发布配置。
func (s *Service) Configure(in ConfigureInput, actorUserID *int64, auditLogger *audit.Logger) (*Config, error) {
	configMu.Lock()
	defer configMu.Unlock()

	snap, err := s.loadSnapshot("WHERE src.key = ?", in.SourceKey)
	if err != nil {
		return nil, err
	}
	if snap.publicAssetID != "" {
		return nil, ErrAlreadyConfigured
	}
	src, err := s.sources.Get(in.SourceKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPublishRootInvalid, err)
	}
	root, err := normalizePublishRoot(in.PublishRoot)
	if err != nil {
		return nil, err
	}
	if root == "" && !in.ConfirmPublishRoot {
		return nil, ErrRootConfirmRequired
	}
	if err := verifyPublishRootDir(src, root); err != nil {
		return nil, err
	}
	if in.CacheMode == "" {
		in.CacheMode = CacheShort
	}
	if in.CorsMode == "" {
		in.CorsMode = CorsPublic
	}
	if err := validateModes(in.CacheMode, in.CorsMode); err != nil {
		return nil, err
	}
	origin, err := NormalizePublicOrigin(in.PublicOrigin)
	if err != nil {
		return nil, err
	}
	origins, err := normalizeOriginList(in.AllowedOrigins)
	if err != nil {
		return nil, err
	}
	assetID := GenerateAssetID()

	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if _, err := tx.Exec(`INSERT INTO source_static_access
 (storage_source_id, enabled, publish_root, public_asset_id, public_origin, cache_mode, cors_mode, revision, updated_at)
 VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?)`, src.ID, enabled, root, assetID, origin, in.CacheMode, in.CorsMode, now); err != nil {
		return nil, err
	}
	if err := replaceOrigins(tx, src.ID, origins); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	logConfigAudit(auditLogger, actorUserID, "static_assets_configure", src.Key, root, "")
	return s.Config(in.SourceKey)
}

// Update 修改协议策略（enabled/cache/cors/origin），发布位置由 Rebind 管理。
func (s *Service) Update(in UpdateInput) (*Config, error) {
	configMu.Lock()
	defer configMu.Unlock()

	snap, err := s.loadSnapshot("WHERE src.key = ?", in.SourceKey)
	if err != nil {
		return nil, err
	}
	if snap.publicAssetID == "" {
		return nil, ErrNotConfigured
	}
	if in.ExpectedRevision != nil && *in.ExpectedRevision != snap.revision {
		return nil, ErrRevisionConflict
	}
	cacheMode := snap.cacheMode
	if in.CacheMode != nil {
		cacheMode = *in.CacheMode
	}
	corsMode := snap.corsMode
	if in.CorsMode != nil {
		corsMode = *in.CorsMode
	}
	if err := validateModes(cacheMode, corsMode); err != nil {
		return nil, err
	}
	origin := snap.publicOrigin
	if in.PublicOrigin != nil {
		if origin, err = NormalizePublicOrigin(*in.PublicOrigin); err != nil {
			return nil, err
		}
	}
	enabled := snap.enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if _, err := tx.Exec(`UPDATE source_static_access
 SET enabled = ?, public_origin = ?, cache_mode = ?, cors_mode = ?, revision = revision + 1, updated_at = ?
 WHERE storage_source_id = ?`, enabled, origin, cacheMode, corsMode, now, snap.sourceID); err != nil {
		return nil, err
	}
	if in.AllowedOrigins != nil {
		origins, normErr := normalizeOriginList(*in.AllowedOrigins)
		if normErr != nil {
			return nil, normErr
		}
		if err := replaceOrigins(tx, snap.sourceID, origins); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Config(in.SourceKey)
}

// Rebind 专用重绑：reset 生成新公开标识；relocate 显式确认后保留标识。
// 普通配置 PUT 不允许静默更换 source/publish_root。
func (s *Service) Rebind(sourceKey string, in RebindInput, actorUserID *int64, auditLogger *audit.Logger) (*Config, error) {
	configMu.Lock()
	defer configMu.Unlock()

	snap, err := s.loadSnapshot("WHERE src.key = ?", sourceKey)
	if err != nil {
		return nil, err
	}
	if snap.publicAssetID == "" {
		return nil, ErrNotConfigured
	}
	if in.ExpectedRevision != nil && *in.ExpectedRevision != snap.revision {
		return nil, ErrRevisionConflict
	}
	if in.Mode != "reset" && in.Mode != "relocate" {
		return nil, ErrRebindModeInvalid
	}
	if in.Mode == "relocate" && !in.ConfirmRelocate {
		return nil, ErrRelocateUnconfirmed
	}
	src, err := s.sources.Get(in.SourceKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPublishRootInvalid, err)
	}
	if src.IsDisabled {
		return nil, fmt.Errorf("%w: 存储源已禁用", ErrPublishRootInvalid)
	}
	root, err := normalizePublishRoot(in.PublishRoot)
	if err != nil {
		return nil, err
	}
	if root == "" && !in.ConfirmPublishRoot {
		return nil, ErrRootConfirmRequired
	}
	if err := verifyPublishRootDir(src, root); err != nil {
		return nil, err
	}
	assetID := snap.publicAssetID
	if in.Mode == "reset" {
		// 旧 ID 不重新指向另一批内容：reset 生成全新标识。
		assetID = GenerateAssetID()
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	// 已配置的目标源不得被覆盖，事务中的主键约束也保护并发迁移。
	if src.ID != snap.sourceID {
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM source_static_access WHERE storage_source_id = ?`, src.ID).Scan(&count); err != nil {
			return nil, err
		}
		if count != 0 {
			return nil, ErrTargetConfigured
		}
	}
	if _, err := tx.Exec(`UPDATE source_static_access
 SET storage_source_id = ?, publish_root = ?, public_asset_id = ?, revision = revision + 1, updated_at = ?
 WHERE storage_source_id = ?`, src.ID, root, assetID, now, snap.sourceID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	logConfigAudit(auditLogger, actorUserID, "static_assets_rebind_"+in.Mode, src.Key, root, "")
	return s.Config(in.SourceKey)
}

// Preflight 校验发布空间候选：源/目录存在可读，返回样例与告警；不创建、不写入。
func (s *Service) Preflight(in PreflightInput) (*PreflightResult, error) {
	src, err := s.sources.Get(in.SourceKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPublishRootInvalid, err)
	}
	if src.IsDisabled {
		return nil, fmt.Errorf("%w: 存储源已禁用", ErrPublishRootInvalid)
	}
	root, err := normalizePublishRoot(in.PublishRoot)
	if err != nil {
		return nil, err
	}
	result := &PreflightResult{
		SourceKey:      src.Key,
		PublishRoot:    root,
		SampleEntries:  []string{},
		SupportedTypes: AllowedTypeExtensions(),
		Warnings:       []string{},
	}
	absRoot, err := security.ResolveInSource(src.RootPath, root)
	if err != nil {
		if errors.Is(err, security.ErrSymlink) {
			return nil, fmt.Errorf("%w: 发布目录不能是符号链接", ErrPublishRootInvalid)
		}
		return nil, fmt.Errorf("%w: %v", ErrPublishRootInvalid, err)
	}
	info, err := os.Lstat(absRoot)
	if err != nil {
		if os.IsNotExist(err) {
			result.Warnings = append(result.Warnings, "发布目录当前不存在；请先创建目录，再配置静态访问。")
			return result, nil
		}
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: 发布路径不是普通目录", ErrPublishRootInvalid)
	}
	result.RootExists = true
	entries, err := os.ReadDir(absRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: 发布目录不可读: %v", ErrPublishRootInvalid, err)
	}
	for _, entry := range entries {
		if len(result.SampleEntries) >= 10 {
			result.SampleTruncated = true
			break
		}
		result.SampleEntries = append(result.SampleEntries, entry.Name())
	}
	// 与公开盘共用同一源时提示重叠风险（能力独立但内容可能重叠）。
	if publicSrc, pubErr := s.publicDriveSource(); pubErr == nil && publicSrc != nil && publicSrc.ID == src.ID {
		result.Warnings = append(result.Warnings,
			"该存储源同时绑定为公开盘：发布目录内的文件也可能经公开盘被浏览（公开盘仍受自身开关控制）。")
	}
	if managedInfo, mErr := os.Lstat(filepath.Join(src.RootPath, security.ManagedNamespaceSegment)); mErr == nil && managedInfo.IsDir() {
		regErr := s.hasManagedRegistration(src.ID)
		result.Warnings = append(result.Warnings,
			"源根存在 .omnistore 托管目录；它永远不会经 /assets/ 公开，也不受发布配置影响。")
		_ = regErr
	}
	if root == "" {
		result.Warnings = append(result.Warnings, "发布根为存储源根目录：其中受支持的媒体文件将公开读取；托管命名空间与排除项仍被隔离。")
	}
	return result, nil
}

func (s *Service) publicDriveSource() (*models.StorageSource, error) {
	var sourceID sql.NullInt64
	err := s.db.QueryRow(`SELECT storage_source_id FROM site_capability_bindings WHERE capability = ?`,
		capabilities.CapabilityPublicDrive).Scan(&sourceID)
	if errors.Is(err, sql.ErrNoRows) || !sourceID.Valid {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.sources.GetByID(sourceID.Int64)
}

func (s *Service) hasManagedRegistration(sourceID int64) error {
	var one int
	return s.db.QueryRow(`SELECT 1 FROM managed_root_registrations WHERE storage_source_id = ?`, sourceID).Scan(&one)
}

// verifyPublishRootDir 确认发布根存在且是普通目录（配置/重绑最终提交时复检）。
func verifyPublishRootDir(src *models.StorageSource, root string) error {
	absRoot, err := security.ResolveInSource(src.RootPath, root)
	if err != nil {
		if errors.Is(err, security.ErrSymlink) {
			return fmt.Errorf("%w: 发布目录不能是符号链接", ErrPublishRootInvalid)
		}
		return fmt.Errorf("%w: %v", ErrPublishRootInvalid, err)
	}
	info, err := os.Lstat(absRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: 发布路径不是普通目录", ErrPublishRootInvalid)
	}
	return nil
}

func replaceOrigins(tx *sql.Tx, sourceID int64, origins []string) error {
	if _, err := tx.Exec(`DELETE FROM source_static_origins WHERE storage_source_id = ?`, sourceID); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, origin := range origins {
		if _, err := tx.Exec(`INSERT INTO source_static_origins (storage_source_id, origin, created_at) VALUES (?, ?, ?)`, sourceID, origin, now); err != nil {
			return err
		}
	}
	return nil
}

func logConfigAudit(auditLogger *audit.Logger, actorUserID *int64, action, sourceKey, publishRoot, errorCode string) {
	if auditLogger == nil {
		return
	}
	auditLogger.Log(audit.Entry{
		ActorType: audit.ActorUser, ActorUserID: actorUserID,
		EntryType: audit.EntryAdmin, Action: action,
		RelativePath: publishRoot, Status: audit.StatusSuccess, ErrorCode: errorCode,
	})
}

// Remove 撤销该源的公开身份；只移除配置，不删除文件。必须先关闭协议。
func (s *Service) Remove(sourceKey string, expectedRevision int64) error {
	configMu.Lock()
	defer configMu.Unlock()
	snap, err := s.loadSnapshot("WHERE src.key = ?", sourceKey)
	if err != nil {
		return err
	}
	if snap.publicAssetID == "" {
		return ErrNotConfigured
	}
	if snap.revision != expectedRevision {
		return ErrRevisionConflict
	}
	if snap.enabled {
		return ErrDisableRequired
	}
	_, err = s.db.Exec(`DELETE FROM source_static_access WHERE storage_source_id = ?`, snap.sourceID)
	return err
}
