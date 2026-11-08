package httpserver

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omni-store/omnistore/internal/config"
	"github.com/omni-store/omnistore/internal/models"
	"github.com/omni-store/omnistore/internal/oidc"
)

const fakeOIDCClientID = "omnistore-test-client"

// fakeIDP 是进程内 mock 身份提供方：discovery + JWKS + token 端点，
// token 端点仅接受固定授权码，签发预先设置的 claims。
type fakeIDP struct {
	srv    *httptest.Server
	mu     sync.Mutex
	key    *rsa.PrivateKey
	kid    string
	issuer string
	claims map[string]any
}

func newFakeIDP(t *testing.T) *fakeIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIDP{key: key, kid: "test-key-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", f.handleDiscovery)
	mux.HandleFunc("/jwks", f.handleJWKS)
	mux.HandleFunc("/token", f.handleToken)
	f.srv = httptest.NewServer(mux)
	f.issuer = f.srv.URL
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIDP) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                f.issuer,
		"authorization_endpoint":                f.issuer + "/authorize",
		"token_endpoint":                        f.issuer + "/token",
		"jwks_uri":                              f.issuer + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (f *fakeIDP) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	pub := f.key.PublicKey
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": f.kid,
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}), // 65537
		}},
	})
}

func (f *fakeIDP) handleToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	if r.Form.Get("code") != "test-code" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "fake-access-token", "token_type": "Bearer", "expires_in": 3600,
		"id_token": f.signIDToken(),
	})
}

func (f *fakeIDP) setClaims(nonce, sub, preferred, name, email string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	f.claims = map[string]any{
		"iss": f.issuer, "sub": sub, "aud": fakeOIDCClientID,
		"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(),
		"nonce": nonce, "preferred_username": preferred, "name": name, "email": email,
	}
}

// setLegacyClaims 模拟 casdoor legacy JWT 布局：无 preferred_username，
// name=登录名，displayName claim 恒存在（可为空串，无 omitempty）。
func (f *fakeIDP) setLegacyClaims(nonce, sub, loginName, display, email string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	f.claims = map[string]any{
		"iss": f.issuer, "sub": sub, "aud": fakeOIDCClientID,
		"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(),
		"nonce": nonce, "name": loginName, "displayName": display, "email": email,
	}
}

// setBareClaims 模拟无可用登录名的布局（JWT-Custom 未选字段、第三方 IdP）：
// 既无 preferred_username 也无 displayName claim。
func (f *fakeIDP) setBareClaims(nonce, sub, email string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	f.claims = map[string]any{
		"iss": f.issuer, "sub": sub, "aud": fakeOIDCClientID,
		"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(),
		"nonce": nonce, "email": email,
	}
}

func (f *fakeIDP) signIDToken() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	header, _ := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT", "kid": f.kid})
	payload, _ := json.Marshal(f.claims)
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// configureOIDC 直接写 system_settings 启用 SSO 并重置客户端缓存。
func configureOIDC(t *testing.T, s *Server, mutate func(*oidc.Config)) {
	t.Helper()
	cfg := &oidc.Config{
		Enabled: true, Issuer: "https://idp.invalid", ClientID: fakeOIDCClientID,
		ClientSecret: "test-secret", Scopes: []string{"openid", "profile", "email"},
	}
	if mutate != nil {
		mutate(cfg)
	}
	if err := oidc.NewSettings(s.db).Save(cfg); err != nil {
		t.Fatal(err)
	}
	s.invalidateOIDCClient()
}

func serveTestRequestWithCookies(t *testing.T, handler http.Handler, method, target, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for _, c := range cookies {
		if c != nil {
			request.AddCookie(c)
		}
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func startOIDCLogin(t *testing.T, handler http.Handler, mode string, session *http.Cookie) (*url.URL, *http.Cookie) {
	t.Helper()
	target := "/api/v1/auth/oidc/login"
	if mode != "" {
		target += "?mode=" + url.QueryEscape(mode)
	}
	rec := serveTestRequestWithCookies(t, handler, http.MethodGet, target, "", session)
	if rec.Code != http.StatusFound {
		t.Fatalf("oidc login status=%d body=%s", rec.Code, rec.Body.String())
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse login redirect: %v", err)
	}
	var flowCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == oidcFlowCookie {
			flowCookie = c
		}
	}
	if flowCookie == nil {
		t.Fatalf("flow cookie missing, cookies=%+v", rec.Result().Cookies())
	}
	return location, flowCookie
}

// followOIDCCallback 模拟 IdP 302 回跳：code 固定为 fake IdP 接受的 test-code。
func followOIDCCallback(t *testing.T, handler http.Handler, location *url.URL, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	state := location.Query().Get("state")
	target := "/api/v1/auth/oidc/callback?code=test-code&state=" + url.QueryEscape(state)
	return serveTestRequestWithCookies(t, handler, http.MethodGet, target, "", cookies...)
}

func assertOIDCRedirectError(t *testing.T, rec *httptest.ResponseRecorder, errCode string) {
	t.Helper()
	if rec.Code != http.StatusFound {
		t.Fatalf("redirect status=%d body=%s", rec.Code, rec.Body.String())
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || location.Path != "/login" {
		t.Fatalf("unexpected error redirect: %q %v", rec.Header().Get("Location"), err)
	}
	if got := location.Query().Get("sso_error"); got != errCode {
		t.Fatalf("sso_error=%q want=%q", got, errCode)
	}
}

func assertOIDCLoginSuccess(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/app" {
		t.Fatalf("success redirect=%d %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookieName() && c.Value != "" {
			return c
		}
	}
	t.Fatalf("session cookie missing, cookies=%+v", rec.Result().Cookies())
	return nil
}

func TestOIDCDisabledRedirectsToError(t *testing.T) {
	server, _ := newAuthIntegrationServerWithConfig(t, nil)
	rec := serveTestRequest(t, server.Handler, http.MethodGet, "/api/v1/auth/oidc/login", "", nil, "")
	assertOIDCRedirectError(t, rec, oidcErrDisabled)
	// 无 flow cookie 的回调同样报 state 错误，不建立任何会话。
	callback := serveTestRequest(t, server.Handler, http.MethodGet, "/api/v1/auth/oidc/callback?code=x&state=y", "", nil, "")
	assertOIDCRedirectError(t, callback, oidcErrStateMismatch)
}

func TestOIDCJITLoginFlow(t *testing.T) {
	idp := newFakeIDP(t)
	server, s := newAuthIntegrationServerWithConfig(t, nil)
	// 预置管理员（setup 接口要求空表，JIT 用户存在后不可再用）。
	if _, err := s.users.Create("admin", "", "admin-password", models.RoleSuperAdmin); err != nil {
		t.Fatal(err)
	}
	configureOIDC(t, s, func(c *oidc.Config) {
		c.Issuer = idp.issuer
		c.AutoCreateUsers = true
	})

	// 完整流程：发起 → 回调 → /app。
	location, flowCookie := startOIDCLogin(t, server.Handler, "", nil)
	idp.setClaims(location.Query().Get("nonce"), "sub-joe-1", "joe", "Joe Doe", "joe@example.com")
	rec := followOIDCCallback(t, server.Handler, location, flowCookie)
	sessionCookie := assertOIDCLoginSuccess(t, rec)

	// JIT 建号结果：无密码、user 角色、SSO 已绑定。
	me := serveTestRequest(t, server.Handler, http.MethodGet, "/api/v1/auth/me", "", sessionCookie, "")
	if me.Code != http.StatusOK {
		t.Fatalf("me status=%d body=%s", me.Code, me.Body.String())
	}
	var meEnvelope struct {
		Data struct {
			User      models.User `json:"user"`
			CSRFToken string      `json:"csrf_token"`
		} `json:"data"`
	}
	decodeTestJSON(t, me, &meEnvelope)
	if meEnvelope.Data.User.Username != "joe" || meEnvelope.Data.User.Role != models.RoleUser ||
		!meEnvelope.Data.User.SSOBound || meEnvelope.Data.User.Email != "joe@example.com" {
		t.Fatalf("unexpected jit user: %+v", meEnvelope.Data.User)
	}
	// OIDC 身份明细不得出现在 API 响应中。
	if strings.Contains(me.Body.String(), "sub-joe-1") {
		t.Fatalf("oidc subject leaked: %s", me.Body.String())
	}

	// 无密码账号不能通过密码登录，也不能修改密码。
	passwordLogin := serveTestRequest(t, server.Handler, http.MethodPost, "/api/v1/auth/login",
		`{"username":"joe","password":"whatever-pass"}`, nil, "")
	assertErrorResponse(t, passwordLogin, http.StatusUnauthorized, CodeUnauthorized)
	changePassword := serveTestRequest(t, server.Handler, http.MethodPost, "/api/v1/me/password",
		`{"old_password":"","new_password":"new-password-1"}`, sessionCookie, meEnvelope.Data.CSRFToken)
	assertErrorResponse(t, changePassword, http.StatusForbidden, CodeForbidden)

	// 二次登录：同一身份直接进已有账号，不重复建号。
	location2, flowCookie2 := startOIDCLogin(t, server.Handler, "", nil)
	idp.setClaims(location2.Query().Get("nonce"), "sub-joe-1", "joe", "Joe Doe", "joe@example.com")
	rec2 := followOIDCCallback(t, server.Handler, location2, flowCookie2)
	assertOIDCLoginSuccess(t, rec2)
	if count, err := s.users.Count(); err != nil || count != 2 {
		t.Fatalf("user count=%d err=%v want 2 (admin+joe)", count, err)
	}

	// 审计记录包含成功登录。
	adminLogin := serveTestRequest(t, server.Handler, http.MethodPost, "/api/v1/auth/login",
		`{"username":"admin","password":"admin-password"}`, nil, "")
	adminCookie, adminCSRF := parseLoginResponse(t, adminLogin)
	logs := serveTestRequest(t, server.Handler, http.MethodGet, "/api/v1/admin/audit-logs?page_size=50", "", adminCookie, adminCSRF)
	if logs.Code != http.StatusOK || !strings.Contains(logs.Body.String(), "oidc_login_success") {
		t.Fatalf("audit logs missing oidc_login_success: status=%d", logs.Code)
	}
}

func TestOIDCAutoLinkAndProtectionRules(t *testing.T) {
	idp := newFakeIDP(t)
	server, s := newAuthIntegrationServerWithConfig(t, nil)
	member, err := s.users.Create("carol", "", "carol-password", models.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	rootAdmin, err := s.users.Create("rootadmin", "", "root-password-1", models.RoleSuperAdmin)
	if err != nil {
		t.Fatal(err)
	}
	configureOIDC(t, s, func(c *oidc.Config) {
		c.Issuer = idp.issuer
		c.AutoLinkUsername = true
	})

	// 同名普通账号自动绑定。
	location, flowCookie := startOIDCLogin(t, server.Handler, "", nil)
	idp.setClaims(location.Query().Get("nonce"), "sub-carol", "carol", "Carol", "carol@example.com")
	rec := followOIDCCallback(t, server.Handler, location, flowCookie)
	sessionCookie := assertOIDCLoginSuccess(t, rec)
	me := serveTestRequest(t, server.Handler, http.MethodGet, "/api/v1/auth/me", "", sessionCookie, "")
	var meEnvelope struct {
		Data struct {
			User models.User `json:"user"`
		} `json:"data"`
	}
	decodeTestJSON(t, me, &meEnvelope)
	if meEnvelope.Data.User.ID != member.ID || !meEnvelope.Data.User.SSOBound {
		t.Fatalf("auto-link should bind carol: %+v", meEnvelope.Data.User)
	}

	// 超管账号绝不允许自动绑定，也不允许 JIT 抢占。
	location2, flowCookie2 := startOIDCLogin(t, server.Handler, "", nil)
	idp.setClaims(location2.Query().Get("nonce"), "sub-root", "rootadmin", "Root", "root@example.com")
	rec2 := followOIDCCallback(t, server.Handler, location2, flowCookie2)
	assertOIDCRedirectError(t, rec2, oidcErrIdentityUnbound)
	if u, err := s.users.GetByID(rootAdmin.ID); err != nil || u.SSOBound {
		t.Fatalf("super admin must stay unbound: %+v %v", u, err)
	}

	// 被禁用账号即使已绑定也拒绝登录。
	if err := s.users.SetDisabled(member.ID, true); err != nil {
		t.Fatal(err)
	}
	location3, flowCookie3 := startOIDCLogin(t, server.Handler, "", nil)
	idp.setClaims(location3.Query().Get("nonce"), "sub-carol", "carol", "Carol", "carol@example.com")
	rec3 := followOIDCCallback(t, server.Handler, location3, flowCookie3)
	assertOIDCRedirectError(t, rec3, oidcErrAccountDisabled)
}

func TestOIDCFlowValidation(t *testing.T) {
	idp := newFakeIDP(t)
	server, s := newAuthIntegrationServerWithConfig(t, nil)
	configureOIDC(t, s, func(c *oidc.Config) {
		c.Issuer = idp.issuer
		c.AutoCreateUsers = true
	})

	// state 不匹配（伪造回调），不建号不建会话。
	location, flowCookie := startOIDCLogin(t, server.Handler, "", nil)
	idp.setClaims(location.Query().Get("nonce"), "sub-x", "x", "X", "x@example.com")
	rec := serveTestRequestWithCookies(t, server.Handler, http.MethodGet,
		"/api/v1/auth/oidc/callback?code=test-code&state=tampered-state", "", flowCookie)
	assertOIDCRedirectError(t, rec, oidcErrStateMismatch)
	if count, err := s.users.Count(); err != nil || count != 0 {
		t.Fatalf("no user should be created on state mismatch: %d %v", count, err)
	}

	// nonce 不匹配（重放/伪造 ID token）。
	location2, flowCookie2 := startOIDCLogin(t, server.Handler, "", nil)
	idp.setClaims("different-nonce", "sub-x", "x", "X", "x@example.com")
	rec2 := followOIDCCallback(t, server.Handler, location2, flowCookie2)
	assertOIDCRedirectError(t, rec2, oidcErrExchangeFailed)

	// 用户名清洗后不合法。
	location3, flowCookie3 := startOIDCLogin(t, server.Handler, "", nil)
	idp.setClaims(location3.Query().Get("nonce"), "sub-bad", "--__--", "Bad", "bad@example.com")
	rec3 := followOIDCCallback(t, server.Handler, location3, flowCookie3)
	assertOIDCRedirectError(t, rec3, oidcErrUsernameInvalid)

	// 自动建号关闭时未绑定身份被拒绝。
	configureOIDC(t, s, func(c *oidc.Config) {
		c.Issuer = idp.issuer
		c.AutoLinkUsername = false
		c.AutoCreateUsers = false
	})
	location4, flowCookie4 := startOIDCLogin(t, server.Handler, "", nil)
	idp.setClaims(location4.Query().Get("nonce"), "sub-unknown", "stranger", "Stranger", "s@example.com")
	rec4 := followOIDCCallback(t, server.Handler, location4, flowCookie4)
	assertOIDCRedirectError(t, rec4, oidcErrIdentityUnbound)
}

// casdoor legacy 布局（name=登录名、displayName 恒存在）：自动绑定与 JIT 均可用。
func TestOIDCLegacyTokenLayout(t *testing.T) {
	idp := newFakeIDP(t)
	server, s := newAuthIntegrationServerWithConfig(t, nil)
	if _, err := s.users.Create("carol", "Carol 显示名", "carol-password", models.RoleUser); err != nil {
		t.Fatal(err)
	}
	configureOIDC(t, s, func(c *oidc.Config) {
		c.Issuer = idp.issuer
		c.AutoLinkUsername = true
		c.AutoCreateUsers = true
	})

	// 自动绑定：legacy 布局的 name 命中同名普通账号。
	location, flowCookie := startOIDCLogin(t, server.Handler, "", nil)
	idp.setLegacyClaims(location.Query().Get("nonce"), "sub-legacy-carol", "carol", "Carol 显示名", "carol@example.com")
	rec := followOIDCCallback(t, server.Handler, location, flowCookie)
	sessionCookie := assertOIDCLoginSuccess(t, rec)
	me := serveTestRequest(t, server.Handler, http.MethodGet, "/api/v1/auth/me", "", sessionCookie, "")
	var meEnvelope struct {
		Data struct {
			User models.User `json:"user"`
		} `json:"data"`
	}
	decodeTestJSON(t, me, &meEnvelope)
	if meEnvelope.Data.User.ID == 0 || meEnvelope.Data.User.Username != "carol" || !meEnvelope.Data.User.SSOBound {
		t.Fatalf("legacy auto-link failed: %+v", meEnvelope.Data.User)
	}

	// JIT：legacy 布局且无同名校友 → 建号，显示名取 displayName。
	location2, flowCookie2 := startOIDCLogin(t, server.Handler, "", nil)
	idp.setLegacyClaims(location2.Query().Get("nonce"), "sub-legacy-hank", "hank", "Hank", "hank@example.com")
	rec2 := followOIDCCallback(t, server.Handler, location2, flowCookie2)
	assertOIDCLoginSuccess(t, rec2)
	created, err := s.users.GetByUsername("hank")
	if err != nil || created.Email != "hank@example.com" || created.DisplayName != "Hank" {
		t.Fatalf("legacy JIT failed: %+v %v", created, err)
	}

	// 显示名为空串的 legacy 用户（displayName claim 存在但为 ""）→ 回退登录名。
	location3, flowCookie3 := startOIDCLogin(t, server.Handler, "", nil)
	idp.setLegacyClaims(location3.Query().Get("nonce"), "sub-legacy-nodisplay", "nodisplay", "", "nd@example.com")
	rec3 := followOIDCCallback(t, server.Handler, location3, flowCookie3)
	assertOIDCLoginSuccess(t, rec3)
	created2, err := s.users.GetByUsername("nodisplay")
	if err != nil || created2.DisplayName != "nodisplay" {
		t.Fatalf("legacy empty display fallback failed: %+v %v", created2, err)
	}
}

// 无可用登录名的布局：JIT 开启时显式失败，绝不把 name 全名当登录名。
func TestOIDCUsernameMissingLayout(t *testing.T) {
	idp := newFakeIDP(t)
	server, s := newAuthIntegrationServerWithConfig(t, nil)
	configureOIDC(t, s, func(c *oidc.Config) {
		c.Issuer = idp.issuer
		c.AutoCreateUsers = true
	})
	location, flowCookie := startOIDCLogin(t, server.Handler, "", nil)
	idp.setBareClaims(location.Query().Get("nonce"), "sub-bare", "bare@example.com")
	rec := followOIDCCallback(t, server.Handler, location, flowCookie)
	assertOIDCRedirectError(t, rec, oidcErrUsernameMissing)
	if count, err := s.users.Count(); err != nil || count != 0 {
		t.Fatalf("no user should be created: %d %v", count, err)
	}
}

func TestOIDCBindFlow(t *testing.T) {
	idp := newFakeIDP(t)
	server, s := newAuthIntegrationServerWithConfig(t, nil)
	configureOIDC(t, s, func(c *oidc.Config) { c.Issuer = idp.issuer })

	// 管理员密码登录后发起绑定。
	setup := serveTestRequest(t, server.Handler, http.MethodPost, "/api/v1/setup/admin",
		`{"username":"admin","display_name":"Admin","password":"admin-password","bootstrap_token":"integration-bootstrap-token"}`, nil, "")
	if setup.Code != http.StatusOK {
		t.Fatalf("setup status=%d", setup.Code)
	}
	adminLogin := serveTestRequest(t, server.Handler, http.MethodPost, "/api/v1/auth/login",
		`{"username":"admin","password":"admin-password"}`, nil, "")
	adminCookie, adminCSRF := parseLoginResponse(t, adminLogin)

	// 未登录发起绑定被拒绝。
	recNoSession := serveTestRequest(t, server.Handler, http.MethodGet, "/api/v1/auth/oidc/login?mode=bind", "", nil, "")
	assertOIDCRedirectError(t, recNoSession, oidcErrBindRequiresLogin)

	location, flowCookie := startOIDCLogin(t, server.Handler, "bind", adminCookie)
	idp.setClaims(location.Query().Get("nonce"), "sub-admin", "admin", "Admin", "admin@example.com")
	rec := followOIDCCallback(t, server.Handler, location, flowCookie, adminCookie)
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/app/settings?section=profile&sso_bound=1") {
		t.Fatalf("bind redirect=%d %q", rec.Code, rec.Header().Get("Location"))
	}
	me := serveTestRequest(t, server.Handler, http.MethodGet, "/api/v1/auth/me", "", adminCookie, "")
	if !strings.Contains(me.Body.String(), `"sso_bound":true`) {
		t.Fatalf("admin should be bound: %s", me.Body.String())
	}

	// 管理员先自助解绑，普通用户占用该身份后再次绑定冲突。
	unbind := serveTestRequest(t, server.Handler, http.MethodPost, "/api/v1/me/oidc/unbind", `{"password":"admin-password"}`, adminCookie, adminCSRF)
	if unbind.Code != http.StatusOK {
		t.Fatalf("unbind status=%d body=%s", unbind.Code, unbind.Body.String())
	}
	member, err := s.users.Create("mallory", "", "mallory-password", models.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.users.BindOIDC(member.ID, idp.issuer, "sub-admin"); err != nil {
		t.Fatal(err)
	}
	location2, flowCookie2 := startOIDCLogin(t, server.Handler, "bind", adminCookie)
	idp.setClaims(location2.Query().Get("nonce"), "sub-admin", "admin", "Admin", "admin@example.com")
	rec2 := followOIDCCallback(t, server.Handler, location2, flowCookie2, adminCookie)
	assertOIDCRedirectError(t, rec2, oidcErrIdentityTaken)

	// 管理员强制解绑普通用户。
	adminUnbind := serveTestRequest(t, server.Handler, http.MethodPost, "/api/v1/admin/users/"+itoaTest(member.ID)+"/unbind-oidc", "", adminCookie, adminCSRF)
	if adminUnbind.Code != http.StatusOK {
		t.Fatalf("admin unbind status=%d body=%s", adminUnbind.Code, adminUnbind.Body.String())
	}
	if u, err := s.users.GetByID(member.ID); err != nil || u.SSOBound {
		t.Fatalf("member should be unbound: %+v %v", u, err)
	}
}

func TestOIDCRateLimitOnCallback(t *testing.T) {
	idp := newFakeIDP(t)
	server, s := newAuthIntegrationServerWithConfig(t, func(cfg *config.Config) {
		cfg.Security.LoginRateLimit.WindowMinutes = 1
		cfg.Security.LoginRateLimit.MaxFailuresPerIP = 2
	})
	configureOIDC(t, s, func(c *oidc.Config) { c.Issuer = idp.issuer })

	for range 2 {
		location, flowCookie := startOIDCLogin(t, server.Handler, "", nil)
		idp.setClaims(location.Query().Get("nonce"), "sub-rl", "rl", "RL", "rl@example.com")
		// 授权码无效 → 交换失败，消耗 IP 额度。
		rec := serveTestRequestWithCookies(t, server.Handler, http.MethodGet,
			"/api/v1/auth/oidc/callback?code=bad-code&state="+url.QueryEscape(location.Query().Get("state")), "", flowCookie)
		assertOIDCRedirectError(t, rec, oidcErrExchangeFailed)
	}
	location, flowCookie := startOIDCLogin(t, server.Handler, "", nil)
	idp.setClaims(location.Query().Get("nonce"), "sub-rl", "rl", "RL", "rl@example.com")
	rec := followOIDCCallback(t, server.Handler, location, flowCookie)
	assertOIDCRedirectError(t, rec, oidcErrRateLimited)
}

func TestAdminOIDCConfigAPI(t *testing.T) {
	server, s := newAuthIntegrationServerWithConfig(t, nil)

	// 未认证不可访问；setup 需在创建普通用户之前。
	anonGet := serveTestRequest(t, server.Handler, http.MethodGet, "/api/v1/admin/oidc", "", nil, "")
	assertErrorResponse(t, anonGet, http.StatusUnauthorized, CodeUnauthorized)
	setup := serveTestRequest(t, server.Handler, http.MethodPost, "/api/v1/setup/admin",
		`{"username":"admin","display_name":"Admin","password":"admin-password","bootstrap_token":"integration-bootstrap-token"}`, nil, "")
	if setup.Code != http.StatusOK {
		t.Fatalf("setup status=%d", setup.Code)
	}
	if _, err := s.users.Create("member", "", "member-password", models.RoleUser); err != nil {
		t.Fatal(err)
	}
	memberLogin := serveTestRequest(t, server.Handler, http.MethodPost, "/api/v1/auth/login",
		`{"username":"member","password":"member-password"}`, nil, "")
	memberCookie, _ := parseLoginResponse(t, memberLogin)
	memberGet := serveTestRequest(t, server.Handler, http.MethodGet, "/api/v1/admin/oidc", "", memberCookie, "")
	assertErrorResponse(t, memberGet, http.StatusForbidden, CodeForbidden)

	adminLogin := serveTestRequest(t, server.Handler, http.MethodPost, "/api/v1/auth/login",
		`{"username":"admin","password":"admin-password"}`, nil, "")
	adminCookie, adminCSRF := parseLoginResponse(t, adminLogin)

	// 默认配置：未启用，回调地址由 PublicURL 派生。
	defaultGet := serveTestRequest(t, server.Handler, http.MethodGet, "/api/v1/admin/oidc", "", adminCookie, adminCSRF)
	if !strings.Contains(defaultGet.Body.String(), `"redirect_uri":"http://example.test/api/v1/auth/oidc/callback"`) ||
		!strings.Contains(defaultGet.Body.String(), `"has_secret":false`) {
		t.Fatalf("unexpected default config: %s", defaultGet.Body.String())
	}
	authStatus := serveTestRequest(t, server.Handler, http.MethodGet, "/api/v1/auth/status", "", nil, "")
	if !strings.Contains(authStatus.Body.String(), `"oidc_enabled":false`) {
		t.Fatalf("auth status should expose oidc_enabled=false: %s", authStatus.Body.String())
	}

	// 保存配置；响应不得回传 client_secret。
	save := serveTestRequest(t, server.Handler, http.MethodPut, "/api/v1/admin/oidc",
		`{"enabled":true,"issuer":"https://idp.invalid","client_id":"cid","client_secret":"super-secret-value","scopes":["openid"],"auto_create_users":true}`,
		adminCookie, adminCSRF)
	if save.Code != http.StatusOK || strings.Contains(save.Body.String(), "super-secret-value") {
		t.Fatalf("save status=%d body=%s", save.Code, save.Body.String())
	}
	// 留空 secret 表示保留原值。
	keepSave := serveTestRequest(t, server.Handler, http.MethodPut, "/api/v1/admin/oidc",
		`{"enabled":true,"issuer":"https://idp.invalid","client_id":"cid"}`, adminCookie, adminCSRF)
	if keepSave.Code != http.StatusOK || !strings.Contains(keepSave.Body.String(), `"has_secret":true`) {
		t.Fatalf("keep secret status=%d body=%s", keepSave.Code, keepSave.Body.String())
	}
	if cfg, err := s.oidcSettings.Load(); err != nil || cfg.ClientSecret != "super-secret-value" {
		t.Fatalf("secret not kept: %+v %v", cfg, err)
	}
	// auth/status 随配置生效。
	authStatus2 := serveTestRequest(t, server.Handler, http.MethodGet, "/api/v1/auth/status", "", nil, "")
	if !strings.Contains(authStatus2.Body.String(), `"oidc_enabled":true`) {
		t.Fatalf("auth status should expose oidc_enabled=true: %s", authStatus2.Body.String())
	}
	// 非法 issuer 拒绝。
	badSave := serveTestRequest(t, server.Handler, http.MethodPut, "/api/v1/admin/oidc",
		`{"enabled":true,"issuer":"ftp://bad","client_id":"cid"}`, adminCookie, adminCSRF)
	assertErrorResponse(t, badSave, http.StatusBadRequest, CodeValidationError)

	// 测试连接：指向 fake IdP 成功，指向死地址失败。
	idp := newFakeIDP(t)
	testOK := serveTestRequest(t, server.Handler, http.MethodPost, "/api/v1/admin/oidc/test",
		`{"issuer":"`+idp.issuer+`"}`, adminCookie, adminCSRF)
	if testOK.Code != http.StatusOK || !strings.Contains(testOK.Body.String(), `"ok":true`) {
		t.Fatalf("probe should succeed: %d %s", testOK.Code, testOK.Body.String())
	}
	testBad := serveTestRequest(t, server.Handler, http.MethodPost, "/api/v1/admin/oidc/test",
		`{"issuer":"http://127.0.0.1:1"}`, adminCookie, adminCSRF)
	if testBad.Code != http.StatusOK || !strings.Contains(testBad.Body.String(), `"ok":false`) {
		t.Fatalf("probe should fail gracefully: %d %s", testBad.Code, testBad.Body.String())
	}
}
