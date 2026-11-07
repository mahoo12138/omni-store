package httpserver

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/omni-store/omnistore/internal/audit"
	"github.com/omni-store/omnistore/internal/auth"
	"github.com/omni-store/omnistore/internal/users"
)

// SessionCookieName 返回登录态 Cookie 名称。
func SessionCookieName() string {
	return auth.SessionCookieName
}

func (s *Server) setSessionCookie(w http.ResponseWriter, sessionID string) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    sessionID,
		Path:     "/",
		MaxAge:   int(s.sessions.TTL().Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cfg.Security.CookieSecure,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cfg.Security.CookieSecure,
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		WriteError(w, r, CodeValidationError, "请求体格式错误", nil)
		return false
	}
	return true
}

// --- 初始化超级管理员（README §8.2） ---

// handleSetupStatus 返回系统是否已初始化。
func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.users.Count()
	if err != nil {
		WriteError(w, r, CodeInternalError, "查询失败", nil)
		return
	}
	WriteData(w, r, map[string]any{"initialized": n > 0})
}

// handleSetupAdmin 创建第一个超级管理员。仅在没有任何用户时可用。
func (s *Server) handleSetupAdmin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username       string `json:"username"`
		DisplayName    string `json:"display_name"`
		Password       string `json:"password"`
		BootstrapToken string `json:"bootstrap_token"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.BootstrapToken), []byte(s.bootstrapToken)) != 1 {
		WriteError(w, r, CodeForbidden, "初始化凭据无效", nil)
		return
	}

	u, err := s.users.CreateFirstAdmin(req.Username, req.DisplayName, req.Password)
	if err != nil {
		if errors.Is(err, users.ErrAlreadyInitialized) {
			WriteError(w, r, CodeForbidden, err.Error(), nil)
			return
		}
		s.writeUserError(w, r, err)
		return
	}

	s.audit.Log(audit.Entry{
		ActorType: audit.ActorUser, ActorUserID: &u.ID,
		EntryType: audit.EntryAdmin, Action: "create_user",
		IPAddress: s.proxy.ClientIP(r), UserAgent: r.UserAgent(),
		Status: audit.StatusSuccess,
	})
	WriteData(w, r, u)
}

// --- 登录 / 退出 / 当前用户 ---

// handleAuthStatus 供公开页面无副作用地判断当前浏览器是否已登录。
// 未登录和会话过期都返回 200，避免公开页面为了渲染入口制造 401 控制台噪声。
// oidc_enabled 供登录页决定是否展示 SSO 登录入口。
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		WriteData(w, r, map[string]any{"authenticated": false, "oidc_enabled": s.oidcEnabled()})
		return
	}
	if _, _, err := s.sessions.Validate(cookie.Value); err != nil {
		if errors.Is(err, auth.ErrSessionInvalid) {
			s.clearSessionCookie(w)
			WriteData(w, r, map[string]any{"authenticated": false, "oidc_enabled": s.oidcEnabled()})
			return
		}
		WriteError(w, r, CodeInternalError, "查询登录状态失败", nil)
		return
	}
	WriteData(w, r, map[string]any{"authenticated": true, "oidc_enabled": s.oidcEnabled()})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	ip := s.proxy.ClientIP(r)
	ua := r.UserAgent()
	attempt, retryAfter, allowed := s.loginLimiter.Begin(ip, req.Username)
	if !allowed {
		seconds := int((retryAfter + time.Second - 1) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		s.audit.Log(audit.Entry{
			ActorType: audit.ActorAnonymous,
			EntryType: audit.EntryWeb, Action: "login_failed",
			IPAddress: ip, UserAgent: ua,
			Status: audit.StatusFailed, ErrorCode: CodeRateLimited,
		})
		WriteError(w, r, CodeRateLimited, "登录尝试过于频繁，请稍后再试", nil)
		return
	}
	attemptFinished := false
	defer func() {
		if !attemptFinished {
			s.loginLimiter.Cancel(attempt)
		}
	}()

	fail := func(userID *int64) {
		attemptFinished = true
		retryAfter, allowed := s.loginLimiter.Failure(attempt)
		errorCode := CodeUnauthorized
		if !allowed {
			errorCode = CodeRateLimited
		}
		s.audit.Log(audit.Entry{
			ActorType: audit.ActorAnonymous, ActorUserID: userID,
			EntryType: audit.EntryWeb, Action: "login_failed",
			IPAddress: ip, UserAgent: ua,
			Status: audit.StatusFailed, ErrorCode: errorCode,
		})
		if !allowed {
			seconds := int((retryAfter + time.Second - 1) / time.Second)
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			WriteError(w, r, CodeRateLimited, "登录尝试过于频繁，请稍后再试", nil)
			return
		}
		WriteError(w, r, CodeUnauthorized, "用户名或密码错误", nil)
	}

	u, err := s.users.GetByUsername(req.Username)
	if err != nil {
		if errors.Is(err, users.ErrNotFound) {
			// 使用与真实密码一致的 bcrypt cost，避免用户名枚举时间差。
			auth.VerifyLoginPassword("", req.Password)
			fail(nil)
			return
		}
		WriteError(w, r, CodeInternalError, "登录失败", nil)
		return
	}

	hash, err := s.users.PasswordHashByUsername(req.Username)
	if err != nil {
		WriteError(w, r, CodeInternalError, "登录失败", nil)
		return
	}
	if !auth.VerifyLoginPassword(hash, req.Password) {
		fail(&u.ID)
		return
	}
	if u.IsDisabled {
		fail(&u.ID)
		return
	}

	sessionID, csrfToken, err := s.sessions.Create(u.ID, ua, ip)
	if err != nil {
		WriteError(w, r, CodeInternalError, "创建会话失败", nil)
		return
	}
	s.loginLimiter.Success(attempt)
	attemptFinished = true
	s.setSessionCookie(w, sessionID)

	s.audit.Log(audit.Entry{
		ActorType: audit.ActorUser, ActorUserID: &u.ID,
		EntryType: audit.EntryWeb, Action: "login_success",
		IPAddress: ip, UserAgent: ua, Status: audit.StatusSuccess,
	})
	WriteData(w, r, map[string]any{"user": u, "csrf_token": csrfToken})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
		_ = s.sessions.Delete(cookie.Value)
	}
	s.clearSessionCookie(w)
	WriteData(w, r, map[string]any{"ok": true})
}

// handleMe 返回当前用户和 Session 级稳定的 CSRF Token（SPA 刷新后恢复登录态用）。
// GET 不修改 CSRF 状态，避免多个标签页互相使 Token 失效。
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	sess := currentSession(r.Context())
	WriteData(w, r, map[string]any{
		"user":       CurrentUser(r.Context()),
		"csrf_token": s.sessions.CSRFToken(sess.SessionID),
	})
}

func (s *Server) writeUserError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, users.ErrUsernameTaken):
		WriteError(w, r, CodeConflict, err.Error(), nil)
	case errors.Is(err, users.ErrInvalidUsername), errors.Is(err, users.ErrWeakPassword):
		WriteError(w, r, CodeValidationError, err.Error(), nil)
	case errors.Is(err, users.ErrNotFound):
		WriteError(w, r, CodeFileNotFound, err.Error(), nil)
	default:
		WriteError(w, r, CodeInternalError, "操作失败", nil)
	}
}
