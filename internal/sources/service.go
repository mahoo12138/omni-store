package sources

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/omni-store/omnistore/internal/auth"
	"github.com/omni-store/omnistore/internal/db"
	"github.com/omni-store/omnistore/internal/models"
	"github.com/omni-store/omnistore/internal/security"
)

var (
	// ErrNotFound 存储源不存在。
	ErrNotFound = errors.New("存储源不存在")
	// ErrNameRequired 存储源名称不能为空。
	ErrNameRequired = errors.New("存储源名称不能为空")
	// ErrQuotaInvalid 存储源配额不能为负数。
	ErrQuotaInvalid = errors.New("存储源配额不能为负数")
	// ErrExistingConfirmationRequired 表示非空目录必须由调用方显式确认导入。
	ErrExistingConfirmationRequired = errors.New("目录已有内容，必须显式确认导入")
)

// rootTopologyMu serializes root-path topology reads and writes across all
// Service instances in this process. The overlap invariant cannot be expressed
// by a simple SQLite UNIQUE constraint because parent/child paths also conflict.
var rootTopologyMu sync.RWMutex

// DefaultExcludePatterns 是新建存储源默认建议排除规则（README §11.3）。
var DefaultExcludePatterns = []string{
	"**/.DS_Store",
	"**/Thumbs.db",
	"**/@eaDir/**",
	"**/#recycle/**",
	"**/.Trash/**",
	"**/.Trashes/**",
	"**/.git/**",
	"**/.env",
	"**/.env.*",
	"**/.ssh/**",
}

// Service 提供存储源管理能力。
type Service struct {
	db      *sql.DB
	rdb     *sql.DB
	dataDir string
	rootsMu sync.RWMutex
	roots   map[int64]*cachedSourceRoot
}

// NewService 创建存储源服务。dataDir 用于路径安全校验。
func NewService(db *sql.DB, dataDir string) *Service {
	return &Service{db: db, dataDir: dataDir}
}

// SetReadDB 注入只读连接池，供高频存储源元数据与排除规则查询使用。
func (s *Service) SetReadDB(rdb *sql.DB) {
	s.rdb = rdb
}

func (s *Service) readDB() *sql.DB {
	if s.rdb != nil {
		return s.rdb
	}
	return s.db
}

// DataDir 返回系统数据目录，供文件服务存放回收站等内部数据；不得作为用户存储源。
func (s *Service) DataDir() string {
	return s.dataDir
}

const sourceColumns = `id, key, name, description, root_path, is_disabled,
  webdav_enabled, s3_enabled, quota_bytes, created_at, updated_at,
 COALESCE((SELECT enabled FROM source_static_access WHERE storage_source_id = storage_sources.id), 0)`

func scanSource(row interface{ Scan(...any) error }) (*models.StorageSource, error) {
	var s models.StorageSource
	var desc sql.NullString
	err := row.Scan(&s.ID, &s.Key, &s.Name, &desc, &s.RootPath, &s.IsDisabled,
		&s.WebdavEnabled, &s.S3Enabled, &s.QuotaBytes, &s.CreatedAt, &s.UpdatedAt, &s.StaticEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.Description = desc.String
	return &s, nil
}

// CreateInput 是创建存储源的输入。
type CreateInput struct {
	Name           string
	Description    string
	RootPath       string
	ImportExisting bool
	// ExcludePatterns 为 nil 时使用默认建议规则。
	ExcludePatterns []string
	HasPatterns     bool
}

// Create 创建存储源，执行全部路径安全校验（README §10.5）。
func (s *Service) Create(in CreateInput) (*models.StorageSource, error) {
	return s.CreateWithInitializer(in, nil)
}

// CreateInitializer 在新来源及排除规则所在事务内写入依赖来源 ID 的初始状态。
// 回调失败会回滚整个来源创建；回调不得执行耗时文件系统扫描。
type CreateInitializer func(*sql.Tx, *models.StorageSource, []string) error

// CreateWithInitializer 原子创建来源及调用方准备好的初始状态。
func (s *Service) CreateWithInitializer(in CreateInput, initialize CreateInitializer) (*models.StorageSource, error) {
	if in.Name = strings.TrimSpace(in.Name); in.Name == "" {
		return nil, ErrNameRequired
	}
	rootTopologyMu.Lock()
	defer rootTopologyMu.Unlock()

	existing, err := s.allRootPaths()
	if err != nil {
		return nil, err
	}
	realPath, err := ValidateRootPath(in.RootPath, s.dataDir, existing)
	if err != nil {
		return nil, err
	}

	patterns := in.ExcludePatterns
	if !in.HasPatterns {
		patterns = DefaultExcludePatterns
	}
	preview, err := previewDirectory(realPath, patterns)
	if err != nil {
		return nil, err
	}
	if !preview.IsEmpty && !in.ImportExisting {
		return nil, ErrExistingConfirmationRequired
	}

	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var (
		key    string
		result sql.Result
	)
	for attempt := 0; attempt < 5; attempt++ {
		key = auth.NewRandomToken("src-", 8)
		result, err = tx.Exec(`INSERT INTO storage_sources
	  (key, name, description, root_path, is_disabled, webdav_enabled, s3_enabled, created_at, updated_at)
	  VALUES (?, ?, ?, ?, 0, 1, 0, ?, ?)`,
			key, in.Name, in.Description, realPath, now, now)
		if err == nil || !db.IsUniqueConstraint(err, "storage_sources.key") {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("创建存储源失败: %w", err)
	}
	storageSourceID, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}

	for _, p := range patterns {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO storage_source_exclude_patterns (storage_source_id, pattern, created_at)
  VALUES (?, ?, ?)`, storageSourceID, p, now); err != nil {
			return nil, err
		}
	}
	pending := &models.StorageSource{
		ID: storageSourceID, Key: key, Name: in.Name, Description: in.Description,
		RootPath: realPath, WebdavEnabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if initialize != nil {
		if err := initialize(tx, pending, append([]string(nil), patterns...)); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(key)
}

func (s *Service) allRootPaths() ([]string, error) {
	rows, err := s.db.Query(`SELECT root_path FROM storage_sources`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Get 按系统生成的不透明 key 查询存储源。
func (s *Service) Get(key string) (*models.StorageSource, error) {
	return scanSource(s.readDB().QueryRow(`SELECT `+sourceColumns+` FROM storage_sources WHERE key = ?`, key))
}

// GetByID 仅供持久化关联解析使用；数字主键不暴露到用户路由。
func (s *Service) GetByID(id int64) (*models.StorageSource, error) {
	return scanSource(s.readDB().QueryRow(`SELECT `+sourceColumns+` FROM storage_sources WHERE id = ?`, id))
}

// List 返回全部存储源（管理员）。
func (s *Service) List() ([]*models.StorageSource, error) {
	rows, err := s.readDB().Query(`SELECT ` + sourceColumns + ` FROM storage_sources ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.StorageSource
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// UpdateInput 是可修改的存储源配置。root_path 创建后不可修改（README §10.3）。
// 产品功能开关不属于存储源，改用 Site Capability 绑定。
type UpdateInput struct {
	Name            *string
	Description     *string
	WebdavEnabled   *bool
	S3Enabled       *bool
	QuotaBytes      *int64
	ExcludePatterns *[]string
}

// Update 修改存储源配置与协议开关。
func (s *Service) Update(key string, in UpdateInput) (*models.StorageSource, error) {
	src, err := s.Get(key)
	if err != nil {
		return nil, err
	}
	if in.Name != nil {
		if v := strings.TrimSpace(*in.Name); v != "" {
			src.Name = v
		}
	}
	if in.Description != nil {
		src.Description = *in.Description
	}
	if in.WebdavEnabled != nil {
		src.WebdavEnabled = *in.WebdavEnabled
	}
	if in.S3Enabled != nil {
		src.S3Enabled = *in.S3Enabled
	}
	if in.QuotaBytes != nil {
		if *in.QuotaBytes < 0 {
			return nil, ErrQuotaInvalid
		}
		src.QuotaBytes = *in.QuotaBytes
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`UPDATE storage_sources SET
  name = ?, description = ?, webdav_enabled = ?, s3_enabled = ?, quota_bytes = ?, updated_at = ?
  WHERE id = ?`,
		src.Name, src.Description, src.WebdavEnabled, src.S3Enabled, src.QuotaBytes, time.Now().UTC(), src.ID)
	if err != nil {
		return nil, fmt.Errorf("更新存储源失败: %w", err)
	}
	if in.ExcludePatterns != nil {
		if err := replaceExcludePatterns(tx, src.ID, *in.ExcludePatterns); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(key)
}

type queryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

type statementExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// SetDisabled 启用/禁用存储源。禁用后所有入口不可访问（README §10.1）。
func (s *Service) SetDisabled(key string, disabled bool) error {
	res, err := s.db.Exec(`UPDATE storage_sources SET is_disabled = ?, updated_at = ? WHERE key = ?`,
		disabled, time.Now().UTC(), key)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete 删除存储源的 OmniStore 内部记录，不删除真实磁盘文件（README §10.4）。
// 仍被 Site Capability 绑定的源在数据库层由 RESTRICT 外键拒绝；
// HTTP 层必须先调用 capabilities.AssertSourceDeletable 给出可读错误。
func (s *Service) Delete(key string) error {
	rootTopologyMu.Lock()
	defer rootTopologyMu.Unlock()

	src, err := s.Get(key)
	if err != nil {
		return err
	}
	id := src.ID
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, q := range []string{
		`DELETE FROM access_policy_sources WHERE storage_source_id = ?`,
		`DELETE FROM storage_source_exclude_patterns WHERE storage_source_id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}

	res, err := tx.Exec(`DELETE FROM storage_sources WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.InvalidateRoot(id)
	return nil
}

// UpdateRootPath 重新绑定存储源根路径（备份恢复/磁盘迁移后的管理动作）。
// 与创建同规则：路径必须存在、不进系统数据目录、不与其它源重叠。
// 不移动、不校验文件内容；绑定后由管理员执行台账校准。
func (s *Service) UpdateRootPath(key, newRoot string) (*models.StorageSource, error) {
	rootTopologyMu.Lock()
	defer rootTopologyMu.Unlock()

	src, err := s.Get(key)
	if err != nil {
		return nil, err
	}
	existing, err := s.allRootPaths()
	if err != nil {
		return nil, err
	}
	filtered := existing[:0]
	for _, path := range existing {
		if path != src.RootPath {
			filtered = append(filtered, path)
		}
	}
	realPath, err := ValidateRootPath(newRoot, s.dataDir, filtered)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(`UPDATE storage_sources SET root_path = ?, updated_at = ? WHERE id = ?`,
		realPath, time.Now().UTC(), src.ID); err != nil {
		return nil, fmt.Errorf("更新存储源根路径失败: %w", err)
	}
	_, _ = s.db.Exec(`DELETE FROM source_usage WHERE storage_source_id = ?`, src.ID)
	s.InvalidateRoot(src.ID)
	return s.Get(key)
}

// --- 排除规则（README §11） ---

// ExcludePatterns 返回存储源的自定义排除规则（不含系统强制规则）。
func (s *Service) ExcludePatterns(id int64) ([]string, error) {
	rows, err := s.readDB().Query(`SELECT pattern FROM storage_source_exclude_patterns
  WHERE storage_source_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetExcludePatterns 整体替换存储源排除规则。
func (s *Service) SetExcludePatterns(id int64, patterns []string) error {
	if _, err := s.GetByID(id); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := replaceExcludePatterns(tx, id, patterns); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceExcludePatterns(execer statementExecer, id int64, patterns []string) error {
	if _, err := execer.Exec(`DELETE FROM storage_source_exclude_patterns WHERE storage_source_id = ?`, id); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if _, err := execer.Exec(`INSERT INTO storage_source_exclude_patterns (storage_source_id, pattern, created_at)
  VALUES (?, ?, ?)`, id, pattern, now); err != nil {
			return err
		}
	}
	return nil
}

// Matcher 返回该存储源的排除规则匹配器（含系统强制规则）。
func (s *Service) Matcher(id int64) (*security.ExcludeMatcher, error) {
	patterns, err := s.ExcludePatterns(id)
	if err != nil {
		return nil, err
	}
	return security.NewExcludeMatcher(patterns), nil
}

// IsPathExcluded 统一排除规则检查函数（README §11.4）。
// relativePath 必须是 NormalizeRelPath 的输出。
func (s *Service) IsPathExcluded(id int64, relativePath string) bool {
	m, err := s.Matcher(id)
	if err != nil {
		// 查询失败时按排除处理，宁可拒绝不可泄露。
		return true
	}
	return m.MatchPrefix(relativePath)
}
