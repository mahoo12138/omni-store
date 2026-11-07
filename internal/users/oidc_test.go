package users

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/omni-store/omnistore/internal/db"
	"github.com/omni-store/omnistore/internal/models"
)

func newOIDCService(t *testing.T) *Service {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "omnistore.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return NewService(conn)
}

func TestNormalizeOIDCUsername(t *testing.T) {
	for _, tt := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "alice", want: "alice"},
		{in: "  bob@example.com ", want: "bob_example_com"},
		{in: "carol.99", want: "carol_99"},
		{in: "a b c", want: "a_b_c"},
		{in: "--__--", wantErr: true},
		{in: "", wantErr: true},
		{in: "ab", wantErr: true},
	} {
		got, err := NormalizeOIDCUsername(tt.in)
		if tt.wantErr {
			if !errors.Is(err, ErrInvalidUsername) {
				t.Fatalf("NormalizeOIDCUsername(%q) error=%v want ErrInvalidUsername", tt.in, err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Fatalf("NormalizeOIDCUsername(%q)=%q,%v want %q", tt.in, got, err, tt.want)
		}
	}
	// 超长输入截断到 32 位。
	got, err := NormalizeOIDCUsername(strings.Repeat("a", 40))
	if err != nil || len(got) != 32 {
		t.Fatalf("long username len=%d err=%v want len 32", len(got), err)
	}
}

func TestOIDCIdentityBindUnbindAndJIT(t *testing.T) {
	service := newOIDCService(t)

	// JIT 建号：无密码、角色 user，password_hash 为空使密码登录天然失败。
	created, err := service.CreateOIDCUser("joe", "Joe Doe", "joe@example.com", "https://idp.test", "sub-1")
	if err != nil {
		t.Fatalf("CreateOIDCUser: %v", err)
	}
	if created.Role != models.RoleUser || !created.SSOBound || created.Email != "joe@example.com" {
		t.Fatalf("unexpected created user: %+v", created)
	}
	if hash, err := service.PasswordHashByUsername("joe"); err != nil || hash != "" {
		t.Fatalf("JIT user should have empty password hash: %q %v", hash, err)
	}
	if has, err := service.HasPassword(created.ID); err != nil || has {
		t.Fatalf("HasPassword(JIT user)=%v %v want false", has, err)
	}
	if _, err := service.Create("joe", "", "some-password", models.RoleUser); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("password login account creation should fail on taken username: %v", err)
	}

	// 身份查找与绑定。
	found, err := service.GetByOIDCIdentity("https://idp.test", "sub-1")
	if err != nil || found.ID != created.ID {
		t.Fatalf("GetByOIDCIdentity=%v %v", found, err)
	}
	if _, err := service.GetByOIDCIdentity("https://idp.test", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing identity error=%v want ErrNotFound", err)
	}

	local, err := service.Create("carol", "", "carol-password", models.RoleUser)
	if err != nil {
		t.Fatalf("Create local user: %v", err)
	}
	if err := service.BindOIDC(local.ID, "https://idp.test", "sub-2"); err != nil {
		t.Fatalf("BindOIDC: %v", err)
	}
	// 同一身份不能绑定到第二个账号。
	if err := service.BindOIDC(created.ID, "https://idp.test", "sub-2"); !errors.Is(err, ErrOIDCIdentityTaken) {
		t.Fatalf("duplicate bind error=%v want ErrOIDCIdentityTaken", err)
	}
	// 空_subject 拒绝。
	if err := service.BindOIDC(local.ID, "https://idp.test", ""); err == nil {
		t.Fatalf("empty subject should be rejected")
	}

	// 解绑后可重新绑定，SSOBound 随之变化。
	if err := service.UnbindOIDC(local.ID); err != nil {
		t.Fatalf("UnbindOIDC: %v", err)
	}
	reloaded, err := service.GetByID(local.ID)
	if err != nil || reloaded.SSOBound {
		t.Fatalf("after unbind SSOBound=%v err=%v want false", reloaded.SSOBound, err)
	}
	if err := service.BindOIDC(created.ID, "https://idp.test", "sub-2"); err != nil {
		t.Fatalf("rebind freed identity: %v", err)
	}
	if err := service.UnbindOIDC(99999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unbind missing user error=%v want ErrNotFound", err)
	}
}

func TestCreateOIDCUserDuplicateUsername(t *testing.T) {
	service := newOIDCService(t)
	if _, err := service.Create("dave", "", "dave-password", models.RoleUser); err != nil {
		t.Fatalf("seed local user: %v", err)
	}
	if _, err := service.CreateOIDCUser("dave", "", "", "https://idp.test", "sub-dave"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("JIT on taken username error=%v want ErrUsernameTaken", err)
	}
	// 身份重复建号（并发兜底路径）。
	if _, err := service.CreateOIDCUser("erin", "", "", "https://idp.test", "sub-e"); err != nil {
		t.Fatalf("seed oidc user: %v", err)
	}
	if _, err := service.CreateOIDCUser("erin2", "", "", "https://idp.test", "sub-e"); !errors.Is(err, ErrOIDCIdentityTaken) {
		t.Fatalf("JIT on taken identity error=%v want ErrOIDCIdentityTaken", err)
	}
}
