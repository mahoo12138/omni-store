package files

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/omni-store/omnistore/internal/models"
	"github.com/omni-store/omnistore/internal/security"
)

func (s *Service) bumpUsageEpoch(storageSourceID int64) {
	s.usageMu.Lock()
	if s.usageEpochs == nil {
		s.usageEpochs = make(map[int64]uint64)
	}
	s.usageEpochs[storageSourceID]++
	s.usageMu.Unlock()
}

func (s *Service) currentUsageEpoch(storageSourceID int64) uint64 {
	s.usageMu.Lock()
	epoch := s.usageEpochs[storageSourceID]
	s.usageMu.Unlock()
	return epoch
}

// scanPhysicalUsage 全量遍历存储源根目录下所有普通文件（不跟随 symlink），
// 包括排除规则文件、.omnistore/ 托管文件与宿主机写入文件，保持物理配额契约。
func scanPhysicalUsage(rootPath string) (int64, error) {
	root, err := security.ResolveInSource(rootPath, "")
	if err != nil {
		return 0, err
	}
	var usage int64
	err = filepath.WalkDir(root, func(absPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if absPath == root {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if info.Mode().IsRegular() {
			usage += info.Size()
		}
		return nil
	})
	return usage, err
}

// physicalSubtreeBytes 计算待删除文件或子树占用的普通文件物理字节数，不跟随 symlink。
func physicalSubtreeBytes(absPath string) int64 {
	info, err := os.Lstat(absPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return 0
	}
	if info.Mode().IsRegular() {
		return info.Size()
	}
	if !info.IsDir() {
		return 0
	}
	var total int64
	_ = filepath.WalkDir(absPath, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		st, err := entry.Info()
		if err != nil || st.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if st.Mode().IsRegular() {
			total += st.Size()
		}
		return nil
	})
	return total
}

// StorageUsage 返回存储源物理用量。优先走 source_usage 增量表（O(1)），
// 首次访问或失效后按需执行一次物理扫描并持久化，避免每次上传全量 WalkDir。
func (s *Service) StorageUsage(src *models.StorageSource) (int64, error) {
	var usage int64
	err := s.readDB().QueryRow(`SELECT usage_bytes FROM source_usage WHERE storage_source_id = ?`, src.ID).Scan(&usage)
	if err == nil {
		return usage, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	scanned, err := scanPhysicalUsage(src.RootPath)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	// 若并发写者在此期间已完成校准与增量更新，则保留更新后的行不回退。
	_, _ = s.db.Exec(`INSERT INTO source_usage (storage_source_id, usage_bytes, calibrated_at)
  VALUES (?, ?, ?) ON CONFLICT(storage_source_id) DO NOTHING`, src.ID, scanned, now)
	var persisted int64
	if err := s.readDB().QueryRow(`SELECT usage_bytes FROM source_usage WHERE storage_source_id = ?`, src.ID).Scan(&persisted); err == nil {
		return persisted, nil
	}
	return scanned, nil
}

// ensureSourceUsageCalibrated 在冷启动（source_usage 尚无行）且进入无锁临时文件写入之前
// 完成一次串行初始化，防止并发上传的 Phase 1 临时文件被首次校准重复计入。
func (s *Service) ensureSourceUsageCalibrated(src *models.StorageSource) (int64, error) {
	var usage int64
	err := s.readDB().QueryRow(`SELECT usage_bytes FROM source_usage WHERE storage_source_id = ?`, src.ID).Scan(&usage)
	if err == nil {
		return usage, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	release := s.locks.Lock(quotaLockKey(src.Key))
	defer release()
	err = s.readDB().QueryRow(`SELECT usage_bytes FROM source_usage WHERE storage_source_id = ?`, src.ID).Scan(&usage)
	if err == nil {
		return usage, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	scanned, err := scanPhysicalUsage(src.RootPath)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	if _, err := s.db.Exec(`INSERT INTO source_usage (storage_source_id, usage_bytes, calibrated_at)
  VALUES (?, ?, ?) ON CONFLICT(storage_source_id) DO UPDATE SET usage_bytes = excluded.usage_bytes, calibrated_at = excluded.calibrated_at`,
		src.ID, scanned, now); err != nil {
		return 0, err
	}
	return scanned, nil
}

// CalibrateStorageUsage 显式扫描源目录并校准 source_usage（供 Reconcile 与后台定时校准调用）。
// 扫描阶段不持有配额写锁；仅在扫描期间无并发写入发生时覆盖缓存值，避免阻塞上传或覆盖增量。
func (s *Service) CalibrateStorageUsage(src *models.StorageSource) (int64, error) {
	epochBefore := s.currentUsageEpoch(src.ID)
	scanned, err := scanPhysicalUsage(src.RootPath)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	release := s.locks.Lock(quotaLockKey(src.Key))
	defer release()
	if s.currentUsageEpoch(src.ID) == epochBefore {
		if _, err := s.db.Exec(`INSERT INTO source_usage (storage_source_id, usage_bytes, calibrated_at)
  VALUES (?, ?, ?) ON CONFLICT(storage_source_id) DO UPDATE SET
    usage_bytes = excluded.usage_bytes,
    calibrated_at = excluded.calibrated_at`, src.ID, scanned, now); err != nil {
			return 0, err
		}
		return scanned, nil
	}
	// 扫描期间发生了服务内写入：保留已精确增量更新的记录，仅当尚无记录时写入兜底。
	_, _ = s.db.Exec(`INSERT INTO source_usage (storage_source_id, usage_bytes, calibrated_at)
  VALUES (?, ?, ?) ON CONFLICT(storage_source_id) DO NOTHING`, src.ID, scanned, now)
	var current int64
	if err := s.readDB().QueryRow(`SELECT usage_bytes FROM source_usage WHERE storage_source_id = ?`, src.ID).Scan(&current); err == nil {
		return current, nil
	}
	return scanned, nil
}

// CalibrateAllSources 后台定期校准全部未禁用存储源的物理用量，收敛外部直接写盘带来的偏差。
func (s *Service) CalibrateAllSources() error {
	return s.CalibrateAllSourcesContext(context.Background())
}

// CalibrateAllSourcesContext 支持取消信号的后台全源物理用量校准。
func (s *Service) CalibrateAllSourcesContext(ctx context.Context) error {
	list, err := s.sources.List()
	if err != nil {
		return err
	}
	var firstErr error
	for _, src := range list {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err := s.CalibrateStorageUsage(src); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// AdjustSourceUsageTx 在写事务内同步更新存储源增量用量；仅在已有校准行时更新。
func (s *Service) AdjustSourceUsageTx(tx *sql.Tx, storageSourceID, delta int64) error {
	s.bumpUsageEpoch(storageSourceID)
	if delta == 0 {
		return nil
	}
	_, err := tx.Exec(`UPDATE source_usage SET usage_bytes = MAX(0, usage_bytes + ?) WHERE storage_source_id = ?`,
		delta, storageSourceID)
	return err
}

// AdjustSourceUsage 非事务场景下同步更新存储源增量用量。
func (s *Service) AdjustSourceUsage(storageSourceID, delta int64) error {
	s.bumpUsageEpoch(storageSourceID)
	if delta == 0 {
		return nil
	}
	_, err := s.db.Exec(`UPDATE source_usage SET usage_bytes = MAX(0, usage_bytes + ?) WHERE storage_source_id = ?`,
		delta, storageSourceID)
	return err
}

// InvalidateSourceUsage 使指定存储源的用量缓存失效（用于崩溃恢复或根目录重绑后按需重新校准）。
func (s *Service) InvalidateSourceUsage(storageSourceID int64) {
	s.bumpUsageEpoch(storageSourceID)
	_, _ = s.db.Exec(`DELETE FROM source_usage WHERE storage_source_id = ?`, storageSourceID)
}

// OptimisticQuotaBound 无锁计算一次上传的乐观字节上限（不持有配额写锁与路径写锁），
// 用于两阶段上传的 Phase 1 流式写入限流；Phase 2 提交临界区会持锁复核精确配额。
func (s *Service) OptimisticQuotaBound(src *models.StorageSource, replacingRelPath string, ownerUserID *int64) (maxBytes int64, limited bool, err error) {
	current, err := s.sources.Get(src.Key)
	if err != nil {
		return 0, false, err
	}
	if current.QuotaBytes > 0 {
		usage, err := s.ensureSourceUsageCalibrated(current)
		if err != nil {
			return 0, false, err
		}
		var replacedBytes int64
		if replacingRelPath != "" {
			_, absPath, prepErr := s.prepare(current, replacingRelPath)
			if prepErr == nil {
				if info, statErr := os.Lstat(absPath); statErr == nil && info.Mode().IsRegular() {
					replacedBytes = info.Size()
				}
			}
		}
		available := current.QuotaBytes - usage
		if available < 0 {
			available = 0
		}
		maxBytes = available + replacedBytes
		limited = true
	}
	if ownerUserID == nil {
		return maxBytes, limited, nil
	}
	var quotaBytes int64
	if err := s.readDB().QueryRow(`SELECT quota_bytes FROM users WHERE id = ?`, *ownerUserID).Scan(&quotaBytes); err != nil {
		return 0, false, err
	}
	if quotaBytes == 0 {
		return maxBytes, limited, nil
	}
	userUsage, err := s.UserUsage(*ownerUserID)
	if err != nil {
		return 0, false, err
	}
	var userReplaced int64
	if replacingRelPath != "" {
		if relPath, normErr := security.NormalizeRelPath(replacingRelPath); normErr == nil {
			_ = s.readDB().QueryRow(`SELECT size FROM file_records
  WHERE storage_source_id = ? AND relative_path = ? AND owner_user_id = ? AND owner_type = ? AND record_status = ?`,
				src.ID, relPath, *ownerUserID, models.FileOwnerUser, models.FileRecordActive).Scan(&userReplaced)
		}
	}
	userAvail := quotaBytes - userUsage
	if userAvail < 0 {
		userAvail = 0
	}
	userMax := userAvail + userReplaced
	if !limited || userMax < maxBytes {
		maxBytes = userMax
	}
	return maxBytes, true, nil
}
