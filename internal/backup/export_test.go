package backup

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/omni-store/omnistore/internal/config"
	"github.com/omni-store/omnistore/internal/db"
	_ "modernc.org/sqlite"
)

func TestCreatePackageIncludesSnapshotConfigAndKeys(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	conn, err := db.Open(filepath.Join(dataDir, "omnistore.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Exec(`INSERT INTO system_settings (key, value, updated_at) VALUES ('demo', 'enabled', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("seed database: %v", err)
	}
	seedTransientState(t, conn)
	if err := os.MkdirAll(filepath.Join(dataDir, "keys"), 0o700); err != nil {
		t.Fatalf("create key directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "keys", "master.key"), []byte("test-key-material"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	cfg := config.Default()
	cfg.Data.Dir = dataDir
	cfg.Database.Path = filepath.Join(dataDir, "omnistore.db")
	now := time.Date(2026, 8, 4, 12, 30, 0, 0, time.UTC)
	pkg, err := CreatePackage(context.Background(), cfg, conn, "test-version", now)
	if err != nil {
		t.Fatalf("create package: %v", err)
	}
	t.Cleanup(pkg.Cleanup)
	if pkg.Filename != "omnistore-system-config-20260804T123000Z.zip" || pkg.Size == 0 {
		t.Fatalf("unexpected package: %+v", pkg)
	}

	zr, err := zip.OpenReader(pkg.Path)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer zr.Close()
	entries := map[string]*zip.File{}
	for _, file := range zr.File {
		entries[file.Name] = file
	}
	for _, name := range []string{
		"manifest.json", "RESTORE.md", "config/effective-config.yaml",
		"database/omnistore.db", "keys/master.key",
	} {
		if entries[name] == nil {
			t.Fatalf("missing archive entry %s", name)
		}
	}

	manifestFile, err := entries["manifest.json"].Open()
	if err != nil {
		t.Fatalf("open manifest: %v", err)
	}
	manifestBytes, _ := io.ReadAll(manifestFile)
	_ = manifestFile.Close()
	var m manifest
	if err := json.Unmarshal(manifestBytes, &m); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if m.AppVersion != "test-version" || !m.Sensitive || m.FormatVersion != 3 {
		t.Fatalf("unexpected manifest: %+v", m)
	}
	// v3：除 manifest.json 外每个条目都有校验和。
	if len(m.Checksums) != len(m.Contents)-1 {
		t.Fatalf("checksums=%v contents=%v", m.Checksums, m.Contents)
	}
	if m.DatabaseMaxMigration != "v2.1.0" {
		t.Fatalf("max migration=%q", m.DatabaseMaxMigration)
	}
	for _, state := range []string{
		"web_sessions", "share_access_sessions", "webdav_locks",
		"s3_multipart_uploads", "trash_metadata",
	} {
		if !contains(m.ResetState, state) {
			t.Fatalf("manifest reset_state missing %q: %+v", state, m.ResetState)
		}
	}

	snapshotPath := filepath.Join(t.TempDir(), "snapshot.db")
	snapshotFile, err := entries["database/omnistore.db"].Open()
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	out, err := os.Create(snapshotPath)
	if err != nil {
		t.Fatalf("create snapshot target: %v", err)
	}
	if _, err := io.Copy(out, snapshotFile); err != nil {
		t.Fatalf("copy snapshot: %v", err)
	}
	_ = out.Close()
	_ = snapshotFile.Close()
	snapshotDB, err := sql.Open("sqlite", snapshotPath)
	if err != nil {
		t.Fatalf("open snapshot database: %v", err)
	}
	defer snapshotDB.Close()
	var value string
	if err := snapshotDB.QueryRow(`SELECT value FROM system_settings WHERE key = 'demo'`).Scan(&value); err != nil || value != "enabled" {
		t.Fatalf("snapshot missing seeded data: value=%q err=%v", value, err)
	}
	assertRowCount(t, snapshotDB, "file_shares", 1)
	for _, table := range []string{
		"sessions",
		"share_access_sessions",
		"webdav_locks",
		"s3_multipart_parts",
		"s3_multipart_uploads",
		"trash_entries",
		"file_records",
	} {
		assertRowCount(t, snapshotDB, table, 0)
	}
	rows, err := snapshotDB.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("check snapshot foreign keys: %v", err)
	}
	if rows.Next() {
		_ = rows.Close()
		t.Fatal("snapshot contains a foreign key violation")
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close foreign key check: %v", err)
	}

	// Sanitizing the exported copy must never mutate the running database.
	for _, table := range []string{
		"sessions",
		"share_access_sessions",
		"webdav_locks",
		"s3_multipart_parts",
		"s3_multipart_uploads",
		"trash_entries",
		"file_records",
	} {
		assertRowCount(t, conn, table, 1)
	}
}

func seedTransientState(t *testing.T, conn *sql.DB) {
	t.Helper()
	statements := []string{
		`INSERT INTO users (
			id, user_public_id, username, display_name, password_hash, role,
			is_disabled, quota_bytes, created_at, updated_at
		) VALUES (1, 'usr_backup', 'backup-user', 'Backup User', 'hash', 'super_admin', 0, 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT INTO storage_sources (
			id, key, name, root_path, is_disabled,
			webdav_enabled, s3_enabled, quota_bytes, created_at, updated_at
		) VALUES (1, 'src_backup', 'Backup Source', '/tmp/backup-source', 0, 1, 0, 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT INTO sessions (
			session_id, user_id, csrf_token_hash, expires_at, created_at, last_seen_at
		) VALUES ('session-backup', 1, 'csrf-hash', '2099-01-01T00:00:00Z', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT INTO file_shares (
			id, share_key, storage_source_id, relative_path, entry_type,
			created_by_user_id, password_hash, created_at
		) VALUES (1, 'share-backup', 1, 'shared.txt', 'file', 1, 'share-password-hash', CURRENT_TIMESTAMP)`,
		`INSERT INTO share_access_sessions (
			share_id, token_hash, expires_at, created_at
		) VALUES (1, 'share-session-hash', '2099-01-01T00:00:00Z', CURRENT_TIMESTAMP)`,
		`INSERT INTO s3_multipart_uploads (
			upload_id, owner_user_id, storage_source_id, object_key, created_at, updated_at
		) VALUES ('upload-backup', 1, 1, 'partial.bin', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT INTO s3_multipart_parts (
			upload_id, part_number, etag, size, created_at
		) VALUES ('upload-backup', 1, 'etag', 10, CURRENT_TIMESTAMP)`,
		`INSERT INTO webdav_locks (
			token, storage_source_id, relative_path, depth, owner_user_id,
			created_at, refreshed_at, expires_at
		) VALUES ('lock-backup', 1, 'locked.txt', '0', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '2099-01-01T00:00:00Z')`,
		`INSERT INTO trash_entries (
			trash_key, storage_source_id, original_relative_path, entry_type,
			file_count, size, deleted_by_user_id, deleted_at
		) VALUES ('trash-backup', 1, 'deleted.txt', 'file', 1, 10, 1, CURRENT_TIMESTAMP)`,
		`INSERT INTO file_records (
			storage_source_id, relative_path, size, owner_user_id, owner_type,
			created_by_user_id, updated_by_user_id, mtime_unix_nano,
			record_status, trash_key, created_at, updated_at
		) VALUES (1, 'deleted.txt', 10, 1, 'user', 1, 1, 1, 'trash', 'trash-backup', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
	}
	for _, statement := range statements {
		if _, err := conn.Exec(statement); err != nil {
			t.Fatalf("seed transient state: %v\n%s", err, statement)
		}
	}
}

func assertRowCount(t *testing.T, conn *sql.DB, table string, want int) {
	t.Helper()
	var got int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("unexpected %s row count: got %d, want %d", table, got, want)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestCreatePackageSkipsSymlinkedKeys(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	conn, err := db.Open(filepath.Join(dataDir, "omnistore.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := os.MkdirAll(filepath.Join(dataDir, "keys"), 0o700); err != nil {
		t.Fatalf("create key directory: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.key")
	if err := os.WriteFile(outside, []byte("must-not-export"), 0o600); err != nil {
		t.Fatalf("write outside key: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(dataDir, "keys", "linked.key")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	cfg := config.Default()
	cfg.Data.Dir = dataDir
	cfg.Database.Path = filepath.Join(dataDir, "omnistore.db")
	pkg, err := CreatePackage(context.Background(), cfg, conn, "test", time.Now())
	if err != nil {
		t.Fatalf("create package: %v", err)
	}
	t.Cleanup(pkg.Cleanup)
	zr, err := zip.OpenReader(pkg.Path)
	if err != nil {
		t.Fatalf("open package: %v", err)
	}
	defer zr.Close()
	for _, file := range zr.File {
		if file.Name == "keys/linked.key" {
			t.Fatal("symlinked key was exported")
		}
	}
}
