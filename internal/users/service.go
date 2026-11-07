// Package users 负责用户管理。
package users

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/omni-store/omnistore/internal/auth"
	"github.com/omni-store/omnistore/internal/models"
)

var (
	// ErrUsernameTaken 用户名已存在。
	ErrUsernameTaken = errors.New("用户名已存在")
	// ErrNotFound 用户不存在。
	ErrNotFound = errors.New("用户不存在")
	// ErrInvalidUsername 用户名格式非法。
	ErrInvalidUsername = errors.New("用户名只允许 3-32 位字母、数字、下划线、短横线")
	// ErrWeakPassword 密码过短。
	ErrWeakPassword = errors.New("密码长度至少 8 位")
	// ErrQuotaInvalid 用户配额不能为负数。
	ErrQuotaInvalid = errors.New("用户配额不能为负数")
	// ErrAlreadyInitialized 首个管理员已由另一请求创建。
	ErrAlreadyInitialized = errors.New("系统已初始化")
	// ErrOIDCIdentityTaken OIDC 身份已被其他账号绑定。
	ErrOIDCIdentityTaken = errors.New("该 SSO 身份已绑定其他账号")
	// ErrNoPassword 账号未设置本地密码（SSO 建号账号）。
	ErrNoPassword = errors.New("账号未设置本地密码")
)

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{3,32}$`)

// Service 提供用户管理能力。
type Service struct {
	db *sql.DB
}

// RevokedCredentials 汇总一次账号凭据撤销实际删除的记录数。
type RevokedCredentials struct {
	Sessions       int64 `json:"sessions"`
	WebDAVTokens   int64 `json:"webdav_tokens"`
	ImageBedTokens int64 `json:"image_bed_tokens"`
	S3Credentials  int64 `json:"s3_credentials"`
}

// NewService 创建用户服务。
func NewService(db *sql.DB) *Service {
	return &Service{db: db}
}

// Count 返回用户总数，用于判断是否进入初始化模式。
func (s *Service) Count() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// Create 创建用户。username 创建后不可修改，密码只保存哈希。
func (s *Service) Create(username, displayName, password, role string) (*models.User, error) {
	username, displayName, hash, err := prepareUser(username, displayName, password, role)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	// user_public_id 冲突概率极低，冲突时重试。
	for range 5 {
		publicID := auth.NewPublicID("u")
		res, err := s.db.Exec(`INSERT INTO users
  (user_public_id, username, display_name, password_hash, role, is_disabled, created_at, updated_at)
  VALUES (?, ?, ?, ?, ?, 0, ?, ?)`,
			publicID, username, displayName, hash, role, now, now)
		if err != nil {
			msg := err.Error()
			if strings.Contains(msg, "users.username") {
				return nil, ErrUsernameTaken
			}
			if strings.Contains(msg, "users.user_public_id") {
				continue
			}
			return nil, fmt.Errorf("创建用户失败: %w", err)
		}
		id, _ := res.LastInsertId()
		return s.GetByID(id)
	}
	return nil, fmt.Errorf("生成 user_public_id 失败")
}

// CreateFirstAdmin atomically creates the only user allowed while the users
// table is empty. INSERT ... SELECT keeps the emptiness check and insert in one
// SQLite statement, so concurrent setup requests cannot both win.
func (s *Service) CreateFirstAdmin(username, displayName, password string) (*models.User, error) {
	username, displayName, hash, err := prepareUser(username, displayName, password, models.RoleSuperAdmin)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	for range 5 {
		publicID := auth.NewPublicID("u")
		res, err := s.db.Exec(`INSERT INTO users
  (user_public_id, username, display_name, password_hash, role, is_disabled, created_at, updated_at)
  SELECT ?, ?, ?, ?, ?, 0, ?, ?
  WHERE NOT EXISTS (SELECT 1 FROM users)`,
			publicID, username, displayName, hash, models.RoleSuperAdmin, now, now)
		if err != nil {
			if strings.Contains(err.Error(), "users.user_public_id") {
				continue
			}
			return nil, fmt.Errorf("创建首个管理员失败: %w", err)
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return nil, err
		}
		if rows == 0 {
			return nil, ErrAlreadyInitialized
		}
		id, _ := res.LastInsertId()
		return s.GetByID(id)
	}
	return nil, fmt.Errorf("生成 user_public_id 失败")
}

func prepareUser(username, displayName, password, role string) (string, string, string, error) {
	username = strings.TrimSpace(username)
	if !usernameRe.MatchString(username) {
		return "", "", "", ErrInvalidUsername
	}
	if len(password) < 8 {
		return "", "", "", ErrWeakPassword
	}
	if role != models.RoleSuperAdmin && role != models.RoleUser {
		return "", "", "", fmt.Errorf("非法角色: %s", role)
	}
	if displayName = strings.TrimSpace(displayName); displayName == "" {
		displayName = username
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return "", "", "", err
	}
	return username, displayName, hash, nil
}

// NormalizeOIDCUsername 将 IdP 的 preferred_username 清洗为本地用户名规则
// （^[a-zA-Z0-9_-]{3,32}$）：非法字符替换为下划线并截断；清洗后仍不合法则报错。
func NormalizeOIDCUsername(preferred string) (string, error) {
	var b strings.Builder
	for _, r := range strings.TrimSpace(preferred) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	username := b.String()
	if len(username) > 32 {
		username = username[:32]
	}
	username = strings.Trim(username, "_-")
	if !usernameRe.MatchString(username) {
		return "", ErrInvalidUsername
	}
	return username, nil
}

// GetByOIDCIdentity 查找绑定了给定 IdP 身份的用户。
func (s *Service) GetByOIDCIdentity(issuer, subject string) (*models.User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE oidc_iss = ? AND oidc_sub = ?`, issuer, subject))
}

// BindOIDC 将 OIDC 身份绑定到指定用户。身份已被其他账号占用时返回
// ErrOIDCIdentityTaken；并发绑定由 idx_users_oidc 唯一索引兜底。
func (s *Service) BindOIDC(userID int64, issuer, subject string) error {
	if subject == "" {
		return fmt.Errorf("OIDC subject 不能为空")
	}
	res, err := s.db.Exec(`UPDATE users SET oidc_iss = ?, oidc_sub = ?, updated_at = ?
	  WHERE id = ? AND NOT EXISTS (
	    SELECT 1 FROM users WHERE oidc_iss = ? AND oidc_sub = ? AND id <> ?)`,
		issuer, subject, time.Now().UTC(), userID, issuer, subject, userID)
	if err != nil {
		// SQLite 唯一约束报错携带列名而非索引名。
		if strings.Contains(err.Error(), "users.oidc_iss") {
			return ErrOIDCIdentityTaken
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.GetByID(userID); err != nil {
			return err
		}
		return ErrOIDCIdentityTaken
	}
	return nil
}

// UnbindOIDC 解除用户的 OIDC 绑定。
func (s *Service) UnbindOIDC(userID int64) error {
	res, err := s.db.Exec(`UPDATE users SET oidc_iss = '', oidc_sub = '', updated_at = ? WHERE id = ?`,
		time.Now().UTC(), userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateOIDCUser 由 IdP 身份（JIT）创建无密码本地账号：角色固定 user，
// 密码哈希为空串使其天然无法通过密码登录。
func (s *Service) CreateOIDCUser(username, displayName, email, issuer, subject string) (*models.User, error) {
	username = strings.TrimSpace(username)
	if !usernameRe.MatchString(username) {
		return nil, ErrInvalidUsername
	}
	if displayName = strings.TrimSpace(displayName); displayName == "" {
		displayName = username
	}
	email = strings.TrimSpace(email)

	now := time.Now().UTC()
	for range 5 {
		publicID := auth.NewPublicID("u")
		res, err := s.db.Exec(`INSERT INTO users
	  (user_public_id, username, display_name, password_hash, role, is_disabled, email, oidc_iss, oidc_sub, created_at, updated_at)
	  VALUES (?, ?, ?, '', ?, 0, ?, ?, ?, ?, ?)`,
			publicID, username, displayName, models.RoleUser, email, issuer, subject, now, now)
		if err != nil {
			msg := err.Error()
			if strings.Contains(msg, "users.username") {
				return nil, ErrUsernameTaken
			}
			if strings.Contains(msg, "users.oidc_iss") {
				return nil, ErrOIDCIdentityTaken
			}
			if strings.Contains(msg, "users.user_public_id") {
				continue
			}
			return nil, fmt.Errorf("创建 SSO 用户失败: %w", err)
		}
		id, _ := res.LastInsertId()
		return s.GetByID(id)
	}
	return nil, fmt.Errorf("生成 user_public_id 失败")
}

// HasPassword 返回账号是否设置了本地密码（SSO 建号账号为 false）。
func (s *Service) HasPassword(id int64) (bool, error) {
	var hash string
	err := s.db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, id).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return hash != "", err
}

const userColumns = `id, user_public_id, username, display_name, role, is_disabled, quota_bytes, email, oidc_iss, oidc_sub, created_at, updated_at`

func scanUser(row interface{ Scan(...any) error }) (*models.User, error) {
	var u models.User
	err := row.Scan(&u.ID, &u.UserPublicID, &u.Username, &u.DisplayName, &u.Role, &u.IsDisabled, &u.QuotaBytes,
		&u.Email, &u.OIDCIssuer, &u.OIDCSubject, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.SSOBound = u.OIDCSubject != ""
	return &u, nil
}

// GetByID 按内部 ID 查询用户。
func (s *Service) GetByID(id int64) (*models.User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

// GetByUsername 按登录名查询用户。
func (s *Service) GetByUsername(username string) (*models.User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE username = ?`, username))
}

// PasswordHashByUsername 返回用户密码哈希（仅登录校验使用）。
func (s *Service) PasswordHashByUsername(username string) (string, error) {
	var hash string
	err := s.db.QueryRow(`SELECT password_hash FROM users WHERE username = ?`, username).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return hash, err
}

// PasswordHashByID 返回用户密码哈希（修改密码校验旧密码使用）。
func (s *Service) PasswordHashByID(id int64) (string, error) {
	var hash string
	err := s.db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, id).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return hash, err
}

// List 返回全部用户（MVP 用户量小，不分页）。
func (s *Service) List() ([]*models.User, error) {
	rows, err := s.db.Query(`SELECT ` + userColumns + ` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*models.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetDisabled 启用/禁用用户。
func (s *Service) SetDisabled(id int64, disabled bool) error {
	res, err := s.db.Exec(`UPDATE users SET is_disabled = ?, updated_at = ? WHERE id = ?`,
		disabled, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete 删除用户及其关联系统数据（Session、Token、策略绑定、偏好）。
// 不触碰任何真实文件。
func (s *Service) Delete(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE file_records SET owner_user_id = NULL, owner_type = ?, updated_at = ? WHERE owner_user_id = ?`,
		models.FileOwnerUnowned, time.Now().UTC(), id); err != nil {
		return err
	}
	// 删除账号不删除真实图片。原用户图片继续保留公开地址，并转为无账号
	// 归属的图片，之后可由管理员按匿名/无主图片处理。
	if _, err := tx.Exec(`UPDATE images SET owner_user_id = NULL, owner_type = ? WHERE owner_user_id = ?`,
		models.ImageOwnerAnonymous, id); err != nil {
		return err
	}
	// 审计历史必须保留，但不能继续引用即将删除的用户行。
	if _, err := tx.Exec(`UPDATE audit_logs SET actor_user_id = NULL WHERE actor_user_id = ?`, id); err != nil {
		return err
	}

	for _, q := range []string{
		`DELETE FROM sessions WHERE user_id = ?`,
		`DELETE FROM user_tokens WHERE user_id = ?`,
		`DELETE FROM image_bed_tokens WHERE user_id = ?`,
		`DELETE FROM s3_credentials WHERE owner_user_id = ?`,
		`DELETE FROM user_access_policies WHERE user_id = ?`,
		`DELETE FROM user_preferences WHERE user_id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	res, err := tx.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// SetQuota 设置用户拥有文件的硬配额；0 表示不限。
func (s *Service) SetQuota(id, quotaBytes int64) error {
	if quotaBytes < 0 {
		return ErrQuotaInvalid
	}
	res, err := s.db.Exec(`UPDATE users SET quota_bytes = ?, updated_at = ? WHERE id = ?`, quotaBytes, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateDisplayName 修改展示名。
func (s *Service) UpdateDisplayName(id int64, displayName string) error {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return fmt.Errorf("显示名不能为空")
	}
	res, err := s.db.Exec(`UPDATE users SET display_name = ?, updated_at = ? WHERE id = ?`,
		displayName, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdatePassword 修改密码并撤销该用户全部登录 Session。
// CLI 密码重置等账号恢复入口使用此方法。
func (s *Service) UpdatePassword(id int64, newPassword string) error {
	return s.updatePassword(id, newPassword, "")
}

// UpdatePasswordKeepingSession 修改密码并仅保留当前登录 Session。
// 调用方负责校验旧密码以及传入属于该用户的 Session ID。
func (s *Service) UpdatePasswordKeepingSession(id int64, newPassword, keepSessionID string) error {
	if keepSessionID == "" {
		return s.UpdatePassword(id, newPassword)
	}
	return s.updatePassword(id, newPassword, keepSessionID)
}

func (s *Service) updatePassword(id int64, newPassword, keepSessionID string) error {
	if len(newPassword) < 8 {
		return ErrWeakPassword
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		hash, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if keepSessionID == "" {
		_, err = tx.Exec(`DELETE FROM sessions WHERE user_id = ?`, id)
	} else {
		_, err = tx.Exec(`DELETE FROM sessions WHERE user_id = ? AND session_id <> ?`, id, keepSessionID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// RevokeCredentials atomically revokes every login and long-lived client
// credential for a user without disabling the account or touching files.
func (s *Service) RevokeCredentials(id int64) (*RevokedCredentials, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM users WHERE id = ?`, id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}

	result := &RevokedCredentials{}
	for _, item := range []struct {
		query string
		count *int64
	}{
		{`DELETE FROM sessions WHERE user_id = ?`, &result.Sessions},
		{`DELETE FROM user_tokens WHERE user_id = ?`, &result.WebDAVTokens},
		{`DELETE FROM image_bed_tokens WHERE user_id = ?`, &result.ImageBedTokens},
		{`DELETE FROM s3_credentials WHERE owner_user_id = ?`, &result.S3Credentials},
	} {
		res, err := tx.Exec(item.query, id)
		if err != nil {
			return nil, err
		}
		if *item.count, err = res.RowsAffected(); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// CountAdmins 返回未禁用的超级管理员数量。
func (s *Service) CountAdmins() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role = ? AND is_disabled = 0`,
		models.RoleSuperAdmin).Scan(&n)
	return n, err
}
