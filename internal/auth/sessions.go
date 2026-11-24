package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/omni-store/omnistore/internal/models"
)

// SessionCookieName 是登录态 Cookie 名称。
const SessionCookieName = "omnistore_session"

const sessionLastSeenThrottle = time.Minute

// ErrSessionInvalid 表示 Session 不存在、已过期或用户不可用。
var ErrSessionInvalid = errors.New("session 无效")

// Sessions 管理 SQLite 中的登录 Session。
type Sessions struct {
	db          *sql.DB
	rdb         *sql.DB
	ttl         time.Duration
	touchMu     sync.Mutex
	lastTouched map[string]time.Time
}

// NewSessions 创建 Session 服务，ttl 为登录有效期。
func NewSessions(db *sql.DB, ttl time.Duration) *Sessions {
	return &Sessions{db: db, ttl: ttl, lastTouched: make(map[string]time.Time)}
}

// SetReadDB 注入只读连接池，使热路径会话校验不阻塞在单连接写池上。
func (s *Sessions) SetReadDB(rdb *sql.DB) {
	s.rdb = rdb
}

func (s *Sessions) readDB() *sql.DB {
	if s.rdb != nil {
		return s.rdb
	}
	return s.db
}

func (s *Sessions) shouldTouchLastSeen(sessionHash string, dbLastSeen, now time.Time) bool {
	if now.Sub(dbLastSeen) < sessionLastSeenThrottle {
		return false
	}
	s.touchMu.Lock()
	defer s.touchMu.Unlock()
	if s.lastTouched == nil {
		s.lastTouched = make(map[string]time.Time)
	}
	if prev, ok := s.lastTouched[sessionHash]; ok && now.Sub(prev) < sessionLastSeenThrottle {
		return false
	}
	if len(s.lastTouched) >= 4096 {
		cutoff := now.Add(-sessionLastSeenThrottle)
		for k, v := range s.lastTouched {
			if v.Before(cutoff) {
				delete(s.lastTouched, k)
			}
		}
		if len(s.lastTouched) >= 4096 {
			clear(s.lastTouched)
		}
	}
	s.lastTouched[sessionHash] = now
	return true
}

// TTL 返回 Session 有效期。
func (s *Sessions) TTL() time.Duration {
	return s.ttl
}

// Create 为用户创建 Session，返回原始 session_id 和明文 CSRF Token。
// 数据库中仅保存 SHA-256(session_id)，不落盘明文凭据；CSRF Token 按需确定性派生。
func (s *Sessions) Create(userID int64, userAgent, ipAddress string) (sessionID, csrfToken string, err error) {
	sessionID = NewRandomToken("", 32)
	csrfToken = csrfTokenForSession(sessionID)
	now := time.Now().UTC()

	_, err = s.db.Exec(`INSERT INTO sessions
  (session_id, user_id, expires_at, created_at, last_seen_at, user_agent, ip_address)
  VALUES (?, ?, ?, ?, ?, ?, ?)`,
		HashToken(sessionID), userID, now.Add(s.ttl), now, now, userAgent, ipAddress)
	if err != nil {
		return "", "", fmt.Errorf("创建 session 失败: %w", err)
	}
	return sessionID, csrfToken, nil
}

// csrfTokenForSession 从高熵 Session ID 单向派生稳定的 CSRF Token。
// 域分离常量避免该值与其他 Session 派生用途混用。
func csrfTokenForSession(sessionID string) string {
	mac := hmac.New(sha256.New, []byte(sessionID))
	_, _ = mac.Write([]byte("omnistore-csrf-v1"))
	return hex.EncodeToString(mac.Sum(nil))
}

// Validate 校验 Session 并返回关联用户。
// Session 过期、用户被禁用都会返回 ErrSessionInvalid。
func (s *Sessions) Validate(sessionID string) (*models.User, *models.Session, error) {
	if sessionID == "" {
		return nil, nil, ErrSessionInvalid
	}
	sessionHash := HashToken(sessionID)
	row := s.readDB().QueryRow(`SELECT
    s.user_id, s.expires_at, s.last_seen_at,
    u.id, u.user_public_id, u.username, u.display_name, u.role, u.is_disabled,
    u.email, u.oidc_iss, u.oidc_sub, u.created_at, u.updated_at
  FROM sessions s JOIN users u ON u.id = s.user_id
  WHERE s.session_id = ?`, sessionHash)

	var sess models.Session
	var u models.User
	err := row.Scan(
		&sess.UserID, &sess.ExpiresAt, &sess.LastSeenAt,
		&u.ID, &u.UserPublicID, &u.Username, &u.DisplayName, &u.Role, &u.IsDisabled,
		&u.Email, &u.OIDCIssuer, &u.OIDCSubject, &u.CreatedAt, &u.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrSessionInvalid
	}
	if err != nil {
		return nil, nil, fmt.Errorf("查询 session 失败: %w", err)
	}
	now := time.Now().UTC()
	if now.After(sess.ExpiresAt) || u.IsDisabled {
		return nil, nil, ErrSessionInvalid
	}
	// 内存中保留原始 sessionID 供 CSRF 派生与密码修改保留当前会话使用，不回写明文到 DB。
	sess.SessionID = sessionID
	u.SSOBound = u.OIDCSubject != ""

	// 活跃时间节流写库（距上次 > 60s 且内存中去重），减少热路径写竞争。
	if s.shouldTouchLastSeen(sessionHash, sess.LastSeenAt, now) {
		_, _ = s.db.Exec(`UPDATE sessions SET last_seen_at = ? WHERE session_id = ?`,
			now, sessionHash)
	}
	return &u, &sess, nil
}

// VerifyCSRF 重新派生该 Session 的期望 CSRF Token 并做常量时间比较。
func (s *Sessions) VerifyCSRF(sess *models.Session, csrfToken string) bool {
	if sess == nil || sess.SessionID == "" || csrfToken == "" {
		return false
	}
	return TokenEqual(csrfTokenForSession(sess.SessionID), csrfToken)
}

// CSRFToken 返回指定 Session 的稳定 CSRF Token，不读写数据库。
// 新建 Session 会得到不同 Token；重复恢复当前 Session 不会使其他标签页失效。
func (s *Sessions) CSRFToken(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	return csrfTokenForSession(sessionID)
}

// Delete 删除指定 Session（退出登录）。
func (s *Sessions) Delete(sessionID string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE session_id = ?`, HashToken(sessionID))
	return err
}

// DeleteByUser 删除某用户全部 Session（禁用/删除用户时调用）。
func (s *Sessions) DeleteByUser(userID int64) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

// CleanupExpired 清理过期 Session（README §21 唯一必要后台任务）。
func (s *Sessions) CleanupExpired() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
