package httpserver

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/omni-store/omnistore/internal/audit"
	"github.com/omni-store/omnistore/internal/auth"
	"github.com/omni-store/omnistore/internal/models"
	"github.com/omni-store/omnistore/internal/oidc"
	"github.com/omni-store/omnistore/internal/users"
)

// 登录页 sso_error 参数：错误明细不入 URL，前端按码展示中文提示。
const (
	oidcErrDisabled          = "oidc_disabled"
	oidcErrNotConfigured     = "oidc_not_configured"
	oidcErrStateMismatch     = "oidc_state_mismatch"
	oidcErrProviderDenied    = "oidc_provider_denied"
	oidcErrExchangeFailed    = "oidc_exchange_failed"
	oidcErrIdentityUnbound   = "oidc_identity_unbound"
	oidcErrIdentityTaken     = "oidc_identity_taken"
	oidcErrUsernameInvalid   = "oidc_username_invalid"
	oidcErrUsernameConflict  = "oidc_username_conflict"
	oidcErrAccountDisabled   = "oidc_account_disabled"
	oidcErrBindRequiresLogin = "oidc_bind_requires_login"
	oidcErrRateLimited       = "oidc_rate_limited"
	oidcErrInternal          = "oidc_internal"
)

// flow cookie 只随 /api/v1/auth/oidc 前缀回传，10 分钟内一次性有效。
const (
	oidcFlowCookie = "omnistore_oidc_flow"
	oidcFlowTTL    = 10 * time.Minute
)

type oidcFlow struct {
	State string
	Nonce string
	Mode  string // login | bind
}

// oidcEnabled 返回 SSO 是否启用（供免鉴权状态接口使用）。
func (s *Server) oidcEnabled() bool {
	cfg, err := s.oidcSettings.Load()
	return err == nil && cfg.Enabled
}

// oidcClientFor 按配置指纹缓存 discovery/JWKS 客户端，避免每次登录重新发现。
func (s *Server) oidcClientFor(cfg *oidc.Config) (*oidc.Client, error) {
	fingerprint := strings.Join([]string{cfg.Issuer, cfg.ClientID, cfg.ClientSecret,
		strings.Join(cfg.ScopesOrDefault(), ",")}, "|")
	s.oidcMu.Lock()
	defer s.oidcMu.Unlock()
	if s.oidcClient != nil && s.oidcFingerprint == fingerprint {
		return s.oidcClient, nil
	}
	client, err := oidc.NewClient(context.Background(), cfg)
	if err != nil {
		return nil, err
	}
	s.oidcClient, s.oidcFingerprint = client, fingerprint
	return client, nil
}

// --- 发起与回调（免鉴权，后端驱动 302 流程） ---

// handleOIDCLogin 发起 SSO 登录：写一次性 flow cookie 后 302 到 IdP 授权页。
// ?mode=bind 为已登录用户的身份绑定流程。
func (s *Server) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.oidcSettings.Load()
	if err != nil {
		s.oidcRedirectError(w, r, oidcErrInternal)
		return
	}
	if !cfg.Enabled {
		s.oidcRedirectError(w, r, oidcErrDisabled)
		return
	}
	client, err := s.oidcClientFor(cfg)
	if err != nil {
		s.oidcRedirectError(w, r, oidcErrNotConfigured)
		return
	}

	mode := "login"
	if r.URL.Query().Get("mode") == "bind" {
		cookie, err := r.Cookie(auth.SessionCookieName)
		if err != nil {
			s.oidcRedirectError(w, r, oidcErrBindRequiresLogin)
			return
		}
		if _, _, err := s.sessions.Validate(cookie.Value); err != nil {
			s.oidcRedirectError(w, r, oidcErrBindRequiresLogin)
			return
		}
		mode = "bind"
	}

	state := auth.NewRandomToken("", 16)
	nonce := auth.NewRandomToken("", 16)
	s.setOIDCFlowCookie(w, oidcFlow{State: state, Nonce: nonce, Mode: mode})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, client.AuthURL(oidc.RedirectURI(s.cfg.Server.PublicURL), state, nonce), http.StatusFound)
}

// handleOIDCCallback 完成 SSO 登录：校验 state/nonce → 换取并验证 ID token →
// 用户映射 → 建立本地 Session → 302 回应用。所有失败统一跳登录页 sso_error。
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	ip := s.proxy.ClientIP(r)
	ua := r.UserAgent()

	flow, ok := s.oidcFlowFrom(r)
	if !ok {
		s.oidcRedirectError(w, r, oidcErrStateMismatch)
		return
	}
	s.clearOIDCFlowCookie(w)

	// state 必须与发起时写入 cookie 的随机值一致，防止伪造回调。
	if subtle.ConstantTimeCompare([]byte(flow.State), []byte(r.URL.Query().Get("state"))) != 1 {
		s.oidcRedirectError(w, r, oidcErrStateMismatch)
		return
	}
	// IdP 侧拒绝（如用户取消授权）不算认证失败，不消耗限流额度。
	if r.URL.Query().Get("error") != "" {
		s.oidcRedirectError(w, r, oidcErrProviderDenied)
		return
	}

	cfg, err := s.oidcSettings.Load()
	if err != nil || !cfg.Enabled {
		s.oidcRedirectError(w, r, oidcErrDisabled)
		return
	}

	// IP 维度限流与密码登录共享额度：Begin 预占，成功释放，失败自然过期。
	attempt, _, allowed := s.loginLimiter.Begin(ip, "")
	if !allowed {
		s.oidcRedirectError(w, r, oidcErrRateLimited)
		return
	}
	finished := false
	defer func() {
		if !finished {
			s.loginLimiter.Cancel(attempt)
		}
	}()

	client, err := s.oidcClientFor(cfg)
	if err != nil {
		finished = true
		s.oidcAuditFailure(nil, "oidc_login_failed", ip, ua, oidcErrNotConfigured)
		s.oidcRedirectError(w, r, oidcErrNotConfigured)
		return
	}

	claims, err := client.Exchange(r.Context(), r.URL.Query().Get("code"),
		oidc.RedirectURI(s.cfg.Server.PublicURL), flow.Nonce)
	if err != nil {
		finished = true
		s.oidcAuditFailure(nil, "oidc_login_failed", ip, ua, oidcErrExchangeFailed)
		s.oidcRedirectError(w, r, oidcErrExchangeFailed)
		return
	}

	if flow.Mode == "bind" {
		finished = true
		s.finishOIDCBind(w, r, claims)
		return
	}

	u, errCode := s.resolveOIDCUser(cfg, strings.TrimSpace(cfg.Issuer), claims)
	if errCode != "" {
		finished = true
		s.oidcAuditFailure(nil, "oidc_login_failed", ip, ua, errCode)
		s.oidcRedirectError(w, r, errCode)
		return
	}
	if u.IsDisabled {
		finished = true
		s.oidcAuditFailure(&u.ID, "oidc_login_failed", ip, ua, oidcErrAccountDisabled)
		s.oidcRedirectError(w, r, oidcErrAccountDisabled)
		return
	}

	sessionID, _, err := s.sessions.Create(u.ID, ua, ip)
	if err != nil {
		finished = true
		s.oidcAuditFailure(&u.ID, "oidc_login_failed", ip, ua, oidcErrInternal)
		s.oidcRedirectError(w, r, oidcErrInternal)
		return
	}
	finished = true
	s.loginLimiter.Success(attempt)
	s.setSessionCookie(w, sessionID)
	s.audit.Log(audit.Entry{
		ActorType: audit.ActorUser, ActorUserID: &u.ID,
		EntryType: audit.EntryWeb, Action: "oidc_login_success",
		IPAddress: ip, UserAgent: ua, Status: audit.StatusSuccess,
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/app", http.StatusFound)
}

// resolveOIDCUser 按映射优先级解析 IdP 身份对应的本地账号：
// 已绑定 → 按用户名自动绑定（仅普通账号）→ JIT 建号 → 拒绝。
// 返回空错误码表示成功。
func (s *Server) resolveOIDCUser(cfg *oidc.Config, issuer string, claims *oidc.Claims) (*models.User, string) {
	u, err := s.users.GetByOIDCIdentity(issuer, claims.Subject)
	if err == nil {
		return u, ""
	}
	if !errors.Is(err, users.ErrNotFound) {
		return nil, oidcErrInternal
	}

	preferred := strings.TrimSpace(claims.PreferredUsername)
	if cfg.AutoLinkUsername && preferred != "" {
		if lu, err := s.users.GetByUsername(preferred); err == nil {
			// 自动绑定仅限未绑定的普通账号：防止 IdP 侧同名账号抢占本地超管。
			if lu.Role == models.RoleUser && lu.OIDCSubject == "" && !lu.IsDisabled {
				if err := s.users.BindOIDC(lu.ID, issuer, claims.Subject); err == nil {
					return lu, ""
				}
			}
		} else if !errors.Is(err, users.ErrNotFound) {
			return nil, oidcErrInternal
		}
	}
	if cfg.AutoCreateUsers && preferred != "" {
		username, err := users.NormalizeOIDCUsername(preferred)
		if err != nil {
			return nil, oidcErrUsernameInvalid
		}
		created, err := s.users.CreateOIDCUser(username, claims.Name, claims.Email, issuer, claims.Subject)
		if err != nil {
			if errors.Is(err, users.ErrUsernameTaken) {
				return nil, oidcErrUsernameConflict
			}
			return nil, oidcErrInternal
		}
		return created, ""
	}
	return nil, oidcErrIdentityUnbound
}

// finishOIDCBind 把 IdP 身份绑定到当前登录用户（幂等）。
func (s *Server) finishOIDCBind(w http.ResponseWriter, r *http.Request, claims *oidc.Claims) {
	ip, ua := s.proxy.ClientIP(r), r.UserAgent()
	bindFail := func(code string) {
		s.oidcAuditFailure(nil, "oidc_bind_failed", ip, ua, code)
		s.oidcRedirectError(w, r, code)
	}

	cookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		bindFail(oidcErrBindRequiresLogin)
		return
	}
	current, _, err := s.sessions.Validate(cookie.Value)
	if err != nil {
		bindFail(oidcErrBindRequiresLogin)
		return
	}
	cfg, err := s.oidcSettings.Load()
	if err != nil || !cfg.Enabled {
		bindFail(oidcErrDisabled)
		return
	}

	issuer := strings.TrimSpace(cfg.Issuer)
	existing, err := s.users.GetByOIDCIdentity(issuer, claims.Subject)
	if err != nil && !errors.Is(err, users.ErrNotFound) {
		bindFail(oidcErrInternal)
		return
	}
	if err == nil && existing.ID != current.ID {
		bindFail(oidcErrIdentityTaken)
		return
	}
	if err != nil {
		if err := s.users.BindOIDC(current.ID, issuer, claims.Subject); err != nil {
			bindFail(oidcErrIdentityTaken)
			return
		}
	}
	s.audit.Log(audit.Entry{
		ActorType: audit.ActorUser, ActorUserID: &current.ID,
		EntryType: audit.EntryWeb, Action: "oidc_bind_success",
		IPAddress: ip, UserAgent: ua, Status: audit.StatusSuccess,
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/app/settings?section=profile&sso_bound=1", http.StatusFound)
}

// --- 管理员：SSO 配置 ---

type oidcConfigResponse struct {
	Enabled          bool     `json:"enabled"`
	Issuer           string   `json:"issuer"`
	ClientID         string   `json:"client_id"`
	Scopes           []string `json:"scopes"`
	AutoLinkUsername bool     `json:"auto_link_username"`
	AutoCreateUsers  bool     `json:"auto_create_users"`
	HasSecret        bool     `json:"has_secret"`
	RedirectURI      string   `json:"redirect_uri"`
}

func (s *Server) oidcConfigResponse(cfg *oidc.Config) oidcConfigResponse {
	return oidcConfigResponse{
		Enabled: cfg.Enabled, Issuer: cfg.Issuer, ClientID: cfg.ClientID,
		Scopes:           cfg.ScopesOrDefault(),
		AutoLinkUsername: cfg.AutoLinkUsername, AutoCreateUsers: cfg.AutoCreateUsers,
		HasSecret:   cfg.ClientSecret != "",
		RedirectURI: oidc.RedirectURI(s.cfg.Server.PublicURL),
	}
}

func (s *Server) handleAdminGetOIDC(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.oidcSettings.Load()
	if err != nil {
		WriteError(w, r, CodeInternalError, "读取 SSO 配置失败", nil)
		return
	}
	WriteData(w, r, s.oidcConfigResponse(cfg))
}

// handleAdminSetOIDC 保存 SSO 配置；client_secret 留空表示保留原值（只写不读）。
func (s *Server) handleAdminSetOIDC(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled          bool     `json:"enabled"`
		Issuer           string   `json:"issuer"`
		ClientID         string   `json:"client_id"`
		ClientSecret     string   `json:"client_secret"`
		Scopes           []string `json:"scopes"`
		AutoLinkUsername bool     `json:"auto_link_username"`
		AutoCreateUsers  bool     `json:"auto_create_users"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	current, err := s.oidcSettings.Load()
	if err != nil {
		WriteError(w, r, CodeInternalError, "读取 SSO 配置失败", nil)
		return
	}
	cfg := &oidc.Config{
		Enabled: req.Enabled, Issuer: strings.TrimSpace(req.Issuer),
		ClientID: strings.TrimSpace(req.ClientID), Scopes: req.Scopes,
		AutoLinkUsername: req.AutoLinkUsername, AutoCreateUsers: req.AutoCreateUsers,
	}
	if secret := strings.TrimSpace(req.ClientSecret); secret != "" {
		cfg.ClientSecret = secret
	} else {
		cfg.ClientSecret = current.ClientSecret
	}
	if err := cfg.Validate(); err != nil {
		WriteError(w, r, CodeValidationError, err.Error(), nil)
		return
	}
	if cfg.Enabled && cfg.ClientSecret == "" {
		WriteError(w, r, CodeValidationError, "启用 SSO 时 Client Secret 不能为空", nil)
		return
	}
	if err := s.oidcSettings.Save(cfg); err != nil {
		WriteError(w, r, CodeInternalError, "保存 SSO 配置失败", nil)
		return
	}
	s.invalidateOIDCClient()
	s.adminAudit(r, "update_oidc_config", audit.StatusSuccess, "")
	WriteData(w, r, s.oidcConfigResponse(cfg))
}

// handleAdminTestOIDC 对尚未保存的 issuer 做 discovery 连通性检查。
func (s *Server) handleAdminTestOIDC(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Issuer string `json:"issuer"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	issuer := strings.TrimSpace(req.Issuer)
	if issuer == "" {
		cfg, err := s.oidcSettings.Load()
		if err != nil {
			WriteError(w, r, CodeInternalError, "读取 SSO 配置失败", nil)
			return
		}
		issuer = cfg.Issuer
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	authorization, token, err := oidc.Probe(ctx, issuer)
	if err != nil {
		WriteData(w, r, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	WriteData(w, r, map[string]any{"ok": true, "authorization_endpoint": authorization, "token_endpoint": token})
}

// --- 解除绑定 ---

// handleUnbindMyOIDC 用户自助解绑。需确认当前密码，防止会话被劫持后恶意解绑；
// 未设置本地密码的 SSO 建号账号拒绝（本地密码只能由管理员经 CLI 设置），
// 避免解绑后无法再登录。
func (s *Server) handleUnbindMyOIDC(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u := CurrentUser(r.Context())
	hash, err := s.users.PasswordHashByID(u.ID)
	if err != nil {
		s.writeUserError(w, r, err)
		return
	}
	if hash == "" {
		WriteError(w, r, CodeValidationError, "当前账号未设置本地密码，解除 SSO 绑定前请联系管理员设置密码", nil)
		return
	}
	if !auth.VerifyPassword(hash, req.Password) {
		s.audit.Log(audit.Entry{
			ActorType: audit.ActorUser, ActorUserID: &u.ID,
			EntryType: audit.EntryWeb, Action: "oidc_unbind",
			IPAddress: s.proxy.ClientIP(r), UserAgent: r.UserAgent(),
			Status: audit.StatusFailed, ErrorCode: CodeUnauthorized,
		})
		WriteError(w, r, CodeForbidden, "密码错误", nil)
		return
	}
	if err := s.users.UnbindOIDC(u.ID); err != nil {
		s.writeUserError(w, r, err)
		return
	}
	s.audit.Log(audit.Entry{
		ActorType: audit.ActorUser, ActorUserID: &u.ID,
		EntryType: audit.EntryWeb, Action: "oidc_unbind",
		IPAddress: s.proxy.ClientIP(r), UserAgent: r.UserAgent(), Status: audit.StatusSuccess,
	})
	WriteData(w, r, map[string]any{"ok": true})
}

func (s *Server) handleAdminUnbindOIDC(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		WriteError(w, r, CodeFileNotFound, "用户不存在", nil)
		return
	}
	if _, err := s.users.GetByID(id); err != nil {
		s.writeUserError(w, r, err)
		return
	}
	if err := s.users.UnbindOIDC(id); err != nil {
		s.writeUserError(w, r, err)
		return
	}
	s.adminAudit(r, "unbind_oidc", audit.StatusSuccess, "")
	WriteData(w, r, map[string]any{"ok": true})
}

// --- 内部辅助 ---

func (s *Server) invalidateOIDCClient() {
	s.oidcMu.Lock()
	defer s.oidcMu.Unlock()
	s.oidcClient, s.oidcFingerprint = nil, ""
}

func (s *Server) setOIDCFlowCookie(w http.ResponseWriter, flow oidcFlow) {
	http.SetCookie(w, &http.Cookie{
		Name:     oidcFlowCookie,
		Value:    flow.State + "." + flow.Nonce + "." + flow.Mode,
		Path:     "/api/v1/auth/oidc",
		MaxAge:   int(oidcFlowTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cfg.Security.CookieSecure,
	})
}

func (s *Server) oidcFlowFrom(r *http.Request) (oidcFlow, bool) {
	cookie, err := r.Cookie(oidcFlowCookie)
	if err != nil {
		return oidcFlow{}, false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return oidcFlow{}, false
	}
	mode := parts[2]
	if mode != "login" && mode != "bind" {
		return oidcFlow{}, false
	}
	return oidcFlow{State: parts[0], Nonce: parts[1], Mode: mode}, true
}

func (s *Server) clearOIDCFlowCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: oidcFlowCookie, Value: "", Path: "/api/v1/auth/oidc",
		MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: s.cfg.Security.CookieSecure,
	})
}

func (s *Server) oidcAuditFailure(userID *int64, action, ip, ua, errorCode string) {
	s.audit.Log(audit.Entry{
		ActorType: audit.ActorAnonymous, ActorUserID: userID,
		EntryType: audit.EntryWeb, Action: action,
		IPAddress: ip, UserAgent: ua,
		Status: audit.StatusFailed, ErrorCode: errorCode,
	})
}

// oidcRedirectError 统一失败出口：302 回登录页并携带 sso_error 码。
func (s *Server) oidcRedirectError(w http.ResponseWriter, r *http.Request, code string) {
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/login?sso_error="+url.QueryEscape(code), http.StatusFound)
}
