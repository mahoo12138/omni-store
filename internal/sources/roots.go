package sources

import (
	"errors"

	"github.com/omni-store/omnistore/internal/models"
	"github.com/omni-store/omnistore/internal/security"
)

type cachedSourceRoot struct {
	rootPath string
	root     *security.SourceRoot
}

// OpenRoot 返回并缓存该存储源的 *security.SourceRoot（底层为 *os.Root），
// 当存储源根路径重绑或被删除时自动失效并关闭旧句柄。
func (s *Service) OpenRoot(src *models.StorageSource) (*security.SourceRoot, error) {
	if src == nil || src.RootPath == "" {
		return nil, ErrNotFound
	}
	if src.ID <= 0 {
		return security.OpenSourceRoot(src.RootPath)
	}
	s.rootsMu.RLock()
	if s.roots != nil {
		if cached, ok := s.roots[src.ID]; ok && cached.rootPath == src.RootPath {
			s.rootsMu.RUnlock()
			return cached.root, nil
		}
	}
	s.rootsMu.RUnlock()

	s.rootsMu.Lock()
	defer s.rootsMu.Unlock()
	if s.roots == nil {
		s.roots = make(map[int64]*cachedSourceRoot)
	}
	if cached, ok := s.roots[src.ID]; ok {
		if cached.rootPath == src.RootPath {
			return cached.root, nil
		}
		_ = cached.root.Close()
		delete(s.roots, src.ID)
	}
	opened, err := security.OpenSourceRoot(src.RootPath)
	if err != nil {
		return nil, err
	}
	s.roots[src.ID] = &cachedSourceRoot{rootPath: src.RootPath, root: opened}
	return opened, nil
}

// InvalidateRoot 关闭并移除指定存储源的缓存 *os.Root 句柄。
func (s *Service) InvalidateRoot(sourceID int64) {
	s.rootsMu.Lock()
	if cached, ok := s.roots[sourceID]; ok {
		_ = cached.root.Close()
		delete(s.roots, sourceID)
	}
	s.rootsMu.Unlock()
}

// Close 关闭全部已缓存的 *os.Root 句柄（用于优雅停机）。
func (s *Service) Close() error {
	s.rootsMu.Lock()
	defer s.rootsMu.Unlock()
	var err error
	for id, cached := range s.roots {
		err = errors.Join(err, cached.root.Close())
		delete(s.roots, id)
	}
	return err
}
