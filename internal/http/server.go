package httpserver

import (
	"database/sql"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/omni-store/omnistore/internal/audit"
	"github.com/omni-store/omnistore/internal/auth"
	"github.com/omni-store/omnistore/internal/buildinfo"
	"github.com/omni-store/omnistore/internal/capabilities"
	"github.com/omni-store/omnistore/internal/config"
	"github.com/omni-store/omnistore/internal/files"
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
	"github.com/omni-store/omnistore/internal/webdav"
	"github.com/omni-store/omnistore/web"
)

// Deps 汇总 HTTP 路由层所需的全部已装配领域服务，由 internal/app 构造并注入。
type Deps struct {
	Config         *config.Config
	DB             *sql.DB
	Logger         *slog.Logger
	Users          *users.Service
	Sources        *sources.Service
	Files          *files.Service
	Public         *publicdisk.Service
	Shares         *shares.Service
	Sessions       *auth.Sessions
	LoginLimiter   *auth.LoginLimiter
	Tokens         *auth.Tokens
	ImageBed       *imagebed.Service
	AnonLimiter    *imagebed.RateLimiter
	Capabilities   *capabilities.Service
	StaticAssets   *staticassets.Service
	Operations     *operations.Service
	Audit          *audit.Logger
	Proxy          *security.ProxyResolver
	OIDCSettings   *oidc.Settings
	S3Keys         *s3api.Credentials
	S3Multipart    *s3api.MultipartStore
	Lifecycle      *lifecycle.Manager
	BootstrapToken string
}

// Server 聚合 HTTP 层依赖。
type Server struct {
	cfg             *config.Config
	db              *sql.DB
	logger          *slog.Logger
	users           *users.Service
	sources         *sources.Service
	files           *files.Service
	public          *publicdisk.Service
	shares          *shares.Service
	sessions        *auth.Sessions
	loginLimiter    *auth.LoginLimiter
	tokens          *auth.Tokens
	imagebed        *imagebed.Service
	anonLimiter     *imagebed.RateLimiter
	capabilities    *capabilities.Service
	staticassets    *staticassets.Service
	operations      *operations.Service
	audit           *audit.Logger
	proxy           *security.ProxyResolver
	oidcSettings    *oidc.Settings
	oidcMu          sync.Mutex
	oidcClient      *oidc.Client
	oidcFingerprint string
	s3Keys          *s3api.Credentials
	s3Multipart     *s3api.MultipartStore
	s3Handler       http.Handler
	lifecycle       *lifecycle.Manager
	bootstrapToken  string
}

func (s *Server) lifecycleManager() *lifecycle.Manager {
	if s.lifecycle != nil {
		return s.lifecycle
	}
	return lifecycle.Default()
}

// New 便捷构造 HTTP Server（主要用于集成测试）；生产启动请通过 internal/app.New 装配。
func New(cfg *config.Config, dbConn *sql.DB, logger *slog.Logger) (*http.Server, *Server) {
	life := lifecycle.NewManager()
	srcSvc := sources.NewService(dbConn, cfg.Data.Dir)
	fileSvc := files.NewService(dbConn, srcSvc, locks.NewManager())
	fileSvc.SetLifecycle(life)
	capSvc := capabilities.NewService(dbConn, srcSvc)
	staticSvc := staticassets.NewService(dbConn, srcSvc, fileSvc, cfg.Server.PublicURL)
	pubSvc := publicdisk.NewService(fileSvc, capSvc)
	shareSvc := shares.NewService(dbConn, srcSvc, fileSvc, cfg.Server.PublicURL)
	sessSvc := auth.NewSessions(dbConn, time.Duration(cfg.Security.SessionTTLHours)*time.Hour)
	tokenSvc := auth.NewTokens(dbConn)
	s3Keys := s3api.NewCredentials(dbConn, cfg.Data.Dir, cfg.Security.MasterKey)
	s3MP := s3api.NewMultipartStore(dbConn, cfg.Data.Dir, fileSvc, cfg.Upload.MaxFileSizeMB)
	s3MP.SetLifecycle(life)
	ibSvc, err := imagebed.NewService(dbConn, cfg.Server.PublicURL,
		filepath.Join(cfg.Data.Dir, "cache", "thumbnails"), srcSvc, capSvc, fileSvc)
	if err != nil {
		panic(err)
	}
	ibSvc.SetLifecycle(life)
	opsSvc := operations.NewService(dbConn, cfg.DatabasePath(), cfg.Data.Dir, buildinfo.Version,
		srcSvc, fileSvc, ibSvc, sessSvc, MultipartCleanupAdapter(s3MP))
	loginLimit := cfg.Security.LoginRateLimit
	if !loginLimit.Enabled {
		loginLimit.MaxFailuresPerIP = 0
		loginLimit.MaxFailuresPerUsername = 0
	}
	return NewWithDeps(Deps{
		Config:       cfg,
		DB:           dbConn,
		Logger:       logger,
		Users:        users.NewService(dbConn),
		Sources:      srcSvc,
		Files:        fileSvc,
		Public:       pubSvc,
		Shares:       shareSvc,
		Sessions:     sessSvc,
		LoginLimiter: auth.NewLoginLimiter(time.Duration(loginLimit.WindowMinutes)*time.Minute, loginLimit.MaxFailuresPerIP, loginLimit.MaxFailuresPerUsername),
		Tokens:       tokenSvc,
		ImageBed:     ibSvc,
		AnonLimiter:  imagebed.NewRateLimiter(cfg.ImageBed.AnonymousRateLimit.PerIPPerHour),
		Capabilities: capSvc,
		StaticAssets: staticSvc,
		Operations:   opsSvc,
		Audit:        audit.New(dbConn, cfg.Audit.Enabled, cfg.Audit.MaxEntries, logger),
		Proxy:        security.NewProxyResolver(cfg.Server.TrustedProxies),
		OIDCSettings: oidc.NewSettings(dbConn),
		S3Keys:       s3Keys,
		S3Multipart:  s3MP,
		Lifecycle:    life,
	})
}

// NewWithDeps 基于已装配好的领域服务构造 HTTP Server 与路由表。
func NewWithDeps(deps Deps) (*http.Server, *Server) {
	cfg := deps.Config
	logger := deps.Logger
	bootstrapToken := strings.TrimSpace(deps.BootstrapToken)
	if bootstrapToken == "" {
		bootstrapToken = strings.TrimSpace(cfg.Security.BootstrapToken)
	}
	if bootstrapToken == "" {
		bootstrapToken = auth.NewRandomToken("setup-", 24)
	}
	s := &Server{
		cfg:            cfg,
		db:             deps.DB,
		logger:         logger,
		users:          deps.Users,
		sources:        deps.Sources,
		files:          deps.Files,
		public:         deps.Public,
		shares:         deps.Shares,
		sessions:       deps.Sessions,
		loginLimiter:   deps.LoginLimiter,
		tokens:         deps.Tokens,
		imagebed:       deps.ImageBed,
		anonLimiter:    deps.AnonLimiter,
		capabilities:   deps.Capabilities,
		staticassets:   deps.StaticAssets,
		operations:     deps.Operations,
		audit:          deps.Audit,
		proxy:          deps.Proxy,
		oidcSettings:   deps.OIDCSettings,
		s3Keys:         deps.S3Keys,
		s3Multipart:    deps.S3Multipart,
		lifecycle:      deps.Lifecycle,
		bootstrapToken: bootstrapToken,
	}
	if count, err := s.users.Count(); err == nil && count == 0 {
		logger.Warn("首次管理员初始化需要一次性凭据", "bootstrap_token", bootstrapToken)
	}
	s.s3Handler = s3api.NewHandler(s.s3Keys, s.sources, s.files, s.audit, s.proxy, logger,
		cfg.Upload.MaxFileSizeMB, s.s3Multipart)

	mux := http.NewServeMux()

	// 系统
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)

	// 初始化超级管理员
	mux.HandleFunc("GET /api/v1/setup/status", s.handleSetupStatus)
	mux.HandleFunc("POST /api/v1/setup/admin", s.handleSetupAdmin)

	// 认证
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/v1/auth/status", s.handleAuthStatus)
	mux.HandleFunc("GET /api/v1/auth/me", s.requireAuth(s.handleMe))

	// OIDC 单点登录（2.1.0，后端驱动 302 流程）
	mux.HandleFunc("GET /api/v1/auth/oidc/login", s.handleOIDCLogin)
	mux.HandleFunc("GET /api/v1/auth/oidc/callback", s.handleOIDCCallback)

	// 用户自助
	mux.HandleFunc("PATCH /api/v1/me/profile", s.requireAuth(s.handleUpdateProfile))
	mux.HandleFunc("GET /api/v1/me/preferences", s.requireAuth(s.handleMyPreferences))
	mux.HandleFunc("PATCH /api/v1/me/preferences", s.requireAuth(s.handleUpdatePreferences))
	mux.HandleFunc("POST /api/v1/me/password", s.requireAuth(s.handleChangePassword))
	mux.HandleFunc("POST /api/v1/me/oidc/unbind", s.requireAuth(s.handleUnbindMyOIDC))
	mux.HandleFunc("GET /api/v1/me/activity", s.requireAuth(s.handleMyActivity))
	mux.HandleFunc("GET /api/v1/me/recent-files", s.requireAuth(s.handleMyRecentFiles))
	mux.HandleFunc("GET /api/v1/me/favorites", s.requireAuth(s.handleMyFavorites))
	mux.HandleFunc("POST /api/v1/me/favorites", s.requireAuth(s.handleAddFavorite))
	mux.HandleFunc("DELETE /api/v1/me/favorites/{favoriteID}", s.requireAuth(s.handleDeleteFavorite))
	mux.HandleFunc("GET /api/v1/me/quota", s.requireAuth(s.handleMyQuota))
	mux.HandleFunc("GET /api/v1/me/tokens", s.requireAuth(s.handleTokenStatus))
	mux.HandleFunc("POST /api/v1/me/tokens/webdav/reset", s.requireAuth(s.handleResetToken(auth.TokenTypeWebDAV)))
	mux.HandleFunc("POST /api/v1/me/tokens/image-bed/reset", s.requireAuth(s.handleResetToken(auth.TokenTypeImageBed)))
	mux.HandleFunc("GET /api/v1/me/tokens/image-bed", s.requireAuth(s.handleListImageBedTokens))
	mux.HandleFunc("POST /api/v1/me/tokens/image-bed", s.requireAuth(s.handleCreateImageBedToken))
	mux.HandleFunc("DELETE /api/v1/me/tokens/image-bed/{token_id}", s.requireAuth(s.handleDeleteImageBedToken))
	mux.HandleFunc("GET /api/v1/me/s3-credentials", s.requireAuth(s.handleListS3Credentials))
	mux.HandleFunc("POST /api/v1/me/s3-credentials", s.requireAuth(s.handleCreateS3Credential))
	mux.HandleFunc("POST /api/v1/me/s3-credentials/{access_key_id}/disable", s.requireAuth(s.handleSetS3CredentialDisabled(true)))
	mux.HandleFunc("POST /api/v1/me/s3-credentials/{access_key_id}/enable", s.requireAuth(s.handleSetS3CredentialDisabled(false)))
	mux.HandleFunc("DELETE /api/v1/me/s3-credentials/{access_key_id}", s.requireAuth(s.handleDeleteS3Credential))

	// 管理员：用户管理
	mux.HandleFunc("GET /api/v1/admin/users", s.requireAdmin(s.handleAdminListUsers))
	mux.HandleFunc("POST /api/v1/admin/users", s.requireAdmin(s.handleAdminCreateUser))
	mux.HandleFunc("POST /api/v1/admin/users/{id}/disable", s.requireAdmin(s.handleAdminSetUserDisabled(true)))
	mux.HandleFunc("POST /api/v1/admin/users/{id}/enable", s.requireAdmin(s.handleAdminSetUserDisabled(false)))
	mux.HandleFunc("PATCH /api/v1/admin/users/{id}/quota", s.requireAdmin(s.handleAdminSetUserQuota))
	mux.HandleFunc("POST /api/v1/admin/users/{id}/revoke-credentials", s.requireAdmin(s.handleAdminRevokeUserCredentials))
	mux.HandleFunc("POST /api/v1/admin/users/{id}/unbind-oidc", s.requireAdmin(s.handleAdminUnbindOIDC))
	mux.HandleFunc("DELETE /api/v1/admin/users/{id}", s.requireAdmin(s.handleAdminDeleteUser))

	// 管理员：概览（dashboard 聚合数据）
	mux.HandleFunc("GET /api/v1/admin/overview", s.requireAdmin(s.handleAdminOverview))

	// 系统功能开关（公开访问，仅返回开关 + 状态文本，docs/home-1.png 右栏"系统状态"）
	mux.HandleFunc("GET /api/v1/system/status", s.handleSystemStatus)

	// 登录用户：可访问存储源
	mux.HandleFunc("GET /api/v1/sources", s.requireAuth(s.handleListMySources))
	mux.HandleFunc("GET /api/v1/search", s.requireAuth(s.handleSearchFiles))
	mux.HandleFunc("GET /api/v1/shares", s.requireAuth(s.handleListShares))
	mux.HandleFunc("POST /api/v1/shares", s.requireAuth(s.handleCreateShare))
	mux.HandleFunc("DELETE /api/v1/shares/{shareKey}", s.requireAuth(s.handleDeleteShare))
	mux.HandleFunc("GET /api/v1/sources/{key}/quota", s.requireAuth(s.handleSourceQuota))

	// 私有网盘文件操作（README §13.2）
	mux.HandleFunc("GET /api/v1/sources/{key}/permission", s.requireAuth(s.handlePathPermission))
	mux.HandleFunc("GET /api/v1/sources/{key}/files", s.requireAuth(s.handleListFiles))
	mux.HandleFunc("GET /api/v1/sources/{key}/files/stat", s.requireAuth(s.handleStatFile))
	mux.HandleFunc("GET /api/v1/sources/{key}/download", s.requireAuth(s.handleDownloadFile))
	mux.HandleFunc("GET /api/v1/sources/{key}/raw", s.requireAuth(s.handleRawFile))
	mux.HandleFunc("POST /api/v1/sources/{key}/download/archive", s.requireAuth(s.handleDownloadArchive))
	mux.HandleFunc("POST /api/v1/sources/{key}/folders", s.requireAuth(s.handleCreateFolder))
	mux.HandleFunc("POST /api/v1/sources/{key}/upload", s.requireAuth(s.handleUploadFile))
	mux.HandleFunc("DELETE /api/v1/sources/{key}/files", s.requireAuth(s.handleDeleteFile))
	mux.HandleFunc("POST /api/v1/sources/{key}/files/rename", s.requireAuth(s.handleRenameFile))
	mux.HandleFunc("POST /api/v1/sources/{key}/files/move", s.requireAuth(s.handleMoveFile))
	mux.HandleFunc("POST /api/v1/sources/{key}/files/copy", s.requireAuth(s.handleCopyFile))
	mux.HandleFunc("GET /api/v1/sources/{key}/trash", s.requireAuth(s.handleListTrash))
	mux.HandleFunc("POST /api/v1/sources/{key}/trash/{trashKey}/restore", s.requireAuth(s.handleRestoreTrash))
	mux.HandleFunc("DELETE /api/v1/sources/{key}/trash/{trashKey}", s.requireAuth(s.handlePurgeTrash))

	// 管理员：存储源管理
	mux.HandleFunc("GET /api/v1/admin/sources", s.requireAdmin(s.handleAdminListSources))
	mux.HandleFunc("POST /api/v1/admin/sources/preflight", s.requireAdmin(s.handleAdminPreflightSource))
	mux.HandleFunc("POST /api/v1/admin/sources", s.requireAdmin(s.handleAdminCreateSource))
	mux.HandleFunc("GET /api/v1/admin/sources/{key}", s.requireAdmin(s.handleAdminGetSource))
	mux.HandleFunc("PATCH /api/v1/admin/sources/{key}", s.requireAdmin(s.handleAdminUpdateSource))
	mux.HandleFunc("POST /api/v1/admin/sources/{key}/reconcile", s.requireAdmin(s.handleAdminReconcileSource))
	mux.HandleFunc("POST /api/v1/admin/sources/{key}/rebind-root", s.requireAdmin(s.handleAdminRebindRoot))
	mux.HandleFunc("POST /api/v1/admin/sources/{key}/disable", s.requireAdmin(s.handleAdminSetSourceDisabled(true)))
	mux.HandleFunc("POST /api/v1/admin/sources/{key}/enable", s.requireAdmin(s.handleAdminSetSourceDisabled(false)))
	mux.HandleFunc("DELETE /api/v1/admin/sources/{key}", s.requireAdmin(s.handleAdminDeleteSource))
	mux.HandleFunc("PUT /api/v1/admin/sources/{key}/exclude-patterns", s.requireAdmin(s.handleAdminSetExcludePatterns))

	// 管理员：访问策略
	mux.HandleFunc("GET /api/v1/admin/policies", s.requireAdmin(s.handleAdminListPolicies))
	mux.HandleFunc("POST /api/v1/admin/policies", s.requireAdmin(s.handleAdminCreatePolicy))
	mux.HandleFunc("GET /api/v1/admin/policies/{key}", s.requireAdmin(s.handleAdminGetPolicy))
	mux.HandleFunc("PUT /api/v1/admin/policies/{key}", s.requireAdmin(s.handleAdminUpdatePolicy))
	mux.HandleFunc("DELETE /api/v1/admin/policies/{key}", s.requireAdmin(s.handleAdminDeletePolicy))

	// 公开网盘（匿名可访问，2.0：/public 固定映射全局绑定 Source）
	mux.HandleFunc("GET /api/v1/public/summary", s.handlePublicSummary)
	mux.HandleFunc("GET /api/v1/public/browse", s.handlePublicBrowse)
	mux.HandleFunc("GET /api/v1/public/shares/{shareKey}", s.handlePublicShareInfo)
	mux.HandleFunc("POST /api/v1/public/shares/{shareKey}/unlock", s.handlePublicShareUnlock)
	mux.HandleFunc("GET /api/v1/public/shares/{shareKey}/browse", s.handlePublicShareBrowse)
	mux.HandleFunc("GET /share/{shareKey}/archive", s.handlePublicShareArchive)
	mux.HandleFunc("GET /public/raw/{path...}", s.handlePublicRaw)
	mux.HandleFunc("HEAD /public/raw/{path...}", s.handlePublicRaw)
	mux.HandleFunc("GET /share/{shareKey}/raw", s.handlePublicShareRaw)
	mux.HandleFunc("HEAD /share/{shareKey}/raw", s.handlePublicShareRaw)
	mux.HandleFunc("GET /share/{shareKey}/raw/{childPath...}", s.handlePublicShareRaw)
	mux.HandleFunc("HEAD /share/{shareKey}/raw/{childPath...}", s.handlePublicShareRaw)

	// WebDAV（README §16）
	davHandler := webdav.New(s.tokens, s.sources, s.files, s.audit, s.proxy, logger, cfg.Upload.MaxFileSizeMB)
	mux.Handle("/dav", davHandler)
	mux.Handle("/dav/", davHandler)

	// 图床：公开图片访问（README §17.8）
	mux.HandleFunc("GET /i/{image_file}", s.handleServeImage)
	mux.HandleFunc("GET /t/{thumbnail_file}", s.handleServeThumbnail)

	// 图床：登录用户（2.0：目标由 Site Capability 绑定决定）
	mux.HandleFunc("GET /api/v1/image-bed/status", s.requireAuth(s.handleImageBedStatus))
	mux.HandleFunc("POST /api/v1/image-bed/uploads", s.requireAuth(s.handleImageBedUpload))
	mux.HandleFunc("GET /api/v1/image-bed/images", s.requireAuth(s.handleImageBedHistory))
	mux.HandleFunc("DELETE /api/v1/image-bed/images/{image_id}", s.requireAuth(s.handleImageBedDelete))

	// 图床：PicGo 兼容接口（Bearer Token，README §17.14）
	mux.HandleFunc("POST /api/v1/image-bed/upload", s.handlePicGoUpload)

	// 图床：匿名公共图床（README §17.5）
	mux.HandleFunc("GET /api/v1/image-bed/anonymous-status", s.handleAnonymousImageBedStatus)
	mux.HandleFunc("POST /api/v1/image-bed/anonymous-upload", s.handleAnonymousImageBedUpload)

	// 管理员：匿名图床配置与匿名图片管理
	mux.HandleFunc("GET /api/v1/admin/image-bed/anonymous-settings", s.requireAdmin(s.handleAdminGetAnonymousSettings))
	mux.HandleFunc("PUT /api/v1/admin/image-bed/anonymous-settings", s.requireAdmin(s.handleAdminSetAnonymousSettings))
	mux.HandleFunc("GET /api/v1/admin/image-bed/retention", s.requireAdmin(s.handleAdminGetImageBedRetention))
	mux.HandleFunc("PUT /api/v1/admin/image-bed/retention", s.requireAdmin(s.handleAdminSetImageBedRetention))
	mux.HandleFunc("GET /api/v1/admin/image-bed/anonymous-images", s.requireAdmin(s.handleAdminListAnonymousImages))
	mux.HandleFunc("DELETE /api/v1/admin/image-bed/anonymous-images/{image_id}", s.requireAdmin(s.handleAdminDeleteAnonymousImage))

	// 管理员：Site Capability 绑定（2.0）
	mux.HandleFunc("GET /api/v1/admin/capabilities", s.requireAdmin(s.handleAdminListCapabilities))
	mux.HandleFunc("PUT /api/v1/admin/capabilities/{capability}", s.requireAdmin(s.handleAdminUpdateCapability))

	// 管理员：运维中心（2.0 Epic C）
	mux.HandleFunc("GET /api/v1/admin/operations/status", s.requireAdmin(s.handleAdminOperationsStatus))
	mux.HandleFunc("POST /api/v1/admin/operations/integrity-check", s.requireAdmin(s.handleAdminIntegrityCheck))
	mux.HandleFunc("POST /api/v1/admin/operations/cleanup", s.requireAdmin(s.handleAdminOperationsCleanup))

	// 管理员：静态资源配置（2.0 AST）
	mux.HandleFunc("GET /api/v1/admin/static-access/settings", s.requireAdmin(s.handleAdminGetStaticSettings))
	mux.HandleFunc("PUT /api/v1/admin/static-access/settings", s.requireAdmin(s.handleAdminUpdateStaticSettings))
	mux.HandleFunc("DELETE /api/v1/admin/sources/{key}/static-access/config", s.requireAdmin(s.handleAdminRemoveStaticConfig))
	mux.HandleFunc("GET /api/v1/admin/sources/{key}/static-access/config", s.requireAdmin(s.handleAdminGetStaticConfig))
	mux.HandleFunc("POST /api/v1/admin/sources/{key}/static-access/config", s.requireAdmin(s.handleAdminConfigureStatic))
	mux.HandleFunc("PUT /api/v1/admin/sources/{key}/static-access/config", s.requireAdmin(s.handleAdminUpdateStaticConfig))
	mux.HandleFunc("POST /api/v1/admin/sources/{key}/static-access/preflight", s.requireAdmin(s.handleAdminPreflightStatic))
	mux.HandleFunc("POST /api/v1/admin/sources/{key}/static-access/rebind", s.requireAdmin(s.handleAdminRebindStatic))

	// 公开静态资源读取（2.0 AST）：方法无关注册，保证非允许方法返回 405
	// 而不是落入 SPA fallback。/assets/ 前缀也承载前端构建产物：
	// 非公开标识形状的首段必须交还 SPA 静态文件，否则 JS bundle 404 白屏。
	spa := s.spaHandler()
	// 同时注册单段与多段模式：避免 ServeMux 对 /assets/{name} 的自动
	// 尾斜杠重定向把前端 bundle 变成 index.html。
	mux.Handle("/assets/{assetID}", s.staticAssetHandler(spa))
	mux.Handle("/assets/{assetID}/{path...}", s.staticAssetHandler(spa))

	// 管理员：审计日志（筛选与分页）
	mux.HandleFunc("GET /api/v1/admin/audit-logs", s.requireAdmin(s.handleAdminAuditLogs))

	// 管理员：实例品牌信息（1.2.0）
	mux.HandleFunc("GET /api/v1/admin/branding", s.requireAdmin(s.handleAdminGetBranding))
	mux.HandleFunc("PUT /api/v1/admin/branding", s.requireAdmin(s.handleAdminSetBranding))

	// 管理员：OIDC 单点登录配置（2.1.0）
	mux.HandleFunc("GET /api/v1/admin/oidc", s.requireAdmin(s.handleAdminGetOIDC))
	mux.HandleFunc("PUT /api/v1/admin/oidc", s.requireAdmin(s.handleAdminSetOIDC))
	mux.HandleFunc("POST /api/v1/admin/oidc/test", s.requireAdmin(s.handleAdminTestOIDC))

	// 管理员：手动导出系统配置包
	mux.HandleFunc("GET /api/v1/admin/system/config-export", s.requireAdmin(s.handleAdminExportSystemConfig))

	// API 未匹配路由统一返回 JSON 404，避免落入 SPA fallback。
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, CodeFileNotFound, "接口不存在", nil)
	})

	// 已移除的中转入口不再回退 SPA 外壳，旧链接明确 404。
	mux.HandleFunc("GET /transfer", http.NotFound)
	mux.HandleFunc("GET /transfer/{path...}", http.NotFound)
	mux.HandleFunc("GET /app/transfer", http.NotFound)
	mux.HandleFunc("GET /pickup/{publicKey}", http.NotFound)
	mux.HandleFunc("GET /collect/{publicKey}", http.NotFound)

	mux.Handle("/", spa)

	var handler http.Handler = mux
	handler = WithRecover(logger, handler)
	handler = WithAccessLog(logger, handler)
	handler = WithRequestID(handler)
	handler = WithSecurityHeaders(handler)

	return &http.Server{
		Addr:              cfg.Server.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: serverReadHeaderTimeout,
		IdleTimeout:       serverIdleTimeout,
	}, s
}

// Sessions 暴露 Session 服务。
func (s *Server) Sessions() *auth.Sessions {
	return s.sessions
}

// Files 暴露文件服务。
func (s *Server) Files() *files.Service {
	return s.files
}

// ImageBed 暴露图床服务。
func (s *Server) ImageBed() *imagebed.Service {
	return s.imagebed
}

// S3Server 返回仅承载 S3 Path-style API 的专用 HTTP Server。
func (s *Server) S3Server() *http.Server {
	return &http.Server{
		Addr: s.cfg.Server.S3Addr, Handler: s.s3Handler,
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
	}
}

// S3Multipart 暴露 Multipart 状态服务。
func (s *Server) S3Multipart() *s3api.MultipartStore {
	return s.s3Multipart
}

// MultipartCleanupAdapter 适配 s3api 的清理结果签名给 operations.Service 使用。
func MultipartCleanupAdapter(store *s3api.MultipartStore) func(time.Duration) (int64, int64, int64, error) {
	return func(maxAge time.Duration) (int64, int64, int64, error) {
		result, err := store.CleanupExpired(maxAge)
		return int64(result.UploadsRemoved), int64(result.OrphansRemoved), 0, err
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.db.Ping(); err != nil {
		WriteError(w, r, CodeInternalError, "数据库不可用", nil)
		return
	}
	WriteData(w, r, map[string]any{
		"status":     "ok",
		"version":    buildinfo.Version,
		"commit":     buildinfo.Commit,
		"build_time": buildinfo.BuildTime,
	})
}

// spaHandler 提供嵌入式前端静态资源。
// 命中真实文件时直接返回；未命中时回退 index.html 交给前端路由。
func (s *Server) spaHandler() http.Handler {
	dist, err := fs.Sub(web.DistFS, "dist")
	if err != nil {
		panic("前端构建产物未嵌入: " + err.Error())
	}
	fileServer := http.FileServerFS(dist)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 托管命名空间不能借 SPA fallback 返回 200 页面；在回退前直接 404。
		if security.ContainsManagedNamespace(strings.TrimPrefix(r.URL.Path, "/")) {
			http.NotFound(w, r)
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(dist, p); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}
		// 前端路由 fallback
		http.ServeFileFS(w, r, dist, "index.html")
	})
}
