// Package shares 实现可撤销、可限时和可选密码保护的文件分享。
package shares

import (
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/omni-store/omnistore/internal/auth"
	"github.com/omni-store/omnistore/internal/db"
	"github.com/omni-store/omnistore/internal/files"
	"github.com/omni-store/omnistore/internal/models"
	"github.com/omni-store/omnistore/internal/security"
	"github.com/omni-store/omnistore/internal/sources"
)

const (
	accessCookiePrefix   = "omnistore_share_access_"
	unlockWindow         = 15 * time.Minute
	unlockMaxPerShare    = 10
	unlockMaxPerIP       = 50
	unlockMaxTrackedKeys = 4096
	unlockSweepInterval  = time.Minute
)

// AccessCookieName 为每个分享使用独立 Cookie，避免解锁另一个分享时覆盖现有会话。
func AccessCookieName(shareKey string) string { return accessCookiePrefix + shareKey }

var (
	ErrNotFound       = errors.New("分享不存在或已失效")
	ErrForbidden      = errors.New("无权管理该分享")
	ErrPassword       = errors.New("访问密码错误")
	ErrPasswordLength = errors.New("访问密码必须为 4 到 128 个字符")
	ErrExpiry         = errors.New("有效期必须晚于当前时间且不能超过 365 天")
	ErrDownloadLimit  = errors.New("下载次数上限必须在 0 到 1000000 之间")
	ErrLocked         = errors.New("分享需要访问密码")
	// ErrExpired / ErrExhausted 仅用于公开信息接口，让访客能区分
	// “链接不存在”与“分享已过期 / 次数用完”；内容接口仍统一折叠为 404。
	ErrExpired   = errors.New("分享已过期")
	ErrExhausted = errors.New("分享下载次数已用完")
)

type Share struct {
	ID              int64      `json:"-"`
	Key             string     `json:"key"`
	SourceID        int64      `json:"-"`
	SourceKey       string     `json:"source_key"`
	SourceName      string     `json:"source_name"`
	RelativePath    string     `json:"path"`
	Name            string     `json:"name"`
	EntryType       string     `json:"type"`
	CreatedByUserID int64      `json:"-"`
	CreatedByName   string     `json:"created_by_name"`
	Protected       bool       `json:"protected"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	MaxDownloads    int64      `json:"max_downloads"`
	DownloadCount   int64      `json:"download_count"`
	InTrash         bool       `json:"in_trash"`
	LastAccessedAt  *time.Time `json:"last_accessed_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	URL             string     `json:"url"`
}

type PublicInfo struct {
	Key           string     `json:"key"`
	Name          string     `json:"name"`
	EntryType     string     `json:"type"`
	Protected     bool       `json:"protected"`
	AccessGranted bool       `json:"access_granted"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	MaxDownloads  int64      `json:"max_downloads"`
	DownloadCount int64      `json:"download_count"`
	// Size 仅在文件分享且已通过密码校验时返回，供预览解析器判断可否预览。
	Size *int64 `json:"size,omitempty"`
}

type CreateInput struct {
	SourceKey    string
	Path         string
	Password     string
	ExpiresAt    *time.Time
	MaxDownloads int64
}

type unlockLimitKey struct {
	ip      string
	shareID int64
}

type unlockLimitEntry struct {
	attempts []time.Time
	lastSeen time.Time
}

type unlockLimiter struct {
	mu          sync.Mutex
	window      time.Duration
	maxPerShare int
	maxPerIP    int
	maxKeys     int
	byShare     map[unlockLimitKey]unlockLimitEntry
	byIP        map[string]unlockLimitEntry
	lastSweep   time.Time
	now         func() time.Time
}

type Service struct {
	db              *sql.DB
	rdb             *sql.DB
	sources         *sources.Service
	files           *files.Service
	publicURL       string
	limiter         unlockLimiter
	sessionSweepMu  sync.Mutex
	lastSessionSweep time.Time
}

func NewService(db *sql.DB, srcSvc *sources.Service, fileSvc *files.Service, publicURL string) *Service {
	return &Service{
		db: db, sources: srcSvc, files: fileSvc, publicURL: strings.TrimRight(publicURL, "/"),
		limiter: newUnlockLimiter(unlockWindow, unlockMaxPerShare, unlockMaxPerIP, unlockMaxTrackedKeys),
	}
}

// SetReadDB 注入只读连接池用于热路径分享查询。
func (s *Service) SetReadDB(rdb *sql.DB) {
	s.rdb = rdb
}

func (s *Service) readDB() *sql.DB {
	if s.rdb != nil {
		return s.rdb
	}
	return s.db
}

func (s *Service) Create(user *models.User, input CreateInput) (*Share, error) {
	src, err := s.sources.Get(strings.TrimSpace(input.SourceKey))
	if err != nil || src.IsDisabled {
		return nil, ErrNotFound
	}
	relPath, err := security.NormalizeRelPath(input.Path)
	if err != nil || relPath == "" {
		return nil, files.ErrInvalid
	}
	allowed, err := s.sources.CanWriteSubtree(user, src.Key, relPath)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrForbidden
	}
	entry, err := s.files.Stat(src, relPath)
	if err != nil {
		return nil, err
	}
	if entry.Type != files.TypeFile && entry.Type != files.TypeDir {
		return nil, files.ErrUnsupported
	}
	password := strings.TrimSpace(input.Password)
	var passwordHash any
	if password != "" {
		if n := utf8.RuneCountInString(password); n < 4 || n > 128 {
			return nil, ErrPasswordLength
		}
		hash, err := auth.HashPassword(password)
		if err != nil {
			return nil, err
		}
		passwordHash = hash
	}
	now := time.Now().UTC()
	if input.ExpiresAt != nil {
		expires := input.ExpiresAt.UTC()
		if !expires.After(now) || expires.After(now.Add(365*24*time.Hour)) {
			return nil, ErrExpiry
		}
		input.ExpiresAt = &expires
	}
	if input.MaxDownloads < 0 || input.MaxDownloads > 1_000_000 {
		return nil, ErrDownloadLimit
	}
	var key string
	for attempt := 0; attempt < 5; attempt++ {
		key = auth.NewRandomToken("shr-", 12)
		_, err = s.db.Exec(`INSERT INTO file_shares
  (share_key, storage_source_id, relative_path, entry_type, created_by_user_id, password_hash,
   expires_at, max_downloads, download_count, created_at)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`, key, src.ID, relPath, entry.Type, user.ID,
			passwordHash, input.ExpiresAt, input.MaxDownloads, now)
		if err == nil || !db.IsUniqueConstraint(err, "file_shares.share_key") {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("创建分享失败: %w", err)
	}
	return s.get(key)
}

func (s *Service) List(user *models.User) ([]*Share, error) {
	query := shareSelect + ` WHERE fs.created_by_user_id = ? ORDER BY fs.created_at DESC`
	args := []any{user.ID}
	if user.IsAdmin() {
		query = shareSelect + ` ORDER BY fs.created_at DESC`
		args = nil
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Share
	for rows.Next() {
		share, err := scanShare(rows, s.publicURL)
		if err != nil {
			return nil, err
		}
		out = append(out, share)
	}
	return out, rows.Err()
}

func (s *Service) Delete(user *models.User, key string) (*Share, error) {
	share, err := s.get(key)
	if err != nil {
		return nil, err
	}
	if !user.IsAdmin() && share.CreatedByUserID != user.ID {
		return nil, ErrForbidden
	}
	_, err = s.db.Exec(`DELETE FROM file_shares WHERE id = ?`, share.ID)
	return share, err
}

func (s *Service) PublicInfo(key, sessionToken string) (*PublicInfo, error) {
	share, err := s.getForPublic(key)
	if err != nil {
		return nil, err
	}
	granted := !share.Protected
	if share.Protected && sessionToken != "" {
		granted, err = s.validSession(share.ID, sessionToken)
		if err != nil {
			return nil, err
		}
	}
	info := &PublicInfo{Key: share.Key, Name: share.Name, EntryType: share.EntryType,
		Protected: share.Protected, AccessGranted: granted, ExpiresAt: share.ExpiresAt,
		MaxDownloads: share.MaxDownloads, DownloadCount: share.DownloadCount}
	if granted && share.EntryType == files.TypeFile {
		if src, srcErr := s.sources.GetByID(share.SourceID); srcErr == nil {
			if entry, statErr := s.files.Stat(src, share.RelativePath); statErr == nil {
				info.Size = &entry.Size
			}
		}
	}
	return info, nil
}

func (s *Service) Unlock(key, password, limiterKey string) (string, time.Time, error) {
	share, passwordHash, err := s.getActiveWithPassword(key)
	if err != nil {
		return "", time.Time{}, err
	}
	if !s.limiter.allow(limiterKey, share.ID) {
		return "", time.Time{}, ErrPassword
	}
	if !share.Protected || !auth.VerifyPassword(passwordHash, password) {
		return "", time.Time{}, ErrPassword
	}
	now := time.Now().UTC()
	expires := now.Add(12 * time.Hour)
	if share.ExpiresAt != nil && share.ExpiresAt.Before(expires) {
		expires = *share.ExpiresAt
	}
	token := auth.NewRandomToken("", 32)
	_, err = s.db.Exec(`INSERT INTO share_access_sessions (share_id, token_hash, expires_at, created_at)
  VALUES (?, ?, ?, ?)`, share.ID, auth.HashToken(token), expires, now)
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

func (s *Service) Resolve(key, sessionToken, childPath string) (*Share, *models.StorageSource, string, error) {
	share, err := s.getActive(key)
	if err != nil {
		return nil, nil, "", err
	}
	if share.Protected {
		valid, err := s.validSession(share.ID, sessionToken)
		if err != nil {
			return nil, nil, "", err
		}
		if !valid {
			return nil, nil, "", ErrLocked
		}
	}
	child, err := security.NormalizeRelPath(childPath)
	if err != nil {
		return nil, nil, "", ErrNotFound
	}
	if share.EntryType == files.TypeFile && child != "" {
		return nil, nil, "", ErrNotFound
	}
	relPath := share.RelativePath
	if child != "" {
		relPath += "/" + child
	}
	src, err := s.sources.GetByID(share.SourceID)
	if err != nil || src.IsDisabled {
		return nil, nil, "", ErrNotFound
	}
	return share, src, relPath, nil
}

func (s *Service) Browse(key, sessionToken, childPath string, opts files.ListOptions) (*files.ListResult, error) {
	share, src, relPath, err := s.Resolve(key, sessionToken, childPath)
	if err != nil {
		return nil, err
	}
	if share.EntryType != files.TypeDir {
		return nil, ErrNotFound
	}
	return s.files.List(src, relPath, opts, false)
}

func (s *Service) ReserveDownload(shareID int64) error {
	now := time.Now().UTC()
	result, err := s.db.Exec(`UPDATE file_shares SET download_count = download_count + 1, last_accessed_at = ?
  WHERE id = ? AND trash_key IS NULL AND (expires_at IS NULL OR expires_at > ?)
    AND (max_downloads = 0 OR download_count < max_downloads)`, now, shareID, now)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) Files() *files.Service { return s.files }

const shareSelect = `SELECT fs.id, fs.share_key, fs.storage_source_id, s.key, s.name, fs.relative_path,
  fs.entry_type, fs.created_by_user_id, u.display_name, fs.password_hash IS NOT NULL, fs.expires_at,
  fs.max_downloads, fs.download_count, fs.trash_key IS NOT NULL, fs.last_accessed_at, fs.created_at
  FROM file_shares fs JOIN storage_sources s ON s.id = fs.storage_source_id
  JOIN users u ON u.id = fs.created_by_user_id`

type rowScanner interface{ Scan(...any) error }

func scanShare(row rowScanner, publicURL string) (*Share, error) {
	share := &Share{}
	if err := row.Scan(&share.ID, &share.Key, &share.SourceID, &share.SourceKey, &share.SourceName,
		&share.RelativePath, &share.EntryType, &share.CreatedByUserID, &share.CreatedByName, &share.Protected,
		&share.ExpiresAt, &share.MaxDownloads, &share.DownloadCount, &share.InTrash,
		&share.LastAccessedAt, &share.CreatedAt); err != nil {
		return nil, err
	}
	share.Name = path.Base(share.RelativePath)
	share.URL = publicURL + "/s/" + share.Key
	return share, nil
}

func (s *Service) get(key string) (*Share, error) {
	share, err := scanShare(s.db.QueryRow(shareSelect+` WHERE fs.share_key = ?`, key), s.publicURL)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return share, err
}

func (s *Service) getActive(key string) (*Share, error) {
	share, err := scanShare(s.db.QueryRow(shareSelect+` WHERE fs.share_key = ? AND fs.trash_key IS NULL
  AND (fs.expires_at IS NULL OR fs.expires_at > ?) AND (fs.max_downloads = 0 OR fs.download_count < fs.max_downloads)`, key, time.Now().UTC()), s.publicURL)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return share, err
}

// getForPublic 在 getActive 的基础上区分过期与次数耗尽，供公开信息页展示明确状态。
// 目标被删除、进入回收站或存储源不可用时仍返回 ErrNotFound，不泄露内部细节。
func (s *Service) getForPublic(key string) (*Share, error) {
	share, err := scanShare(s.db.QueryRow(shareSelect+` WHERE fs.share_key = ? AND fs.trash_key IS NULL`, key), s.publicURL)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if src, srcErr := s.sources.GetByID(share.SourceID); srcErr != nil || src.IsDisabled {
		return nil, ErrNotFound
	}
	now := time.Now().UTC()
	if share.ExpiresAt != nil && !share.ExpiresAt.After(now) {
		return nil, ErrExpired
	}
	if share.MaxDownloads > 0 && share.DownloadCount >= share.MaxDownloads {
		return nil, ErrExhausted
	}
	return share, nil
}

func (s *Service) getActiveWithPassword(key string) (*Share, string, error) {
	share, err := s.getActive(key)
	if err != nil {
		return nil, "", err
	}
	var hash sql.NullString
	if err := s.db.QueryRow(`SELECT password_hash FROM file_shares WHERE id = ?`, share.ID).Scan(&hash); err != nil {
		return nil, "", err
	}
	return share, hash.String, nil
}

func (s *Service) validSession(shareID int64, token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	now := time.Now().UTC()
	s.sessionSweepMu.Lock()
	shouldSweep := s.lastSessionSweep.IsZero() || now.Sub(s.lastSessionSweep) >= time.Minute
	if shouldSweep {
		s.lastSessionSweep = now
	}
	s.sessionSweepMu.Unlock()
	if shouldSweep {
		_, _ = s.db.Exec(`DELETE FROM share_access_sessions WHERE expires_at <= ?`, now)
	}
	var found int
	err := s.readDB().QueryRow(`SELECT 1 FROM share_access_sessions WHERE share_id = ? AND token_hash = ? AND expires_at > ?`,
		shareID, auth.HashToken(token), now).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func newUnlockLimiter(window time.Duration, maxPerShare, maxPerIP, maxKeys int) unlockLimiter {
	return unlockLimiter{
		window: window, maxPerShare: maxPerShare, maxPerIP: maxPerIP, maxKeys: maxKeys,
		byShare: make(map[unlockLimitKey]unlockLimitEntry), byIP: make(map[string]unlockLimitEntry),
		now: time.Now,
	}
}

func (l *unlockLimiter) allow(ip string, shareID int64) bool {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		ip = "unknown"
	}
	now := l.now()
	cutoff := now.Add(-l.window)
	key := unlockLimitKey{ip: ip, shareID: shareID}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lastSweep.IsZero() || !now.Before(l.lastSweep.Add(unlockSweepInterval)) {
		l.sweep(cutoff)
		l.lastSweep = now
	}

	ipEntry := pruneUnlockEntry(l.byIP[ip], cutoff)
	if len(ipEntry.attempts) >= l.maxPerIP {
		l.storeIP(ip, ipEntry)
		return false
	}
	shareEntry := pruneUnlockEntry(l.byShare[key], cutoff)
	if len(shareEntry.attempts) >= l.maxPerShare {
		l.storeShare(key, shareEntry)
		return false
	}

	if _, exists := l.byIP[ip]; !exists && len(l.byIP) >= l.maxKeys {
		l.evictOldestIP()
	}
	if _, exists := l.byShare[key]; !exists && len(l.byShare) >= l.maxKeys {
		l.evictOldestShare()
	}
	ipEntry.attempts = append(ipEntry.attempts, now)
	ipEntry.lastSeen = now
	shareEntry.attempts = append(shareEntry.attempts, now)
	shareEntry.lastSeen = now
	l.byIP[ip] = ipEntry
	l.byShare[key] = shareEntry
	return true
}

func pruneUnlockEntry(entry unlockLimitEntry, cutoff time.Time) unlockLimitEntry {
	kept := entry.attempts[:0]
	for _, attempt := range entry.attempts {
		if attempt.After(cutoff) {
			kept = append(kept, attempt)
		}
	}
	entry.attempts = kept
	return entry
}

func (l *unlockLimiter) storeIP(ip string, entry unlockLimitEntry) {
	if len(entry.attempts) == 0 {
		delete(l.byIP, ip)
		return
	}
	l.byIP[ip] = entry
}

func (l *unlockLimiter) storeShare(key unlockLimitKey, entry unlockLimitEntry) {
	if len(entry.attempts) == 0 {
		delete(l.byShare, key)
		return
	}
	l.byShare[key] = entry
}

func (l *unlockLimiter) sweep(cutoff time.Time) {
	for ip, entry := range l.byIP {
		l.storeIP(ip, pruneUnlockEntry(entry, cutoff))
	}
	for key, entry := range l.byShare {
		l.storeShare(key, pruneUnlockEntry(entry, cutoff))
	}
}

func (l *unlockLimiter) evictOldestIP() {
	var oldestKey string
	var oldest time.Time
	for key, entry := range l.byIP {
		if oldestKey == "" || entry.lastSeen.Before(oldest) {
			oldestKey, oldest = key, entry.lastSeen
		}
	}
	delete(l.byIP, oldestKey)
}

func (l *unlockLimiter) evictOldestShare() {
	var oldestKey unlockLimitKey
	var oldest time.Time
	first := true
	for key, entry := range l.byShare {
		if first || entry.lastSeen.Before(oldest) {
			oldestKey, oldest, first = key, entry.lastSeen, false
		}
	}
	if !first {
		delete(l.byShare, oldestKey)
	}
}
