package locks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	ErrPersistentLocked = errors.New("资源已被 WebDAV 锁定")
	ErrLockNotFound     = errors.New("WebDAV 锁不存在")
	ErrLockForbidden    = errors.New("无权操作该 WebDAV 锁")
)

const (
	DepthZero     = "0"
	DepthInfinity = "infinity"
)

// PersistentLock 是数据库中持久化的 WebDAV 独占写锁。
type PersistentLock struct {
	Token           string
	StorageSourceID int64
	RelativePath    string
	Depth           string
	OwnerXML        string
	OwnerUserID     int64
	CreatedAt       time.Time
	RefreshedAt     time.Time
	ExpiresAt       time.Time
}

// MutationScope 描述一次文件写操作影响的路径范围。
type MutationScope struct {
	Path               string
	IncludeDescendants bool
}

// SourceMutation 描述同一次操作在一个存储源内影响的路径范围。
type SourceMutation struct {
	StorageSourceID int64
	Scopes          []MutationScope
}

const persistentLockSweepInterval = time.Minute

// PersistentStore 管理跨请求、跨进程重启保留的 WebDAV 锁。
// mu 将“检查锁 -> 文件变更”与 LOCK 创建串行化，避免检查后的竞态。
type PersistentStore struct {
	db        *sql.DB
	rdb       *sql.DB
	mu        sync.Mutex
	lastSweep time.Time
}

func NewPersistentStore(db *sql.DB) *PersistentStore {
	return &PersistentStore{db: db}
}

// SetReadDB 注入只读连接池用于锁发现和读查询。
func (s *PersistentStore) SetReadDB(rdb *sql.DB) {
	s.rdb = rdb
}

func (s *PersistentStore) readDB() *sql.DB {
	if s.rdb != nil {
		return s.rdb
	}
	return s.db
}

// GuardMutation 校验受影响路径上的锁；成功时调用方必须执行返回的结束函数。
// 文件系统操作成功删除或移动资源时，把已消失的锁根路径传给结束函数，
// 使锁记录清理和 LOCK 创建仍处于同一临界区。
func (s *PersistentStore) GuardMutation(ctx context.Context, storageSourceID int64, scopes []MutationScope, submittedTokens []string, submittedOwnerUserID *int64) (func(...string) error, error) {
	release, err := s.GuardMutations(ctx, []SourceMutation{{StorageSourceID: storageSourceID, Scopes: scopes}}, submittedTokens, submittedOwnerUserID)
	if err != nil {
		return nil, err
	}
	return func(removedRoots ...string) error {
		removed := make(map[int64][]string)
		if len(removedRoots) > 0 {
			removed[storageSourceID] = removedRoots
		}
		return release(removed)
	}, nil
}

// GuardMutations 在同一临界区检查一次操作涉及的多个存储源，避免跨来源移动串联锁时自锁。
// 调用方结束时可按存储源传入已经消失的锁根路径。
func (s *PersistentStore) GuardMutations(ctx context.Context, mutations []SourceMutation, submittedTokens []string, submittedOwnerUserID *int64) (func(map[int64][]string) error, error) {
	s.mu.Lock()
	if err := s.cleanupExpired(ctx); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	tokens := make(map[string]struct{}, len(submittedTokens))
	for _, token := range submittedTokens {
		tokens[token] = struct{}{}
	}
	for _, mutation := range mutations {
		active, err := s.listSource(ctx, mutation.StorageSourceID)
		if err != nil {
			s.mu.Unlock()
			return nil, err
		}
		for _, lock := range active {
			for _, scope := range mutation.Scopes {
				if mutationIntersectsLock(scope, lock) {
					_, tokenSubmitted := tokens[lock.Token]
					ownerMatches := submittedOwnerUserID != nil && lock.OwnerUserID == *submittedOwnerUserID
					if !tokenSubmitted || !ownerMatches {
						s.mu.Unlock()
						return nil, ErrPersistentLocked
					}
					break
				}
			}
		}
	}
	finished := false
	return func(removedRoots map[int64][]string) error {
		if finished {
			return nil
		}
		finished = true
		defer s.mu.Unlock()
		for storageSourceID, roots := range removedRoots {
			for _, root := range roots {
				if err := s.deleteLocksRootedAt(ctx, storageSourceID, root); err != nil {
					return err
				}
			}
		}
		return nil
	}, nil
}

// Create 创建独占写锁。OmniStore 1.0.0 / V1 只实现 RFC 4918 要求的独占锁。
func (s *PersistentStore) Create(ctx context.Context, storageSourceID int64, relPath, depth, ownerXML string, ownerUserID int64, timeout time.Duration) (*PersistentLock, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.cleanupExpired(ctx); err != nil {
		return nil, err
	}
	active, err := s.listSource(ctx, storageSourceID)
	if err != nil {
		return nil, err
	}
	for _, existing := range active {
		if lockScopesIntersect(relPath, depth, existing.RelativePath, existing.Depth) {
			return nil, ErrPersistentLocked
		}
	}
	now := time.Now().UTC()
	lock := &PersistentLock{
		Token:           newLockToken(),
		StorageSourceID: storageSourceID,
		RelativePath:    relPath,
		Depth:           depth,
		OwnerXML:        ownerXML,
		OwnerUserID:     ownerUserID,
		CreatedAt:       now,
		RefreshedAt:     now,
		ExpiresAt:       now.Add(timeout),
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO webdav_locks
  (token, storage_source_id, relative_path, depth, owner_xml, owner_user_id, created_at, refreshed_at, expires_at)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, lock.Token, lock.StorageSourceID, lock.RelativePath, lock.Depth,
		lock.OwnerXML, lock.OwnerUserID, lock.CreatedAt, lock.RefreshedAt, lock.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("创建 WebDAV 锁失败: %w", err)
	}
	return lock, nil
}

func (s *PersistentStore) Refresh(ctx context.Context, token string, ownerUserID int64, storageSourceID int64, relPath string, timeout time.Duration) (*PersistentLock, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.cleanupExpired(ctx); err != nil {
		return nil, err
	}
	lock, err := s.get(ctx, token)
	if err != nil {
		return nil, err
	}
	if lock.OwnerUserID != ownerUserID {
		return nil, ErrLockForbidden
	}
	if lock.StorageSourceID != storageSourceID || !lockCovers(lock.RelativePath, lock.Depth, relPath) {
		return nil, ErrLockNotFound
	}
	lock.RefreshedAt = time.Now().UTC()
	lock.ExpiresAt = lock.RefreshedAt.Add(timeout)
	if _, err := s.db.ExecContext(ctx, `UPDATE webdav_locks SET refreshed_at = ?, expires_at = ? WHERE token = ?`,
		lock.RefreshedAt, lock.ExpiresAt, token); err != nil {
		return nil, err
	}
	return lock, nil
}

func (s *PersistentStore) Unlock(ctx context.Context, token string, ownerUserID int64, storageSourceID int64, relPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.cleanupExpired(ctx); err != nil {
		return err
	}
	lock, err := s.get(ctx, token)
	if err != nil {
		return err
	}
	if lock.OwnerUserID != ownerUserID {
		return ErrLockForbidden
	}
	if lock.StorageSourceID != storageSourceID || !lockCovers(lock.RelativePath, lock.Depth, relPath) {
		return ErrLockNotFound
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM webdav_locks WHERE token = ?`, token)
	return err
}

// Discover 返回直接或通过 Depth: infinity 覆盖目标资源的活动锁。
func (s *PersistentStore) Discover(ctx context.Context, storageSourceID int64, relPath string) ([]PersistentLock, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.cleanupExpired(ctx); err != nil {
		return nil, err
	}
	active, err := s.listSource(ctx, storageSourceID)
	if err != nil {
		return nil, err
	}
	out := make([]PersistentLock, 0, len(active))
	for _, lock := range active {
		if lockCovers(lock.RelativePath, lock.Depth, relPath) {
			out = append(out, lock)
		}
	}
	return out, nil
}

func (s *PersistentStore) Delete(ctx context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM webdav_locks WHERE token = ?`, token)
	return err
}

func (s *PersistentStore) CleanupExpired(ctx context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cleanupExpiredCount(ctx)
}

func (s *PersistentStore) cleanupExpired(ctx context.Context) error {
	now := time.Now().UTC()
	var hasExpired int
	// 利用 idx_webdav_locks_expires_at 在只读池做零写开销检查；仅当确实存在过期锁时才触发写池 DELETE。
	if err := s.readDB().QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM webdav_locks WHERE expires_at <= ?)`, now).Scan(&hasExpired); err != nil {
		return err
	}
	if hasExpired == 0 {
		return nil
	}
	_, err := s.cleanupExpiredCount(ctx)
	return err
}

func (s *PersistentStore) cleanupExpiredCount(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM webdav_locks WHERE expires_at <= ?`, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// deleteLocksRootedAt 删除以 root 本身或其后代为根的锁。
// 该方法只在 PersistentStore.mu 已持有时调用；外部祖先的 Depth: infinity 锁不会被删除。
func (s *PersistentStore) deleteLocksRootedAt(ctx context.Context, storageSourceID int64, root string) error {
	var err error
	if root == "" {
		_, err = s.db.ExecContext(ctx, `DELETE FROM webdav_locks WHERE storage_source_id = ?`, storageSourceID)
	} else {
		_, err = s.db.ExecContext(ctx, `DELETE FROM webdav_locks
  WHERE storage_source_id = ? AND (relative_path = ? OR substr(relative_path, 1, length(?) + 1) = ? || '/')`,
			storageSourceID, root, root, root)
	}
	if err != nil {
		return fmt.Errorf("清理已移除资源的 WebDAV 锁失败: %w", err)
	}
	return nil
}

func (s *PersistentStore) get(ctx context.Context, token string) (*PersistentLock, error) {
	var lock PersistentLock
	err := s.readDB().QueryRowContext(ctx, `SELECT token, storage_source_id, relative_path, depth, owner_xml,
  owner_user_id, created_at, refreshed_at, expires_at FROM webdav_locks WHERE token = ?`, token).
		Scan(&lock.Token, &lock.StorageSourceID, &lock.RelativePath, &lock.Depth, &lock.OwnerXML,
			&lock.OwnerUserID, &lock.CreatedAt, &lock.RefreshedAt, &lock.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrLockNotFound
	}
	if err != nil {
		return nil, err
	}
	if !lock.ExpiresAt.After(time.Now().UTC()) {
		return nil, ErrLockNotFound
	}
	return &lock, nil
}

func (s *PersistentStore) listSource(ctx context.Context, storageSourceID int64) ([]PersistentLock, error) {
	rows, err := s.readDB().QueryContext(ctx, `SELECT token, storage_source_id, relative_path, depth, owner_xml,
  owner_user_id, created_at, refreshed_at, expires_at FROM webdav_locks
  WHERE storage_source_id = ? AND expires_at > ? ORDER BY created_at`, storageSourceID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PersistentLock
	for rows.Next() {
		var lock PersistentLock
		if err := rows.Scan(&lock.Token, &lock.StorageSourceID, &lock.RelativePath, &lock.Depth, &lock.OwnerXML,
			&lock.OwnerUserID, &lock.CreatedAt, &lock.RefreshedAt, &lock.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, lock)
	}
	return out, rows.Err()
}

func mutationIntersectsLock(scope MutationScope, lock PersistentLock) bool {
	return lockCovers(lock.RelativePath, lock.Depth, scope.Path) ||
		(scope.IncludeDescendants && isSameOrDescendant(scope.Path, lock.RelativePath))
}

func lockScopesIntersect(pathA, depthA, pathB, depthB string) bool {
	return lockCovers(pathA, depthA, pathB) || lockCovers(pathB, depthB, pathA)
}

func lockCovers(lockPath, depth, target string) bool {
	if lockPath == target {
		return true
	}
	return depth == DepthInfinity && isSameOrDescendant(lockPath, target)
}

func isSameOrDescendant(parent, child string) bool {
	if parent == "" {
		return true
	}
	return child == parent || strings.HasPrefix(child, parent+"/")
}

func newLockToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	raw := hex.EncodeToString(b[:])
	return "urn:uuid:" + raw[0:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:32]
}
