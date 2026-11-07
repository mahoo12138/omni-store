// Package models 定义系统数据结构。
package models

import "time"

// 用户角色。
const (
	RoleSuperAdmin = "super_admin"
	RoleUser       = "user"
)

// User 对应 users 表。
type User struct {
	ID           int64  `json:"id"`
	UserPublicID string `json:"user_public_id"`
	Username     string `json:"username"`
	DisplayName  string `json:"display_name"`
	Role         string `json:"role"`
	IsDisabled   bool   `json:"is_disabled"`
	QuotaBytes   int64  `json:"quota_bytes"`
	Email        string `json:"email,omitempty"`
	// OIDC 身份明细不对 API 暴露，仅对外给出是否已绑定 SSO（2.1.0）。
	OIDCIssuer  string    `json:"-"`
	OIDCSubject string    `json:"-"`
	SSOBound    bool      `json:"sso_bound,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// IsAdmin 判断是否超级管理员。
func (u *User) IsAdmin() bool {
	return u.Role == RoleSuperAdmin
}

// Session 对应 sessions 表。
type Session struct {
	SessionID     string
	UserID        int64
	CSRFTokenHash string
	ExpiresAt     time.Time
	CreatedAt     time.Time
	LastSeenAt    time.Time
	UserAgent     string
	IPAddress     string
}
