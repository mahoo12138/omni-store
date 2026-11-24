package files

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/omni-store/omnistore/internal/security"
)

const (
	maxRecentFilesPerUser = 500
	maxFavoritesPerUser   = 500
	recentWriteThrottle   = time.Minute
)

// ErrFavoriteLimit 表示单用户收藏数已达上限（500 项）。
var ErrFavoriteLimit = errors.New("收藏数量已达上限（500 项）")

// RecentFileRecord 是 recent_files 表的只读查询行。
type RecentFileRecord struct {
	ID           int64
	SourceID     int64
	SourceKey    string
	SourceName   string
	RelativePath string
	AccessedAt   string
}

// FavoriteRecord 是 favorites 表的只读查询行。
type FavoriteRecord struct {
	ID           int64
	SourceID     int64
	SourceKey    string
	SourceName   string
	RelativePath string
	CreatedAt    string
}

func (s *Service) shouldRecordRecent(userID, sourceID int64, relPath string, now time.Time) bool {
	key := fmt.Sprintf("%d:%d:%s", userID, sourceID, relPath)
	s.recentMu.Lock()
	defer s.recentMu.Unlock()
	if s.recentCache == nil {
		s.recentCache = make(map[string]time.Time)
	}
	if prev, ok := s.recentCache[key]; ok && now.Sub(prev) < recentWriteThrottle {
		return false
	}
	if len(s.recentCache) >= 4096 {
		clear(s.recentCache)
	}
	s.recentCache[key] = now
	return true
}

func (s *Service) clearRecentCache() {
	s.recentMu.Lock()
	clear(s.recentCache)
	s.recentMu.Unlock()
}

// RecordRecentFile 记录用户最近访问/操作的文件；同一文件 60s 内节流去重，避免下载热路径反复写库。
func (s *Service) RecordRecentFile(userID, storageSourceID int64, relPath string) error {
	normalized, err := security.NormalizeRelPath(relPath)
	if err != nil || normalized == "" {
		return nil
	}
	now := time.Now().UTC()
	if !s.shouldRecordRecent(userID, storageSourceID, normalized, now) {
		return nil
	}
	return s.recordRecentFilesTx(userID, storageSourceID, []string{normalized}, now)
}

// RecordRecentFilesBatch 在单个 SQLite 事务中批量写入多条最近文件（如批量归档下载），
// 避免循环中产生 O(N) 次独立写库。
func (s *Service) RecordRecentFilesBatch(userID, storageSourceID int64, relPaths []string) error {
	now := time.Now().UTC()
	normalized := make([]string, 0, len(relPaths))
	for _, p := range relPaths {
		rel, err := security.NormalizeRelPath(p)
		if err != nil || rel == "" {
			continue
		}
		if s.shouldRecordRecent(userID, storageSourceID, rel, now) {
			normalized = append(normalized, rel)
		}
	}
	if len(normalized) == 0 {
		return nil
	}
	return s.recordRecentFilesTx(userID, storageSourceID, normalized, now)
}

func (s *Service) recordRecentFilesTx(userID, storageSourceID int64, relPaths []string, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, rel := range relPaths {
		if _, err := tx.Exec(`INSERT INTO recent_files (user_id, storage_source_id, relative_path, accessed_at)
  VALUES (?, ?, ?, ?)
  ON CONFLICT(user_id, storage_source_id, relative_path) DO UPDATE SET accessed_at = excluded.accessed_at`,
			userID, storageSourceID, rel, now); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM recent_files WHERE user_id = ? AND id NOT IN (
  SELECT id FROM recent_files WHERE user_id = ? ORDER BY accessed_at DESC, id DESC LIMIT ?
)`, userID, userID, maxRecentFilesPerUser); err != nil {
		return err
	}
	return tx.Commit()
}

// ListRecentFiles 从只读连接池查询用户最近文件候选列表。
func (s *Service) ListRecentFiles(userID int64) ([]RecentFileRecord, error) {
	rows, err := s.readDB().Query(`SELECT rf.id, rf.storage_source_id, s.key, s.name, rf.relative_path, rf.accessed_at
  FROM recent_files rf JOIN storage_sources s ON s.id = rf.storage_source_id
  WHERE rf.user_id = ? ORDER BY rf.accessed_at DESC, rf.id DESC LIMIT ?`, userID, maxRecentFilesPerUser)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RecentFileRecord, 0, 64)
	for rows.Next() {
		var item RecentFileRecord
		if err := rows.Scan(&item.ID, &item.SourceID, &item.SourceKey, &item.SourceName, &item.RelativePath, &item.AccessedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ListFavorites 从只读连接池查询用户收藏候选列表。
func (s *Service) ListFavorites(userID int64) ([]FavoriteRecord, error) {
	rows, err := s.readDB().Query(`SELECT f.id, f.storage_source_id, s.key, s.name, f.relative_path, f.created_at
  FROM favorites f JOIN storage_sources s ON s.id = f.storage_source_id
  WHERE f.user_id = ? ORDER BY f.created_at DESC, f.id DESC LIMIT ?`, userID, maxFavoritesPerUser)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]FavoriteRecord, 0)
	for rows.Next() {
		var item FavoriteRecord
		if err := rows.Scan(&item.ID, &item.SourceID, &item.SourceKey, &item.SourceName, &item.RelativePath, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// AddFavorite 幂等添加收藏，校验 500 项上限并返回收藏 ID。
func (s *Service) AddFavorite(userID, storageSourceID int64, relativePath string) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRow(`SELECT id FROM favorites WHERE user_id = ? AND storage_source_id = ? AND relative_path = ?`,
		userID, storageSourceID, relativePath).Scan(&id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM favorites WHERE user_id = ?`, userID).Scan(&count); err != nil {
			return 0, err
		}
		if count >= maxFavoritesPerUser {
			return 0, ErrFavoriteLimit
		}
		res, err := tx.Exec(`INSERT INTO favorites (user_id, storage_source_id, relative_path, created_at)
  VALUES (?, ?, ?, ?)`, userID, storageSourceID, relativePath, time.Now().UTC())
		if err != nil {
			return 0, err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// DeleteFavorite 删除指定用户的某条收藏记录；返回是否实际删除了行。
func (s *Service) DeleteFavorite(userID, favoriteID int64) (bool, error) {
	result, err := s.db.Exec(`DELETE FROM favorites WHERE id = ? AND user_id = ?`, favoriteID, userID)
	if err != nil {
		return false, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return deleted > 0, nil
}

// rebaseUserPathsTx 在移动/重命名/恢复的同一 SQLite 事务中原子迁移所有受影响用户的
// recent_files 与 favorites 路径，避免崩溃后收藏或最近文件仍指向旧路径（同时覆盖 WebDAV MOVE 与恢复重放）。
func (s *Service) rebaseUserPathsTx(tx *sql.Tx, sourceID int64, oldPath string, targetSourceID int64, newPath string) error {
	oldPath, err := security.NormalizeRelPath(oldPath)
	if err != nil || oldPath == "" {
		return nil
	}
	newPath, err = security.NormalizeRelPath(newPath)
	if err != nil || newPath == "" {
		return nil
	}
	if sourceID == targetSourceID && oldPath == newPath {
		return nil
	}
	s.clearRecentCache()

	// 1. 迁移 recent_files（覆盖全部用户受影响的条目）。
	type recentRow struct {
		id         int64
		userID     int64
		path       string
		accessedAt string
	}
	recentRows, err := tx.Query(`SELECT id, user_id, relative_path, accessed_at FROM recent_files
  WHERE storage_source_id = ? AND `+relativePathSubtreeSQL,
		appendRelativePathSubtreeArgs([]any{sourceID}, oldPath)...)
	if err != nil {
		return err
	}
	var recents []recentRow
	for recentRows.Next() {
		var item recentRow
		if err := recentRows.Scan(&item.id, &item.userID, &item.path, &item.accessedAt); err != nil {
			_ = recentRows.Close()
			return err
		}
		recents = append(recents, item)
	}
	if err := recentRows.Err(); err != nil {
		_ = recentRows.Close()
		return err
	}
	if err := recentRows.Close(); err != nil {
		return err
	}
	for _, item := range recents {
		targetPath := newPath + strings.TrimPrefix(item.path, oldPath)
		if targetSourceID == sourceID && targetPath == item.path {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO recent_files (user_id, storage_source_id, relative_path, accessed_at)
  VALUES (?, ?, ?, ?)
  ON CONFLICT(user_id, storage_source_id, relative_path) DO UPDATE SET accessed_at =
    CASE WHEN recent_files.accessed_at < excluded.accessed_at THEN excluded.accessed_at ELSE recent_files.accessed_at END`,
			item.userID, targetSourceID, targetPath, item.accessedAt); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM recent_files WHERE id = ?`, item.id); err != nil {
			return err
		}
	}

	// 2. 迁移 favorites（覆盖全部用户受影响的收藏）。
	type favoriteRow struct {
		id        int64
		userID    int64
		path      string
		createdAt string
	}
	favRows, err := tx.Query(`SELECT id, user_id, relative_path, created_at FROM favorites
  WHERE storage_source_id = ? AND `+relativePathSubtreeSQL,
		appendRelativePathSubtreeArgs([]any{sourceID}, oldPath)...)
	if err != nil {
		return err
	}
	var favs []favoriteRow
	for favRows.Next() {
		var item favoriteRow
		if err := favRows.Scan(&item.id, &item.userID, &item.path, &item.createdAt); err != nil {
			_ = favRows.Close()
			return err
		}
		favs = append(favs, item)
	}
	if err := favRows.Err(); err != nil {
		_ = favRows.Close()
		return err
	}
	if err := favRows.Close(); err != nil {
		return err
	}
	for _, item := range favs {
		targetPath := newPath + strings.TrimPrefix(item.path, oldPath)
		if targetSourceID == sourceID && targetPath == item.path {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO favorites (user_id, storage_source_id, relative_path, created_at)
  VALUES (?, ?, ?, ?)
  ON CONFLICT(user_id, storage_source_id, relative_path) DO UPDATE SET created_at =
    CASE WHEN favorites.created_at > excluded.created_at THEN excluded.created_at ELSE favorites.created_at END`,
			item.userID, targetSourceID, targetPath, item.createdAt); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM favorites WHERE id = ?`, item.id); err != nil {
			return err
		}
	}
	return nil
}
