// Package app 负责全量领域服务的依赖装配、统一崩溃恢复编排与受控生命周期后台任务管理。
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/omni-store/omnistore/internal/audit"
	"github.com/omni-store/omnistore/internal/auth"
	"github.com/omni-store/omnistore/internal/buildinfo"
	"github.com/omni-store/omnistore/internal/capabilities"
	"github.com/omni-store/omnistore/internal/config"
	"github.com/omni-store/omnistore/internal/db"
	"github.com/omni-store/omnistore/internal/files"
	httpserver "github.com/omni-store/omnistore/internal/http"
	"github.com/omni-store/omnistore/internal/imagebed"
	"github.com/omni-store/omnistore/internal/lifecycle"
	"github.com/omni-store/omnistore/internal/locks"
	"github.com/omni-store/omnistore/internal/oidc"
	"github.com/omni-store/omnistore/internal/operations"
	"github.com/omni-store/omnistore/internal/publicdisk"
	"github.com/omni-store/omnistore/internal/s3api"
	"github.com/omni-store/omnistore/internal/security"
	"github.com/omni-store/omnistore/internal/shares"
	"github.com/omni-store/omnistore/internal/sources"
	"github.com/omni-store/omnistore/internal/staticassets"
	"github.com/omni-store/omnistore/internal/users"
)

// RecoverySummary 描述单个崩溃恢复子系统的启动恢复结果。
type RecoverySummary struct {
	Name       string
	Completed  int
	RolledBack int
	Details    map[string]int
}

// Recoverer 定义统一的启动崩溃恢复契约。
type Recoverer interface {
	Name() string
	Recover() (RecoverySummary, error)
}

type recovererFunc struct {
	name string
	fn   func() (RecoverySummary, error)
}

func (r recovererFunc) Name() string                      { return r.name }
func (r recovererFunc) Recover() (RecoverySummary, error) { return r.fn() }

// App 持有完整的应用实例依赖图与生命周期句柄。
type App struct {
	cfg        *config.Config
	pool       *db.Pool
	logger     *slog.Logger
	lifecycle  *lifecycle.Manager
	sources    *sources.Service
	files      *files.Service
	sessions   *auth.Sessions
	imagebed   *imagebed.Service
	s3MP       *s3api.MultipartStore
	httpServer *http.Server
	server     *httpserver.Server
	recoverers []Recoverer
}

// New 装配所有领域服务与 HTTP 路由，注入读写分离数据库连接池和实例级生命周期管理器。
func New(cfg *config.Config, pool *db.Pool, logger *slog.Logger) (*App, error) {
	if cfg == nil {
		return nil, fmt.Errorf("配置不能为空")
	}
	if pool == nil {
		return nil, fmt.Errorf("数据库连接池不能为空")
	}
	if logger == nil {
		logger = slog.Default()
	}

	writeDB := pool.Write()
	readDB := pool.Read()
	life := lifecycle.NewManager()

	userSvc := users.NewService(writeDB)
	userSvc.SetReadDB(readDB)

	sessSvc := auth.NewSessions(writeDB, time.Duration(cfg.Security.SessionTTLHours)*time.Hour)
	sessSvc.SetReadDB(readDB)

	tokenSvc := auth.NewTokens(writeDB)
	tokenSvc.SetReadDB(readDB)

	auditLogger := audit.New(writeDB, cfg.Audit.Enabled, cfg.Audit.MaxEntries, logger)
	auditLogger.SetReadDB(readDB)

	proxyResolver := security.NewProxyResolver(cfg.Server.TrustedProxies)

	loginLimit := cfg.Security.LoginRateLimit
	if !loginLimit.Enabled {
		loginLimit.MaxFailuresPerIP = 0
		loginLimit.MaxFailuresPerUsername = 0
	}
	loginLimiter := auth.NewLoginLimiter(
		time.Duration(loginLimit.WindowMinutes)*time.Minute,
		loginLimit.MaxFailuresPerIP,
		loginLimit.MaxFailuresPerUsername,
	)

	srcSvc := sources.NewService(writeDB, cfg.Data.Dir)
	srcSvc.SetReadDB(readDB)

	fileSvc := files.NewService(writeDB, srcSvc, locks.NewManager())
	fileSvc.SetReadDB(readDB)
	fileSvc.SetLifecycle(life)

	capSvc := capabilities.NewService(writeDB, srcSvc)
	staticSvc := staticassets.NewService(writeDB, srcSvc, fileSvc, cfg.Server.PublicURL)
	pubSvc := publicdisk.NewService(fileSvc, capSvc)

	shareSvc := shares.NewService(writeDB, srcSvc, fileSvc, cfg.Server.PublicURL)
	shareSvc.SetReadDB(readDB)

	s3Keys := s3api.NewCredentials(writeDB, cfg.Data.Dir, cfg.Security.MasterKey)
	s3Keys.SetReadDB(readDB)

	s3MP := s3api.NewMultipartStore(writeDB, cfg.Data.Dir, fileSvc, cfg.Upload.MaxFileSizeMB)
	s3MP.SetLifecycle(life)

	ibSvc, err := imagebed.NewService(writeDB, cfg.Server.PublicURL,
		filepath.Join(cfg.Data.Dir, "cache", "thumbnails"), srcSvc, capSvc, fileSvc)
	if err != nil {
		_ = srcSvc.Close()
		return nil, fmt.Errorf("初始化图床服务失败: %w", err)
	}
	ibSvc.SetLifecycle(life)

	anonLimiter := imagebed.NewRateLimiter(cfg.ImageBed.AnonymousRateLimit.PerIPPerHour)
	opsSvc := operations.NewService(writeDB, cfg.DatabasePath(), cfg.Data.Dir, buildinfo.Version,
		srcSvc, fileSvc, ibSvc, sessSvc, httpserver.MultipartCleanupAdapter(s3MP))
	oidcSettings := oidc.NewSettings(writeDB)

	httpSrv, server := httpserver.NewWithDeps(httpserver.Deps{
		Config:       cfg,
		DB:           writeDB,
		Logger:       logger,
		Users:        userSvc,
		Sources:      srcSvc,
		Files:        fileSvc,
		Public:       pubSvc,
		Shares:       shareSvc,
		Sessions:     sessSvc,
		LoginLimiter: loginLimiter,
		Tokens:       tokenSvc,
		ImageBed:     ibSvc,
		AnonLimiter:  anonLimiter,
		Capabilities: capSvc,
		StaticAssets: staticSvc,
		Operations:   opsSvc,
		Audit:        auditLogger,
		Proxy:        proxyResolver,
		OIDCSettings: oidcSettings,
		S3Keys:       s3Keys,
		S3Multipart:  s3MP,
		Lifecycle:    life,
	})

	a := &App{
		cfg:        cfg,
		pool:       pool,
		logger:     logger,
		lifecycle:  life,
		sources:    srcSvc,
		files:      fileSvc,
		sessions:   sessSvc,
		imagebed:   ibSvc,
		s3MP:       s3MP,
		httpServer: httpSrv,
		server:     server,
	}
	a.recoverers = a.buildRecoverers()
	return a, nil
}

func (a *App) buildRecoverers() []Recoverer {
	return []Recoverer{
		recovererFunc{
			name: "移动或永久删除",
			fn: func() (RecoverySummary, error) {
				r, err := a.files.RecoverPathOperations()
				return RecoverySummary{
					Name:       "移动或永久删除",
					Completed:  r.CompletedMoves + r.CompletedDeletes,
					RolledBack: r.RolledBackMoves,
					Details: map[string]int{
						"completed_moves":   r.CompletedMoves,
						"rolled_back_moves": r.RolledBackMoves,
						"completed_deletes": r.CompletedDeletes,
					},
				}, err
			},
		},
		recovererFunc{
			name: "跨来源复制",
			fn: func() (RecoverySummary, error) {
				r, err := a.files.RecoverCopyOperations()
				return RecoverySummary{
					Name:       "跨来源复制",
					Completed:  r.CompletedCopies,
					RolledBack: r.RolledBackCopies,
					Details: map[string]int{
						"completed_copies":   r.CompletedCopies,
						"rolled_back_copies": r.RolledBackCopies,
					},
				}, err
			},
		},
		recovererFunc{
			name: "跨来源移动",
			fn: func() (RecoverySummary, error) {
				r, err := a.files.RecoverTransferOperations()
				return RecoverySummary{
					Name:       "跨来源移动",
					Completed:  r.CompletedMoves,
					RolledBack: r.RolledBackMoves,
					Details: map[string]int{
						"completed_moves":   r.CompletedMoves,
						"rolled_back_moves": r.RolledBackMoves,
					},
				}, err
			},
		},
		recovererFunc{
			name: "回收站操作",
			fn: func() (RecoverySummary, error) {
				r, err := a.files.RecoverTrashOperations()
				return RecoverySummary{
					Name:       "回收站操作",
					Completed:  r.CompletedMoves + r.CompletedRestores + r.CompletedPurges,
					RolledBack: r.RolledBackMoves + r.RolledBackRestores,
					Details: map[string]int{
						"completed_moves":      r.CompletedMoves,
						"rolled_back_moves":    r.RolledBackMoves,
						"completed_restores":   r.CompletedRestores,
						"rolled_back_restores": r.RolledBackRestores,
						"completed_purges":     r.CompletedPurges,
					},
				}, err
			},
		},
		recovererFunc{
			name: "普通文件上传",
			fn: func() (RecoverySummary, error) {
				r, err := a.files.RecoverFileUploadOperations()
				return RecoverySummary{
					Name:       "普通文件上传",
					Completed:  r.CompletedUploads,
					RolledBack: r.RolledBackUploads,
					Details: map[string]int{
						"completed_uploads":   r.CompletedUploads,
						"rolled_back_uploads": r.RolledBackUploads,
					},
				}, err
			},
		},
		recovererFunc{
			name: "S3 Multipart 分片上传",
			fn: func() (RecoverySummary, error) {
				r, err := a.s3MP.RecoverMultipartPartOperations()
				return RecoverySummary{
					Name:       "S3 Multipart 分片上传",
					Completed:  r.CompletedParts,
					RolledBack: r.RolledBackParts,
					Details: map[string]int{
						"completed_parts":   r.CompletedParts,
						"rolled_back_parts": r.RolledBackParts,
					},
				}, err
			},
		},
		recovererFunc{
			name: "S3 Multipart 完成操作",
			fn: func() (RecoverySummary, error) {
				r, err := a.s3MP.RecoverMultipartCompletions()
				return RecoverySummary{
					Name:       "S3 Multipart 完成操作",
					Completed:  r.CompletedUploads,
					RolledBack: r.RolledBackUploads,
					Details: map[string]int{
						"completed_uploads":   r.CompletedUploads,
						"rolled_back_uploads": r.RolledBackUploads,
					},
				}, err
			},
		},
		recovererFunc{
			name: "图床上传",
			fn: func() (RecoverySummary, error) {
				r, err := a.imagebed.RecoverUploadOperations()
				return RecoverySummary{
					Name:       "图床上传",
					Completed:  r.CompletedUploads,
					RolledBack: r.RolledBackUploads,
					Details: map[string]int{
						"completed_uploads":   r.CompletedUploads,
						"rolled_back_uploads": r.RolledBackUploads,
					},
				}, err
			},
		},
	}
}

// RecoverAll 在对外监听前依次执行全部 8 个崩溃恢复子系统；任一失败即阻止启动。
func (a *App) RecoverAll() error {
	for _, rec := range a.recoverers {
		summary, err := rec.Recover()
		if err != nil {
			return fmt.Errorf("恢复中断的%s失败: %w", rec.Name(), err)
		}
		if summary.Completed+summary.RolledBack > 0 {
			args := []any{
				"subsystem", summary.Name,
				"completed", summary.Completed,
				"rolled_back", summary.RolledBack,
			}
			for k, v := range summary.Details {
				args = append(args, k, v)
			}
			a.logger.Info("已恢复中断操作", args...)
		}
	}
	return nil
}

// StartBackgroundWorkers 启动全部受 context 与 WaitGroup 管控的后台维护任务。
// 调用方在关闭数据库连接前必须取消 ctx 并调用返回的 wait 函数，避免关闭后竞态访问 DB。
func (a *App) StartBackgroundWorkers(ctx context.Context) func() {
	var wg sync.WaitGroup
	runPeriodic := func(interval time.Duration, runImmediately bool, fn func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if runImmediately {
				if ctx.Err() != nil {
					return
				}
				fn(ctx)
			}
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					fn(ctx)
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// 1. 过期 Session 清理（每小时）
	runPeriodic(time.Hour, false, func(context.Context) {
		if n, err := a.sessions.CleanupExpired(); err != nil {
			a.logger.Error("清理过期 session 失败", "err", err)
		} else if n > 0 {
			a.logger.Info("清理过期 session", "count", n)
		}
	})

	// 2. S3 Multipart 过期上传与孤儿分片清理（启动时 + 每小时）
	if a.cfg.Server.S3Enabled {
		runPeriodic(time.Hour, true, func(context.Context) {
			result, err := a.s3MP.CleanupExpired(s3api.MultipartMaxAge)
			if err != nil {
				a.logger.Warn("清理 S3 Multipart 临时数据未完全成功", "err", err,
					"uploads_removed", result.UploadsRemoved, "orphans_removed", result.OrphansRemoved)
				return
			}
			if result.UploadsRemoved > 0 || result.OrphansRemoved > 0 {
				a.logger.Info("清理 S3 Multipart 临时数据",
					"uploads_removed", result.UploadsRemoved, "orphans_removed", result.OrphansRemoved)
			}
		})
	}

	// 3. WebDAV 过期持久锁清理（启动时 + 每小时）
	runPeriodic(time.Hour, true, func(c context.Context) {
		if n, err := a.files.PersistentLocks().CleanupExpired(c); err != nil {
			a.logger.Warn("清理过期 WebDAV 锁失败", "err", err)
		} else if n > 0 {
			a.logger.Info("清理过期 WebDAV 锁", "count", n)
		}
	})

	// 4. 缩略图缓存清理（启动时 + 每天）
	runPeriodic(24*time.Hour, true, func(context.Context) {
		if n, err := a.imagebed.CleanupThumbnailCache(30 * 24 * time.Hour); err != nil {
			a.logger.Warn("清理缩略图缓存失败", "err", err)
		} else if n > 0 {
			a.logger.Info("清理缩略图缓存", "count", n)
		}
	})

	// 5. 存储源物理用量增量缓存后台校准（每 15 分钟，自愈外部磁盘改动导致的漂移）
	runPeriodic(15*time.Minute, false, func(c context.Context) {
		if err := a.files.CalibrateAllSourcesContext(c); err != nil && c.Err() == nil {
			a.logger.Warn("校准存储源物理用量失败", "err", err)
		}
	})

	return wg.Wait
}

// HTTPServer 返回主 HTTP Server。
func (a *App) HTTPServer() *http.Server {
	return a.httpServer
}

// S3Server 返回仅承载 S3 Path-style API 的专用 HTTP Server。
func (a *App) S3Server() *http.Server {
	return a.server.S3Server()
}

// Close 释放应用持有的底层资源（如缓存的存储源 *os.Root 文件描述符）。
func (a *App) Close() error {
	if a.sources != nil {
		return a.sources.Close()
	}
	return nil
}
