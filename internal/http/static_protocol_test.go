package httpserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/omni-store/omnistore/internal/sources"
)

func TestStaticHTTPSourceIsolationAndPublicPreflight(t *testing.T) {
	f := newStaticHTTPFixture(t)
	first := f.configure(t, `{"publish_root":"blog-assets"}`)
	if first.Code != http.StatusOK {
		t.Fatal(first.Body.String())
	}
	firstID := decodeStaticConfig(t, first)["public_asset_id"].(string)
	if resp := serveTestRequest(t, f.handler, http.MethodPut, "/api/v1/admin/sources/"+f.source.Key+"/static-access/config",
		`{"publish_root":"other"}`, f.cookie, f.csrf); resp.Code != http.StatusConflict || !strings.Contains(resp.Body.String(), CodeStaticRebindRequired) {
		t.Fatalf("silent publication change status=%d body=%s", resp.Code, resp.Body.String())
	}
	root := filepath.Join(filepath.Dir(f.sourceRoot), "second")
	if err := os.MkdirAll(filepath.Join(root, "media"), 0o755); err != nil {
		t.Fatal(err)
	}
	second, err := f.app.sources.Create(sources.CreateInput{Name: "Second", RootPath: root, ImportExisting: true})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/admin/sources/" + second.Key + "/static-access/config"
	secondResp := serveTestRequest(t, f.handler, http.MethodPost, path,
		`{"publish_root":"media","cache_mode":"no-cache","cors_mode":"allowlist","allowed_origins":["https://second.example.test"]}`, f.cookie, f.csrf)
	if secondResp.Code != http.StatusOK {
		t.Fatal(secondResp.Body.String())
	}
	secondID := decodeStaticConfig(t, secondResp)["public_asset_id"].(string)
	if err := os.WriteFile(filepath.Join(f.sourceRoot, "blog-assets/a.png"), []byte(staticPNG+"first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "media/a.png"), []byte(staticPNG+"second"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ id, suffix, cors, cache string }{
		{firstID, "first", "*", "public, max-age=300"},
		{secondID, "second", "https://second.example.test", "no-cache"},
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
			req := httptest.NewRequest(method, "/assets/"+tc.id+"/a.png", nil)
			req.Header.Set("Origin", "https://second.example.test")
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, req)
			wantStatus := http.StatusOK
			if method == http.MethodOptions {
				wantStatus = http.StatusNoContent
			}
			if rec.Code != wantStatus || rec.Header().Get("Access-Control-Allow-Origin") != tc.cors {
				t.Fatalf("%s %s status=%d headers=%v", method, tc.id, rec.Code, rec.Header())
			}
			if method == http.MethodGet && rec.Body.String() != staticPNG+tc.suffix {
				t.Fatalf("wrong source content=%q", rec.Body.String())
			}
			if method != http.MethodGet && rec.Body.Len() != 0 {
				t.Fatal("HEAD/OPTIONS must have no body")
			}
			if method != http.MethodOptions && rec.Header().Get("Cache-Control") != tc.cache {
				t.Fatal("wrong source cache")
			}
		}
	}
	// 404 适用于未知 ID、内部目录、目录请求；OPTIONS 也必须应用发布授权。
	for _, suffix := range []string{"ast-00000000000000000000000000000000/a.png", secondID + "/.omnistore/a.png", secondID + "/"} {
		resp := serveTestRequest(t, f.handler, http.MethodOptions, "/assets/"+suffix, "", nil, "")
		if resp.Code != http.StatusNotFound || resp.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("preflight %s status=%d headers=%v", suffix, resp.Code, resp.Header())
		}
	}
	if err := f.app.sources.SetDisabled(second.Key, true); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodOptions} {
		resp := serveTestRequest(t, f.handler, method, "/assets/"+secondID+"/a.png", "", nil, "")
		if resp.Code != http.StatusNotFound {
			t.Fatalf("disabled source %s=%d", method, resp.Code)
		}
	}
	// 关闭/移除第二个协议不会改变第一源，普通管理员 API 无匿名写入。
	if resp := serveTestRequest(t, f.handler, http.MethodPut, path, `{"enabled":false}`, nil, ""); resp.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous config write=%d", resp.Code)
	}
	if resp := serveTestRequest(t, f.handler, http.MethodPut, path, `{"enabled":false}`, f.cookie, f.csrf); resp.Code != http.StatusOK {
		t.Fatal(resp.Body.String())
	}
	cfg := serveTestRequest(t, f.handler, http.MethodGet, path, "", f.cookie, "")
	rev := decodeStaticConfig(t, cfg)["revision"]
	if resp := serveTestRequest(t, f.handler, http.MethodDelete, path, fmt.Sprintf(`{"expected_revision":%v}`, rev), f.cookie, f.csrf); resp.Code != http.StatusOK {
		t.Fatal(resp.Body.String())
	}
	if resp := serveTestRequest(t, f.handler, http.MethodGet, "/assets/"+firstID+"/a.png", "", nil, ""); resp.Code != http.StatusOK {
		t.Fatalf("first source affected=%d", resp.Code)
	}
	if resp := serveTestRequest(t, f.handler, http.MethodGet, "/api/v1/admin/capabilities", "", f.cookie, ""); strings.Contains(resp.Body.String(), "static_assets") {
		t.Fatal("static access leaked into site capability list")
	}
}

func TestPatchSourceStaticEnabledLifecycle(t *testing.T) {
	f := newStaticHTTPFixture(t)
	if err := os.WriteFile(filepath.Join(f.sourceRoot, "blog-assets/direct.png"), []byte(staticPNG+"quick"), 0o600); err != nil {
		t.Fatal(err)
	}

	sourcePath := "/api/v1/admin/sources/" + f.source.Key
	enableResp := serveTestRequest(t, f.handler, http.MethodPatch, sourcePath, `{"static_enabled":true}`, f.cookie, f.csrf)
	if enableResp.Code != http.StatusOK {
		t.Fatalf("enable via PATCH status=%d body=%s", enableResp.Code, enableResp.Body.String())
	}
	if !strings.Contains(enableResp.Body.String(), `"static_enabled":true`) {
		t.Fatalf("expected static_enabled=true in response: %s", enableResp.Body.String())
	}

	cfgResp := serveTestRequest(t, f.handler, http.MethodGet, sourcePath+"/static-access/config", "", f.cookie, "")
	if cfgResp.Code != http.StatusOK {
		t.Fatalf("get config status=%d body=%s", cfgResp.Code, cfgResp.Body.String())
	}
	cfg := decodeStaticConfig(t, cfgResp)
	assetID, _ := cfg["public_asset_id"].(string)
	if assetID == "" || cfg["publish_root"] != "" || cfg["enabled"] != true {
		t.Fatalf("unexpected auto config=%+v", cfg)
	}

	// 开启后立即可通过根路径访问。
	if get := serveTestRequest(t, f.handler, http.MethodGet, "/assets/"+assetID+"/blog-assets/direct.png", "", nil, ""); get.Code != http.StatusOK || get.Body.String() != staticPNG+"quick" {
		t.Fatalf("GET after quick enable status=%d body=%q", get.Code, get.Body.String())
	}

	// 关闭后 404，再次开启保留同一 public_asset_id。
	if resp := serveTestRequest(t, f.handler, http.MethodPatch, sourcePath, `{"static_enabled":false}`, f.cookie, f.csrf); resp.Code != http.StatusOK {
		t.Fatalf("disable via PATCH status=%d body=%s", resp.Code, resp.Body.String())
	}
	if get := serveTestRequest(t, f.handler, http.MethodGet, "/assets/"+assetID+"/blog-assets/direct.png", "", nil, ""); get.Code != http.StatusNotFound {
		t.Fatalf("GET after disable status=%d, want 404", get.Code)
	}
	if resp := serveTestRequest(t, f.handler, http.MethodPatch, sourcePath, `{"static_enabled":true}`, f.cookie, f.csrf); resp.Code != http.StatusOK {
		t.Fatalf("re-enable via PATCH status=%d body=%s", resp.Code, resp.Body.String())
	}
	cfgAfter := decodeStaticConfig(t, serveTestRequest(t, f.handler, http.MethodGet, sourcePath+"/static-access/config", "", f.cookie, ""))
	if cfgAfter["public_asset_id"] != assetID {
		t.Fatalf("re-enable changed asset id: got %v want %s", cfgAfter["public_asset_id"], assetID)
	}

	// 启用时不可删除源；关闭后可直接删除源（无需额外手动移除静态配置）。
	if del := serveTestRequest(t, f.handler, http.MethodDelete, sourcePath, "", f.cookie, f.csrf); del.Code != http.StatusConflict {
		t.Fatalf("delete while static enabled status=%d, want 409", del.Code)
	}
	if resp := serveTestRequest(t, f.handler, http.MethodPatch, sourcePath, `{"static_enabled":false}`, f.cookie, f.csrf); resp.Code != http.StatusOK {
		t.Fatalf("disable before delete status=%d", resp.Code)
	}
	if del := serveTestRequest(t, f.handler, http.MethodDelete, sourcePath, "", f.cookie, f.csrf); del.Code != http.StatusOK {
		t.Fatalf("delete after disable status=%d body=%s", del.Code, del.Body.String())
	}
}

func TestGlobalStaticHTTPSettingsSyncAndDefaults(t *testing.T) {
	f := newStaticHTTPFixture(t)
	if err := os.WriteFile(filepath.Join(f.sourceRoot, "blog-assets/cdn.png"), []byte(staticPNG+"cdn"), 0o600); err != nil {
		t.Fatal(err)
	}

	settingsPath := "/api/v1/admin/static-access/settings"
	update := serveTestRequest(t, f.handler, http.MethodPut, settingsPath,
		`{"public_origin":"https://cdn.example.test","cache_mode":"long","cors_mode":"allowlist","allowed_origins":["https://blog.example.test"]}`,
		f.cookie, f.csrf)
	if update.Code != http.StatusOK {
		t.Fatalf("update global static settings status=%d body=%s", update.Code, update.Body.String())
	}

	// 新开启静态 HTTP 的存储源自动继承全局 CDN / 缓存 / 跨域参数。
	sourcePath := "/api/v1/admin/sources/" + f.source.Key
	if resp := serveTestRequest(t, f.handler, http.MethodPatch, sourcePath, `{"static_enabled":true}`, f.cookie, f.csrf); resp.Code != http.StatusOK {
		t.Fatalf("enable static status=%d body=%s", resp.Code, resp.Body.String())
	}

	getSettings := serveTestRequest(t, f.handler, http.MethodGet, settingsPath, "", f.cookie, "")
	if getSettings.Code != http.StatusOK {
		t.Fatalf("get global static settings status=%d body=%s", getSettings.Code, getSettings.Body.String())
	}
	view := decodeStaticConfig(t, getSettings)
	sourcesList, _ := view["sources"].([]any)
	if len(sourcesList) != 1 {
		t.Fatalf("expected 1 enabled static source, got %v", view["sources"])
	}
	first := sourcesList[0].(map[string]any)
	assetID := first["public_asset_id"].(string)
	if first["base_url"] != "https://cdn.example.test/assets/"+assetID+"/" {
		t.Fatalf("unexpected cdn base_url=%v", first["base_url"])
	}

	req := httptest.NewRequest(http.MethodGet, "/assets/"+assetID+"/blog-assets/cdn.png", nil)
	req.Header.Set("Origin", "https://blog.example.test")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || rec.Header().Get("Access-Control-Allow-Origin") != "https://blog.example.test" {
		t.Fatalf("served headers status=%d headers=%v", rec.Code, rec.Header())
	}

	// 再次修改全局参数会同步到已开启的存储源。
	update2 := serveTestRequest(t, f.handler, http.MethodPut, settingsPath,
		`{"public_origin":"","cache_mode":"no-cache","cors_mode":"public","allowed_origins":[]}`,
		f.cookie, f.csrf)
	if update2.Code != http.StatusOK {
		t.Fatalf("second update status=%d body=%s", update2.Code, update2.Body.String())
	}
	rec2 := serveTestRequest(t, f.handler, http.MethodGet, "/assets/"+assetID+"/blog-assets/cdn.png", "", nil, "")
	if rec2.Code != http.StatusOK || rec2.Header().Get("Cache-Control") != "no-cache" || rec2.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("synced headers status=%d headers=%v", rec2.Code, rec2.Header())
	}
}
