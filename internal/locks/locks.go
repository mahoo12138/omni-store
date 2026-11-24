// Package locks 实现请求级内存路径读写锁（README §15）。
// MVP 只支持单实例，锁在进程内存中，不做持久锁和分布式锁。
package locks

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// Manager 管理按 key 的读写锁。文件路径 key = opaque_source_key + "\x00" +
// normalized_relative_path；同一来源内祖先与后代路径互相冲突，兄弟路径仍可并发。
// 不含分隔符的协调 key（例如 quota:*）继续只做精确匹配。
// 对大小写不敏感文件系统上的存储源，调用方可通过 SetSourceCaseInsensitive 注册归一化规则，
// 确保 "A.txt" 与 "a.txt" 在锁冲突判定上映射到同一把锁。
type Manager struct {
	mu              sync.Mutex
	entries         map[string]*entry
	waiters         []*waiter
	caseInsensitive map[string]bool
}

type entry struct {
	readers int
	writers int
}

type waiter struct {
	keys  []string
	write bool
	ready chan struct{}
}

// NewManager 创建锁管理器。
func NewManager() *Manager {
	return &Manager{
		entries:         make(map[string]*entry),
		caseInsensitive: make(map[string]bool),
	}
}

// SetSourceCaseInsensitive 配置某存储源是否位于大小写不敏感文件系统上。
func (m *Manager) SetSourceCaseInsensitive(sourceKey string, ci bool) {
	m.mu.Lock()
	if m.caseInsensitive == nil {
		m.caseInsensitive = make(map[string]bool)
	}
	if ci {
		m.caseInsensitive[sourceKey] = true
	} else {
		delete(m.caseInsensitive, sourceKey)
	}
	m.mu.Unlock()
}

func (m *Manager) canonicalizeKeysLocked(keys []string) []string {
	ordered := make([]string, len(keys))
	for i, key := range keys {
		if src, rel, ok := splitPathKey(key); ok && m.caseInsensitive[src] {
			key = Key(src, strings.ToLower(rel))
		}
		ordered[i] = key
	}
	return normalizeKeys(ordered)
}

// Key 构造锁 key。
func Key(sourceKey, relPath string) string {
	return sourceKey + "\x00" + relPath
}

func splitPathKey(key string) (source, relPath string, ok bool) {
	index := strings.IndexByte(key, 0)
	if index < 0 {
		return "", "", false
	}
	return key[:index], key[index+1:], true
}

func isSameOrDescendantPath(parent, child string) bool {
	return parent == "" || child == parent || strings.HasPrefix(child, parent+"/")
}

func keysConflict(left, right string) bool {
	if left == right {
		return true
	}
	leftSource, leftPath, leftIsPath := splitPathKey(left)
	rightSource, rightPath, rightIsPath := splitPathKey(right)
	if !leftIsPath || !rightIsPath || leftSource != rightSource {
		return false
	}
	return isSameOrDescendantPath(leftPath, rightPath) || isSameOrDescendantPath(rightPath, leftPath)
}

func normalizeKeys(keys []string) []string {
	ordered := append([]string(nil), keys...)
	sort.Strings(ordered)
	out := ordered[:0]
	for _, key := range ordered {
		if len(out) == 0 || out[len(out)-1] != key {
			out = append(out, key)
		}
	}
	return out
}

func waiterConflicts(left, right *waiter) bool {
	if !left.write && !right.write {
		return false
	}
	for _, leftKey := range left.keys {
		for _, rightKey := range right.keys {
			if keysConflict(leftKey, rightKey) {
				return true
			}
		}
	}
	return false
}

func (m *Manager) canAcquire(candidate *waiter) bool {
	for key, active := range m.entries {
		if active.writers == 0 && !candidate.write {
			continue
		}
		for _, candidateKey := range candidate.keys {
			if keysConflict(candidateKey, key) {
				return false
			}
		}
	}
	// 只阻止与更早等待者冲突的请求；不相交的兄弟路径可以越过队列并发。
	for _, queued := range m.waiters {
		if queued == candidate {
			break
		}
		if waiterConflicts(candidate, queued) {
			return false
		}
	}
	return true
}

func (m *Manager) grantLocked(keys []string, write bool) {
	for _, key := range keys {
		active := m.entries[key]
		if active == nil {
			active = &entry{}
			m.entries[key] = active
		}
		if write {
			active.writers++
		} else {
			active.readers++
		}
	}
}

func (m *Manager) removeWaiterLocked(target *waiter) bool {
	for index, queued := range m.waiters {
		if queued == target {
			m.waiters = append(m.waiters[:index], m.waiters[index+1:]...)
			return true
		}
	}
	return false
}

// wakeEligibleLocked 只唤醒当前可获取锁的等待者，避免 Broadcast 惊群；
// 被唤醒的等待者在移除队列前已经直接获得锁占用，保证后续扫描看到最新状态。
func (m *Manager) wakeEligibleLocked() {
	for i := 0; i < len(m.waiters); {
		candidate := m.waiters[i]
		if !m.canAcquire(candidate) {
			i++
			continue
		}
		m.waiters = append(m.waiters[:i], m.waiters[i+1:]...)
		m.grantLocked(candidate.keys, candidate.write)
		close(candidate.ready)
	}
}

func (m *Manager) releaseFunc(keys []string, write bool) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			for _, key := range keys {
				active := m.entries[key]
				if write {
					active.writers--
				} else {
					active.readers--
				}
				if active.readers == 0 && active.writers == 0 {
					delete(m.entries, key)
				}
			}
			m.wakeEligibleLocked()
			m.mu.Unlock()
		})
	}
}

func (m *Manager) acquireContext(ctx context.Context, keys []string, write bool) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	keys = m.canonicalizeKeysLocked(keys)
	candidate := &waiter{keys: keys, write: write, ready: make(chan struct{})}
	if m.canAcquire(candidate) {
		m.grantLocked(keys, write)
		m.mu.Unlock()
		return m.releaseFunc(keys, write), nil
	}
	m.waiters = append(m.waiters, candidate)
	m.mu.Unlock()

	select {
	case <-candidate.ready:
		return m.releaseFunc(keys, write), nil
	case <-ctx.Done():
		m.mu.Lock()
		removed := m.removeWaiterLocked(candidate)
		if removed {
			// 该等待者曾可能阻挡后续冲突请求，退出后立即尝试唤醒队列。
			m.wakeEligibleLocked()
			m.mu.Unlock()
			return nil, ctx.Err()
		}
		m.mu.Unlock()
		// 与唤醒竞态：锁已授予当前等待者，立即释放后再返回取消错误。
		<-candidate.ready
		m.releaseFunc(keys, write)()
		return nil, ctx.Err()
	}
}

func (m *Manager) acquire(keys []string, write bool) func() {
	unlock, _ := m.acquireContext(context.Background(), keys, write)
	return unlock
}

// RLock 获取读锁，返回解锁函数。用于下载、列表、文件信息。
func (m *Manager) RLock(key string) func() {
	return m.acquire([]string{key}, false)
}

// RLockContext 获取读锁并支持通过 ctx 取消等待。
func (m *Manager) RLockContext(ctx context.Context, key string) (func(), error) {
	return m.acquireContext(ctx, []string{key}, false)
}

// RLockMany 原子获取多个路径读锁，避免批量读取逐个加锁时与等待中的写锁交叉等待。
func (m *Manager) RLockMany(keys ...string) func() {
	return m.acquire(keys, false)
}

// RLockManyContext 原子获取多个路径读锁并支持通过 ctx 取消等待。
func (m *Manager) RLockManyContext(ctx context.Context, keys ...string) (func(), error) {
	return m.acquireContext(ctx, keys, false)
}

// Lock 获取写锁，返回解锁函数。用于上传、删除、重命名、移动、创建目录。
func (m *Manager) Lock(key string) func() {
	return m.acquire([]string{key}, true)
}

// LockContext 获取写锁并支持通过 ctx 取消等待。
func (m *Manager) LockContext(ctx context.Context, key string) (func(), error) {
	return m.acquireContext(ctx, []string{key}, true)
}

// LockPair 原子获取两个写锁；路径范围由 Manager 统一判断，避免祖先/后代
// key 不完全相等时发生交叉等待。
func (m *Manager) LockPair(key1, key2 string) func() {
	return m.acquire([]string{key1, key2}, true)
}

// LockPairContext 原子获取两个写锁并支持通过 ctx 取消等待。
func (m *Manager) LockPairContext(ctx context.Context, key1, key2 string) (func(), error) {
	return m.acquireContext(ctx, []string{key1, key2}, true)
}
