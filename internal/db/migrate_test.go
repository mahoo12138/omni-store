package db

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseAndSortMigrationFilenamesBySemanticVersion(t *testing.T) {
	names := []string{"v2.0.0.sql", "v1.10.0.sql", "v1.2.1.sql", "v1.2.0.sql"}
	files := make([]migrationFile, 0, len(names))
	for _, name := range names {
		version, err := parseMigrationFilename(name)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, migrationFile{name: name, version: version})
	}
	sortMigrationFiles(files)
	got := make([]string, 0, len(files))
	for _, file := range files {
		got = append(got, file.name)
	}
	want := []string{"v1.2.0.sql", "v1.2.1.sql", "v1.10.0.sql", "v2.0.0.sql"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected migration order: got=%v want=%v", got, want)
	}
	for _, invalid := range []string{"0001_init.sql", "v1.0.sql", "v1.01.0.sql", "v1.0.0-dev.sql"} {
		if _, err := parseMigrationFilename(invalid); err == nil {
			t.Fatalf("expected invalid migration filename %s", invalid)
		}
	}
}

func TestOpenAppliesInitialAndFileExperienceMigrations(t *testing.T) {
	conn, err := Open(filepath.Join(t.TempDir(), "omnistore.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer conn.Close()

	var appliedAt string
	if err := conn.QueryRow(`SELECT applied_at FROM schema_migrations WHERE version = 'v1.1.0'`).Scan(&appliedAt); err != nil {
		t.Fatalf("query v1.1.0 migration: %v", err)
	}
	if appliedAt == "" {
		t.Fatal("v1.1.0 migration has no applied_at")
	}
	if err := conn.QueryRow(`SELECT applied_at FROM schema_migrations WHERE version = 'v1.1.1'`).Scan(&appliedAt); err != nil || appliedAt == "" {
		t.Fatalf("query v1.1.1 migration applied_at=%q err=%v", appliedAt, err)
	}
	var initialMigrationCount int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 'v1.0.0'`).Scan(&initialMigrationCount); err != nil || initialMigrationCount != 1 {
		t.Fatalf("initial migration count=%d err=%v", initialMigrationCount, err)
	}
	for _, table := range []string{"users", "storage_sources", "file_records", "images", "audit_logs", "webdav_locks", "recent_files", "favorites"} {
		var count int
		if err := conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("table %s count=%d err=%v", table, count, err)
		}
	}
}

func TestFreshDatabaseHasThemePreferenceColumn(t *testing.T) {
	conn, err := Open(filepath.Join(t.TempDir(), "omnistore.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer conn.Close()

	rows, err := conn.Query(`PRAGMA table_info(user_preferences)`)
	if err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid, notNull, pk int
			name, ctype      string
			dflt             any
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan column row: %v", err)
		}
		if name != "theme" {
			continue
		}
		if notNull != 1 {
			t.Fatal("user_preferences.theme must be NOT NULL")
		}
		if got, _ := dflt.(string); got != "'system'" {
			t.Fatalf("user_preferences.theme default=%v, want 'system'", dflt)
		}
		return
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns: %v", err)
	}
	t.Fatal("fresh database is missing the user_preferences.theme column")
}

func TestMigrateNeverReplaysAppliedVersion(t *testing.T) {
	conn, err := Open(filepath.Join(t.TempDir(), "omnistore.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer conn.Close()

	var appliedAtBefore string
	if err := conn.QueryRow(`SELECT applied_at FROM schema_migrations WHERE version = 'v1.0.0'`).Scan(&appliedAtBefore); err != nil {
		t.Fatalf("query applied_at: %v", err)
	}
	if _, err := conn.Exec(`DROP TABLE webdav_locks`); err != nil {
		t.Fatalf("drop table to detect replay: %v", err)
	}
	if err := Migrate(conn); err != nil {
		t.Fatalf("repeat Migrate(): %v", err)
	}

	var tableCount int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'webdav_locks'`).Scan(&tableCount); err != nil {
		t.Fatalf("query dropped table: %v", err)
	}
	if tableCount != 0 {
		t.Fatal("applied v1.0.0 migration was replayed")
	}
	var appliedAtAfter string
	if err := conn.QueryRow(`SELECT applied_at FROM schema_migrations WHERE version = 'v1.0.0'`).Scan(&appliedAtAfter); err != nil {
		t.Fatalf("query repeated applied_at: %v", err)
	}
	if appliedAtAfter != appliedAtBefore {
		t.Fatalf("applied_at changed: before=%q after=%q", appliedAtBefore, appliedAtAfter)
	}
}

func TestMigrateRejectsNewerOrUnknownVersion(t *testing.T) {
	conn, err := Open(filepath.Join(t.TempDir(), "omnistore.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES ('v99.0.0', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("insert newer migration: %v", err)
	}
	if err := Migrate(conn); err == nil {
		t.Fatal("Migrate() accepted newer schema version v99.0.0")
	}
}

func TestIsUniqueConstraintDetectsSQLiteCodes(t *testing.T) {
	conn, err := Open(filepath.Join(t.TempDir(), "omnistore.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Exec(`CREATE TABLE uniq_test (id TEXT PRIMARY KEY, code TEXT NOT NULL UNIQUE)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO uniq_test (id, code) VALUES ('a', 'c1')`); err != nil {
		t.Fatal(err)
	}
	_, pkErr := conn.Exec(`INSERT INTO uniq_test (id, code) VALUES ('a', 'c2')`)
	if !IsUniqueConstraint(pkErr, "uniq_test.id") || IsUniqueConstraint(pkErr, "uniq_test.code") {
		t.Fatalf("primary key conflict check failed: err=%v", pkErr)
	}
	_, uniqErr := conn.Exec(`INSERT INTO uniq_test (id, code) VALUES ('b', 'c1')`)
	if !IsUniqueConstraint(uniqErr, "uniq_test.code") || IsUniqueConstraint(uniqErr, "uniq_test.id") {
		t.Fatalf("unique column conflict check failed: err=%v", uniqErr)
	}
}
