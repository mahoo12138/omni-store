// Package lifecycle coordinates resource deletion with in-flight operations.
package lifecycle

import (
	"sort"
	"sync"
)

type key struct {
	kind uint8
	id   int64
}

const (
	sourceKind uint8 = iota + 1
	userKind
)

// Key identifies a lifecycle-managed resource.
type Key struct{ value key }

// Source identifies a storage source lifecycle.
func Source(id int64) Key { return Key{value: key{kind: sourceKind, id: id}} }

// User identifies a user lifecycle.
func User(id int64) Key { return Key{value: key{kind: userKind, id: id}} }

type entry struct {
	mu   sync.RWMutex
	refs int
}

// Manager coordinates lifecycle read/write leases for an application instance.
// Injecting a per-app Manager prevents tests and independent server instances
// from sharing package-level global lock state.
type Manager struct {
	mu      sync.Mutex
	entries map[key]*entry
}

// NewManager creates an isolated lifecycle manager.
func NewManager() *Manager {
	return &Manager{entries: make(map[key]*entry)}
}

var defaultManager = NewManager()

// Default returns the package-level fallback manager used when callers do not
// inject an explicit *Manager.
func Default() *Manager {
	return defaultManager
}

// Read prevents the identified resources from being deleted until release.
func Read(keys ...Key) func() { return defaultManager.Read(keys...) }

// Write waits for all in-flight operations on the resources and prevents new
// ones from starting until release. Deletion checks and deletion must share one
// Write lease.
func Write(keys ...Key) func() { return defaultManager.Write(keys...) }

// Read prevents the identified resources from being deleted until release.
func (m *Manager) Read(keys ...Key) func() {
	if m == nil {
		m = defaultManager
	}
	return m.acquire(false, keys)
}

// Write waits for all in-flight operations on the resources and prevents new
// ones from starting until release.
func (m *Manager) Write(keys ...Key) func() {
	if m == nil {
		m = defaultManager
	}
	return m.acquire(true, keys)
}

func (m *Manager) acquire(write bool, requested []Key) func() {
	keys := normalizedKeys(requested)
	entries := make([]*entry, len(keys))
	m.mu.Lock()
	if m.entries == nil {
		m.entries = make(map[key]*entry)
	}
	for i, item := range keys {
		current := m.entries[item]
		if current == nil {
			current = &entry{}
			m.entries[item] = current
		}
		current.refs++
		entries[i] = current
	}
	m.mu.Unlock()

	for _, current := range entries {
		if write {
			current.mu.Lock()
		} else {
			current.mu.RLock()
		}
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			for i := len(entries) - 1; i >= 0; i-- {
				if write {
					entries[i].mu.Unlock()
				} else {
					entries[i].mu.RUnlock()
				}
			}
			m.mu.Lock()
			for i, item := range keys {
				entries[i].refs--
				if entries[i].refs == 0 {
					delete(m.entries, item)
				}
			}
			m.mu.Unlock()
		})
	}
}

func normalizedKeys(requested []Key) []key {
	seen := make(map[key]struct{}, len(requested))
	keys := make([]key, 0, len(requested))
	for _, item := range requested {
		if item.value.kind == 0 || item.value.id <= 0 {
			continue
		}
		if _, exists := seen[item.value]; exists {
			continue
		}
		seen[item.value] = struct{}{}
		keys = append(keys, item.value)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].kind != keys[j].kind {
			return keys[i].kind < keys[j].kind
		}
		return keys[i].id < keys[j].id
	})
	return keys
}
