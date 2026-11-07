package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omni-store/omnistore/migrations"
)

// newV2LegacyDB 构造一个只应用 ≤2.0.0 迁移的数据库，模拟 2.0 存量实例。
func newV2LegacyDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "legacy.db"))+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = conn.Close() })
	ctx := context.Background()
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
  version TEXT PRIMARY KEY,
  applied_at DATETIME NOT NULL
)`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"v1.0.0.sql", "v1.1.0.sql", "v1.1.1.sql", "v2.0.0.sql"} {
		content, err := migrations.FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(ctx, string(content)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			name[:len(name)-4], time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	return conn
}

func TestMigrateV2ToV21OIDCColumns(t *testing.T) {
	conn := newV2LegacyDB(t)
	ctx := context.Background()

	if _, err := conn.ExecContext(ctx,
		`INSERT INTO users (id, user_public_id, username, display_name, password_hash, role, is_disabled, created_at, updated_at)
		  VALUES (1, 'pub-a', 'admin', 'Admin', 'x', 'super_admin', 0, datetime('now'), datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(conn); err != nil {
		t.Fatalf("upgrade to 2.1.0: %v", err)
	}

	// 存量用户的新列回填为空串（未绑定）。
	var email, iss, sub string
	if err := conn.QueryRowContext(ctx,
		`SELECT email, oidc_iss, oidc_sub FROM users WHERE id = 1`).Scan(&email, &iss, &sub); err != nil {
		t.Fatalf("query upgraded user: %v", err)
	}
	if email != "" || iss != "" || sub != "" {
		t.Fatalf("legacy user columns not defaulted: %q %q %q", email, iss, sub)
	}

	// 绑定唯一性：同一 IdP 身份仅允许一个账号；未绑定（空串）不冲突。
	if _, err := conn.ExecContext(ctx,
		`UPDATE users SET oidc_iss = 'https://idp.test', oidc_sub = 'sub-1' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO users (user_public_id, username, display_name, password_hash, role, is_disabled, oidc_iss, oidc_sub, created_at, updated_at)
		  VALUES ('pub-b', 'bob', 'Bob', 'x', 'user', 0, 'https://idp.test', 'sub-1', datetime('now'), datetime('now'))`); err == nil {
		t.Fatal("duplicate oidc identity should violate idx_users_oidc")
	} else if !strings.Contains(err.Error(), "users.oidc_iss") {
		t.Fatalf("unexpected constraint error: %v", err)
	}
	for i := 2; i <= 4; i++ {
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO users (user_public_id, username, display_name, password_hash, role, is_disabled, created_at, updated_at)
			  VALUES (?, ?, 'U', 'x', 'user', 0, datetime('now'), datetime('now'))`,
			"pub-u"+string(rune('0'+i)), "user"+string(rune('0'+i))); err != nil {
			t.Fatalf("multiple unbound users should coexist: %v", err)
		}
	}
}
