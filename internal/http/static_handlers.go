package httpserver

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/omni-store/omnistore/internal/audit"
	"github.com/omni-store/omnistore/internal/staticassets"
)

// --- 公开静态资源读取（2.0 AST）：GET/HEAD /assets/{assetID}/{path...} ---

// staticCORSAllowedHeaders 是预检允许的请求头：只服务 GET/HEAD 所需范围。
var staticCORSAllowedHeaders = "Range, If-None-Match, If-Modified-Since, If-Range"

// staticExposeHeaders 暴露给跨域脚本读取的响应头。
const staticExposeHeaders = "ETag, Accept-Ranges, Content-Range"

func applyStaticCORS(w http.ResponseWriter, mode string, allowedOrigins []string, r *http.Request) {
	switch mode {
	case staticassets.CorsPublic:
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Expose-Headers", staticExposeHeaders)
	case staticassets.CorsAllowlist:
		w.Header().Add("Vary", "Origin")
		origin := r.Header.Get("Origin")
		for _, candidate := range allowedOrigins {
			if origin != "" && strings.EqualFold(origin, candidate) {
				w.Header().Set("Access-Control-Allow-Origin", candidate)
				w.Header().Set("Access-Control-Expose-Headers", staticExposeHeaders)
				return
			}
		}
	}
}

// staticAssetHandler 分派 /assets/{assetID}/{path...} 的全部方法：
// 首段不是合法公开标识形状时交给 SPA 静态文件（/assets/ 同时承载前端构建产物，
// 不能被公开资源路由遮蔽）；标识形状的请求按 GET/HEAD 读取、OPTIONS 预检、
// 其余方法 405 分派（内部路径拒绝在 SPA fallback 之前）。
func (s *Server) staticAssetHandler(spa http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !staticassets.IsAssetIDShape(r.PathValue("assetID")) {
			spa.ServeHTTP(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			s.handleStaticAsset(w, r)
		case http.MethodOptions:
			s.handleStaticAssetOptions(w, r)
		default:
			w.Header().Set("Allow", "GET, HEAD, OPTIONS")
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func (s *Server) handleStaticAsset(w http.ResponseWriter, r *http.Request) {
	resolved, err := s.staticassets.Open(r.PathValue("assetID"), r.PathValue("path"))
	if err != nil {
		// 真实 404：不存在、目录、托管段、越界、排除、停用与类型不允许不可区分。
		w.Header().Set("Cache-Control", "no-store")
		http.NotFound(w, r)
		return
	}
	defer resolved.Unlock()
	defer resolved.File.Close()

	applyStaticCORS(w, resolved.CorsMode, resolved.AllowedOrigins, r)
	w.Header().Set("Content-Type", resolved.MimeType)
	w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
	w.Header().Set("Cache-Control", resolved.CacheControl)
	w.Header().Set("ETag", staticassets.ResolvedETag(resolved.Info))
	// http.ServeContent 处理条件请求与单段 Range（206/416/304），流式读取不整段缓冲；
	// ETag 为弱验证器，If-Range 无强验证依据时按 RFC 9110 返回完整 200。
	serveContentWithDeadline(w, r, "", resolved.ModTime(), resolved.File)
}

func (s *Server) handleStaticAssetOptions(w http.ResponseWriter, r *http.Request) {
	resolved, err := s.staticassets.Open(r.PathValue("assetID"), r.PathValue("path"))
	if err != nil {
		w.Header().Set("Cache-Control", "no-store")
		http.NotFound(w, r)
		return
	}
	defer resolved.Unlock()
	defer resolved.File.Close()
	applyStaticCORS(w, resolved.CorsMode, resolved.AllowedOrigins, r)
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD")
	w.Header().Set("Access-Control-Allow-Headers", staticCORSAllowedHeaders)
	w.Header().Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

// --- 管理员：静态资源配置（AST-02/AST-10） ---

func (s *Server) handleAdminGetStaticConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.staticassets.Config(r.PathValue("key"))
	if err != nil {
		writeStaticConfigError(w, r, err)
		return
	}
	WriteData(w, r, cfg)
}

func writeStaticConfigError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		WriteError(w, r, CodeSourceNotFound, "存储源不存在", nil)
	case errors.Is(err, staticassets.ErrRebindRequired):
		WriteError(w, r, CodeStaticRebindRequired, err.Error(), nil)
	case errors.Is(err, staticassets.ErrAlreadyConfigured),
		errors.Is(err, staticassets.ErrTargetConfigured),
		errors.Is(err, staticassets.ErrDisableRequired),
		errors.Is(err, staticassets.ErrNotConfigured),
		errors.Is(err, staticassets.ErrRebindModeInvalid),
		errors.Is(err, staticassets.ErrRelocateUnconfirmed),
		errors.Is(err, staticassets.ErrRootConfirmRequired),
		errors.Is(err, staticassets.ErrRevisionConflict):
		WriteError(w, r, CodeConflict, err.Error(), nil)
	case errors.Is(err, staticassets.ErrPublishRootInvalid):
		WriteError(w, r, CodeValidationError, err.Error(), nil)
	default:
		WriteError(w, r, CodeValidationError, err.Error(), nil)
	}
}

func (s *Server) handleAdminConfigureStatic(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PublishRoot        string   `json:"publish_root"`
		Enabled            *bool    `json:"enabled"`
		CacheMode          string   `json:"cache_mode"`
		CorsMode           string   `json:"cors_mode"`
		PublicOrigin       string   `json:"public_origin"`
		AllowedOrigins     []string `json:"allowed_origins"`
		ConfirmPublishRoot bool     `json:"confirm_publish_root"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	user := CurrentUser(r.Context())
	cfg, err := s.staticassets.Configure(staticassets.ConfigureInput{
		SourceKey:          r.PathValue("key"),
		PublishRoot:        req.PublishRoot,
		Enabled:            req.Enabled,
		CacheMode:          req.CacheMode,
		CorsMode:           req.CorsMode,
		PublicOrigin:       req.PublicOrigin,
		AllowedOrigins:     req.AllowedOrigins,
		ConfirmPublishRoot: req.ConfirmPublishRoot,
	}, &user.ID, s.audit)
	if err != nil {
		writeStaticConfigError(w, r, err)
		return
	}
	s.adminAudit(r, "configure_static_assets", audit.StatusSuccess, "")
	WriteData(w, r, cfg)
}

func (s *Server) handleAdminUpdateStaticConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceKey        *string   `json:"source_key"`
		PublishRoot      *string   `json:"publish_root"`
		Enabled          *bool     `json:"enabled"`
		CacheMode        *string   `json:"cache_mode"`
		CorsMode         *string   `json:"cors_mode"`
		PublicOrigin     *string   `json:"public_origin"`
		AllowedOrigins   *[]string `json:"allowed_origins"`
		ExpectedRevision *int64    `json:"expected_revision"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.SourceKey != nil || req.PublishRoot != nil {
		current, err := s.staticassets.Config(r.PathValue("key"))
		if err != nil {
			writeStaticConfigError(w, r, err)
			return
		}
		if (req.SourceKey != nil && *req.SourceKey != current.SourceKey) ||
			(req.PublishRoot != nil && *req.PublishRoot != current.PublishRoot) {
			writeStaticConfigError(w, r, staticassets.ErrRebindRequired)
			return
		}
	}
	cfg, err := s.staticassets.Update(staticassets.UpdateInput{
		SourceKey:        r.PathValue("key"),
		Enabled:          req.Enabled,
		CacheMode:        req.CacheMode,
		CorsMode:         req.CorsMode,
		PublicOrigin:     req.PublicOrigin,
		AllowedOrigins:   req.AllowedOrigins,
		ExpectedRevision: req.ExpectedRevision,
	})
	if err != nil {
		writeStaticConfigError(w, r, err)
		return
	}
	s.adminAudit(r, "update_static_assets", audit.StatusSuccess, "")
	WriteData(w, r, cfg)
}

func (s *Server) handleAdminRebindStatic(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceKey          string `json:"source_key"`
		PublishRoot        string `json:"publish_root"`
		Mode               string `json:"mode"`
		ConfirmPublishRoot bool   `json:"confirm_publish_root"`
		ConfirmRelocate    bool   `json:"confirm_relocate"`
		ExpectedRevision   *int64 `json:"expected_revision"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	user := CurrentUser(r.Context())
	cfg, err := s.staticassets.Rebind(r.PathValue("key"), staticassets.RebindInput{
		SourceKey:          req.SourceKey,
		PublishRoot:        req.PublishRoot,
		Mode:               req.Mode,
		ConfirmPublishRoot: req.ConfirmPublishRoot,
		ConfirmRelocate:    req.ConfirmRelocate,
		ExpectedRevision:   req.ExpectedRevision,
	}, &user.ID, s.audit)
	if err != nil {
		writeStaticConfigError(w, r, err)
		return
	}
	WriteData(w, r, cfg)
}

func (s *Server) handleAdminPreflightStatic(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PublishRoot string `json:"publish_root"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.staticassets.Preflight(staticassets.PreflightInput{
		SourceKey:   r.PathValue("key"),
		PublishRoot: req.PublishRoot,
	})
	if err != nil {
		writeStaticConfigError(w, r, err)
		return
	}
	WriteData(w, r, result)
}

func (s *Server) handleAdminRemoveStaticConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.staticassets.Remove(r.PathValue("key"), req.ExpectedRevision); err != nil {
		writeStaticConfigError(w, r, err)
		return
	}
	s.adminAudit(r, "remove_static_access", audit.StatusSuccess, "")
	WriteData(w, r, map[string]any{"ok": true})
}

func (s *Server) handleAdminGetStaticSettings(w http.ResponseWriter, r *http.Request) {
	view, err := s.staticassets.GlobalSettingsView()
	if err != nil {
		WriteError(w, r, CodeInternalError, "查询静态 HTTP 配置失败", nil)
		return
	}
	WriteData(w, r, view)
}

func (s *Server) handleAdminUpdateStaticSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PublicOrigin   string   `json:"public_origin"`
		CacheMode      string   `json:"cache_mode"`
		CorsMode       string   `json:"cors_mode"`
		AllowedOrigins []string `json:"allowed_origins"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	user := CurrentUser(r.Context())
	var actorID *int64
	if user != nil {
		actorID = &user.ID
	}
	view, err := s.staticassets.SaveGlobalSettings(staticassets.GlobalSettings{
		PublicOrigin:   req.PublicOrigin,
		CacheMode:      req.CacheMode,
		CorsMode:       req.CorsMode,
		AllowedOrigins: req.AllowedOrigins,
	}, actorID, s.audit)
	if err != nil {
		writeStaticConfigError(w, r, err)
		return
	}
	s.adminAudit(r, "update_static_http_settings", audit.StatusSuccess, "")
	WriteData(w, r, view)
}
