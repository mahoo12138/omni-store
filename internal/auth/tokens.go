package auth

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/omni-store/omnistore/internal/db"
	"github.com/omni-store/omnistore/internal/models"
)

// Token 类型。WebDAV 存在 user_tokens，图床 Token 存在 image_bed_tokens。
const (
	TokenTypeWebDAV   = "webdav"
	TokenTypeImageBed = "image_bed"
)

// ErrTokenInvalid Token 无效或用户不可用。
var ErrTokenInvalid = errors.New("token 无效")

var (
	// ErrImageBedTokenNotFound 图床 Token 不存在或不属于当前用户。
	ErrImageBedTokenNotFound = errors.New("图床 Token 不存在")
	// ErrImageBedTokenLimit 单个用户的图床 Token 已达到上限。
	ErrImageBedTokenLimit = errors.New("图床 Token 最多创建 10 个")
	// ErrImageBedTokenLabel 图床 Token 名称不合法。
	ErrImageBedTokenLabel = errors.New("Token 名称必须为 1-32 个可见字符")
)

// MaxImageBedTokens 是每个用户可同时持有的图床 Token 上限。
const MaxImageBedTokens = 10

const tokenLastUsedThrottle = time.Minute

// Tokens 管理用户 WebDAV Token 和图床 API Token。
// WebDAV 每用户一个；图床 Token 可命名并创建多个。两者都只保存哈希。
type Tokens struct {
	db          *sql.DB
	rdb         *sql.DB
	touchMu     sync.Mutex
	lastTouched map[string]time.Time
}

// NewTokens 创建 Token 服务。
func NewTokens(db *sql.DB) *Tokens {
	return &Tokens{db: db, lastTouched: make(map[string]time.Time)}
}

// SetReadDB 注入只读连接池用于热路径 Token 校验查询。
func (t *Tokens) SetReadDB(rdb *sql.DB) {
	t.rdb = rdb
}

func (t *Tokens) readDB() *sql.DB {
	if t.rdb != nil {
		return t.rdb
	}
	return t.db
}

func (t *Tokens) shouldTouchLastUsed(key string, dbLastUsed sql.NullTime, now time.Time) bool {
	if dbLastUsed.Valid && now.Sub(dbLastUsed.Time) < tokenLastUsedThrottle {
		return false
	}
	t.touchMu.Lock()
	defer t.touchMu.Unlock()
	if t.lastTouched == nil {
		t.lastTouched = make(map[string]time.Time)
	}
	// 数据库尚无使用时间时（如刚创建或重置）允许立即落盘一次，随后按 60s 节流。
	if dbLastUsed.Valid {
		if prev, ok := t.lastTouched[key]; ok && now.Sub(prev) < tokenLastUsedThrottle {
			return false
		}
	}
	if len(t.lastTouched) >= 4096 {
		clear(t.lastTouched)
	}
	t.lastTouched[key] = now
	return true
}

// Reset 兼容单 Token 重置接口，返回明文（只展示一次）。
// WebDAV 撤销旧 Token；图床类型会撤销该用户全部图床 Token。
func (t *Tokens) Reset(userID int64, tokenType string) (string, error) {
	if tokenType != TokenTypeWebDAV && tokenType != TokenTypeImageBed {
		return "", fmt.Errorf("非法 token 类型: %s", tokenType)
	}
	if tokenType == TokenTypeImageBed {
		return t.resetImageBedTokens(userID)
	}
	plaintext := NewRandomToken("", 24)
	now := time.Now().UTC()
	_, err := t.db.Exec(`INSERT INTO user_tokens (user_id, token_type, token_hash, created_at)
  VALUES (?, ?, ?, ?)
  ON CONFLICT(user_id, token_type) DO UPDATE SET token_hash = excluded.token_hash,
    created_at = excluded.created_at, last_used_at = NULL`,
		userID, tokenType, HashToken(plaintext), now)
	if err != nil {
		return "", fmt.Errorf("生成 token 失败: %w", err)
	}
	return plaintext, nil
}

// ImageBedToken 是图床 Token 的可展示元数据，不包含明文或哈希。
type ImageBedToken struct {
	TokenID    string     `json:"token_id"`
	Label      string     `json:"label"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

type tokenExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func normalizeImageBedTokenLabel(label string) (string, error) {
	label = strings.TrimSpace(label)
	length := len([]rune(label))
	if length < 1 || length > 32 {
		return "", ErrImageBedTokenLabel
	}
	for _, r := range label {
		if r < 32 || r == 127 {
			return "", ErrImageBedTokenLabel
		}
	}
	return label, nil
}

func insertImageBedToken(exec tokenExecer, userID int64, label string) (*ImageBedToken, string, error) {
	plaintext := NewRandomToken("", 24)
	now := time.Now().UTC()
	for range 5 {
		tokenID := NewPublicID("ibt")
		_, err := exec.Exec(`INSERT INTO image_bed_tokens
  (token_id, user_id, label, token_hash, created_at) VALUES (?, ?, ?, ?, ?)`,
			tokenID, userID, label, HashToken(plaintext), now)
		if err == nil {
			return &ImageBedToken{TokenID: tokenID, Label: label, CreatedAt: now}, plaintext, nil
		}
		if !db.IsUniqueConstraint(err, "image_bed_tokens.token_id") {
			return nil, "", fmt.Errorf("创建图床 Token 失败: %w", err)
		}
	}
	return nil, "", fmt.Errorf("生成图床 token_id 失败")
}

// CreateImageBedToken 创建一个命名图床 Token，明文只在本次调用返回。
func (t *Tokens) CreateImageBedToken(userID int64, label string) (*ImageBedToken, string, error) {
	label, err := normalizeImageBedTokenLabel(label)
	if err != nil {
		return nil, "", err
	}
	tx, err := t.db.Begin()
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM image_bed_tokens WHERE user_id = ?`, userID).Scan(&count); err != nil {
		return nil, "", err
	}
	if count >= MaxImageBedTokens {
		return nil, "", ErrImageBedTokenLimit
	}
	item, plaintext, err := insertImageBedToken(tx, userID, label)
	if err != nil {
		return nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	return item, plaintext, nil
}

// ListImageBedTokens 返回当前用户的图床 Token 元数据。
func (t *Tokens) ListImageBedTokens(userID int64) ([]*ImageBedToken, error) {
	rows, err := t.db.Query(`SELECT token_id, label, created_at, last_used_at
  FROM image_bed_tokens WHERE user_id = ? ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*ImageBedToken{}
	for rows.Next() {
		var item ImageBedToken
		var lastUsed sql.NullTime
		if err := rows.Scan(&item.TokenID, &item.Label, &item.CreatedAt, &lastUsed); err != nil {
			return nil, err
		}
		if lastUsed.Valid {
			item.LastUsedAt = &lastUsed.Time
		}
		out = append(out, &item)
	}
	return out, rows.Err()
}

// DeleteImageBedToken 仅撤销指定用户自己的图床 Token。
func (t *Tokens) DeleteImageBedToken(userID int64, tokenID string) error {
	res, err := t.db.Exec(`DELETE FROM image_bed_tokens WHERE user_id = ? AND token_id = ?`, userID, tokenID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrImageBedTokenNotFound
	}
	return nil
}

// resetImageBedTokens 兼容旧的单 Token 重置接口：撤销全部图床 Token并生成一个默认 Token。
func (t *Tokens) resetImageBedTokens(userID int64) (string, error) {
	tx, err := t.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM image_bed_tokens WHERE user_id = ?`, userID); err != nil {
		return "", err
	}
	_, plaintext, err := insertImageBedToken(tx, userID, "默认 Token")
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return plaintext, nil
}

// Verify 按用户名和明文 Token 校验。用户被禁用时 Token 失效（README §8.6）。
func (t *Tokens) Verify(username, tokenType, plaintext string) (*models.User, error) {
	if username == "" || plaintext == "" {
		return nil, ErrTokenInvalid
	}
	row := t.readDB().QueryRow(`SELECT
    u.id, u.user_public_id, u.username, u.display_name, u.role, u.is_disabled, u.created_at, u.updated_at,
    t.token_hash, t.last_used_at
  FROM users u JOIN user_tokens t ON t.user_id = u.id
  WHERE u.username = ? AND t.token_type = ?`, username, tokenType)

	var u models.User
	var hash string
	var lastUsed sql.NullTime
	err := row.Scan(&u.ID, &u.UserPublicID, &u.Username, &u.DisplayName, &u.Role,
		&u.IsDisabled, &u.CreatedAt, &u.UpdatedAt, &hash, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTokenInvalid
	}
	if err != nil {
		return nil, err
	}
	if u.IsDisabled || !TokenEqual(hash, HashToken(plaintext)) {
		return nil, ErrTokenInvalid
	}

	now := time.Now().UTC()
	if t.shouldTouchLastUsed(fmt.Sprintf("ut:%d:%s", u.ID, tokenType), lastUsed, now) {
		_, _ = t.db.Exec(`UPDATE user_tokens SET last_used_at = ? WHERE user_id = ? AND token_type = ?`,
			now, u.ID, tokenType)
	}
	return &u, nil
}

// VerifyByToken 按明文 Token 直接校验（PicGo 等不携带用户名的入口）。
// 数据库只存哈希，按哈希精确查找。
func (t *Tokens) VerifyByToken(tokenType, plaintext string) (*models.User, error) {
	if plaintext == "" {
		return nil, ErrTokenInvalid
	}
	if tokenType == TokenTypeImageBed {
		return t.verifyImageBedToken(plaintext)
	}
	row := t.readDB().QueryRow(`SELECT
    u.id, u.user_public_id, u.username, u.display_name, u.role, u.is_disabled, u.created_at, u.updated_at,
    t.last_used_at
  FROM users u JOIN user_tokens t ON t.user_id = u.id
  WHERE t.token_type = ? AND t.token_hash = ?`, tokenType, HashToken(plaintext))

	var u models.User
	var lastUsed sql.NullTime
	err := row.Scan(&u.ID, &u.UserPublicID, &u.Username, &u.DisplayName, &u.Role,
		&u.IsDisabled, &u.CreatedAt, &u.UpdatedAt, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTokenInvalid
	}
	if err != nil {
		return nil, err
	}
	if u.IsDisabled {
		return nil, ErrTokenInvalid
	}
	now := time.Now().UTC()
	if t.shouldTouchLastUsed(fmt.Sprintf("ut:%d:%s", u.ID, tokenType), lastUsed, now) {
		_, _ = t.db.Exec(`UPDATE user_tokens SET last_used_at = ? WHERE user_id = ? AND token_type = ?`,
			now, u.ID, tokenType)
	}
	return &u, nil
}

func (t *Tokens) verifyImageBedToken(plaintext string) (*models.User, error) {
	row := t.readDB().QueryRow(`SELECT
    u.id, u.user_public_id, u.username, u.display_name, u.role, u.is_disabled, u.created_at, u.updated_at,
    t.token_id, t.last_used_at
  FROM users u JOIN image_bed_tokens t ON t.user_id = u.id
  WHERE t.token_hash = ?`, HashToken(plaintext))

	var u models.User
	var tokenID string
	var lastUsed sql.NullTime
	err := row.Scan(&u.ID, &u.UserPublicID, &u.Username, &u.DisplayName, &u.Role,
		&u.IsDisabled, &u.CreatedAt, &u.UpdatedAt, &tokenID, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTokenInvalid
	}
	if err != nil {
		return nil, err
	}
	if u.IsDisabled {
		return nil, ErrTokenInvalid
	}
	now := time.Now().UTC()
	if t.shouldTouchLastUsed("ibt:"+tokenID, lastUsed, now) {
		_, _ = t.db.Exec(`UPDATE image_bed_tokens SET last_used_at = ? WHERE token_id = ?`,
			now, tokenID)
	}
	return &u, nil
}

// TokenStatus 是 Token 状态（不含明文）。
type TokenStatus struct {
	Exists     bool       `json:"exists"`
	Count      int        `json:"count"`
	CreatedAt  *time.Time `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// Status 返回用户各类型 Token 状态。
func (t *Tokens) Status(userID int64) (map[string]TokenStatus, error) {
	out := map[string]TokenStatus{
		TokenTypeWebDAV:   {},
		TokenTypeImageBed: {},
	}
	rows, err := t.db.Query(`SELECT token_type, created_at, last_used_at FROM user_tokens
  WHERE user_id = ? AND token_type = ?`, userID, TokenTypeWebDAV)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var typ string
		var createdAt time.Time
		var lastUsed sql.NullTime
		if err := rows.Scan(&typ, &createdAt, &lastUsed); err != nil {
			return nil, err
		}
		st := TokenStatus{Exists: true, Count: 1, CreatedAt: &createdAt}
		if lastUsed.Valid {
			st.LastUsedAt = &lastUsed.Time
		}
		out[typ] = st
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	imageTokens, err := t.ListImageBedTokens(userID)
	if err != nil {
		return nil, err
	}
	if len(imageTokens) > 0 {
		latest := imageTokens[0]
		out[TokenTypeImageBed] = TokenStatus{
			Exists: true, Count: len(imageTokens), CreatedAt: &latest.CreatedAt,
			LastUsedAt: latest.LastUsedAt,
		}
	}
	return out, nil
}
