package db

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/omni-store/omnistore/migrations"
)

var migrationFilenamePattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.sql$`)

type semanticVersion struct {
	major int
	minor int
	patch int
}

type migrationFile struct {
	name    string
	version semanticVersion
}

func compareSemanticVersion(left, right semanticVersion) int {
	if left.major != right.major {
		if left.major < right.major {
			return -1
		}
		return 1
	}
	if left.minor != right.minor {
		if left.minor < right.minor {
			return -1
		}
		return 1
	}
	if left.patch != right.patch {
		if left.patch < right.patch {
			return -1
		}
		return 1
	}
	return 0
}

// Migrate applies each SemVer migration exactly once and records the version in
// schema_migrations. Applied files are immutable: schema changes must be added
// as a new vMAJOR.MINOR.PATCH.sql file instead of editing or replaying history.
//
// 迁移期间固定单条 SQLite 连接并关闭外键强制（PRAGMA foreign_keys 是事务内
// no-op，必须在事务外切换），避免重建带子表引用的表时触发级联删除；
// 全部迁移结束后用 foreign_key_check 校验完整性并恢复强制。
// 如果数据库中存在高于或未知于当前二进制内置版本的迁移，拒绝启动以防止降级破坏。
func Migrate(conn *sql.DB) error {
	ctx := context.Background()
	pinned, err := conn.Conn(ctx)
	if err != nil {
		return fmt.Errorf("获取迁移数据库连接失败: %w", err)
	}
	defer pinned.Close()

	if _, err := pinned.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
  version TEXT PRIMARY KEY,
  applied_at DATETIME NOT NULL
)`); err != nil {
		return fmt.Errorf("创建 schema_migrations 失败: %w", err)
	}

	applied := map[string]bool{}
	rows, err := pinned.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("读取迁移版本失败: %w", err)
	}
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			return err
		}
		applied[version] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}

	files, err := migrationFiles()
	if err != nil {
		return err
	}
	known := make(map[string]bool, len(files))
	var maxEmbedded semanticVersion
	for _, file := range files {
		known[strings.TrimSuffix(file.name, ".sql")] = true
		maxEmbedded = file.version
	}
	// 降级保护：数据库包含当前二进制未知或更新版本的迁移时拒绝启动。
	unknownVersions := make([]string, 0)
	for version := range applied {
		if known[version] {
			continue
		}
		if parsed, parseErr := parseMigrationFilename(version + ".sql"); parseErr == nil &&
			len(files) > 0 && compareSemanticVersion(parsed, maxEmbedded) > 0 {
			return fmt.Errorf("数据库迁移版本 %s 高于当前二进制支持的最高版本 %s，禁止降级启动",
				version, strings.TrimSuffix(files[len(files)-1].name, ".sql"))
		}
		unknownVersions = append(unknownVersions, version)
	}
	if len(unknownVersions) > 0 {
		sort.Strings(unknownVersions)
		return fmt.Errorf("数据库包含当前二进制未识别的迁移版本 %s，拒绝启动", strings.Join(unknownVersions, ", "))
	}

	pending := make([]migrationFile, 0, len(files))
	for _, file := range files {
		if !applied[strings.TrimSuffix(file.name, ".sql")] {
			pending = append(pending, file)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	if _, err := pinned.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("迁移前关闭外键强制失败: %w", err)
	}
	defer func() {
		_, _ = pinned.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
	}()

	for _, file := range pending {
		sqlBytes, err := migrations.FS.ReadFile(file.name)
		if err != nil {
			return fmt.Errorf("读取迁移 %s 失败: %w", file.name, err)
		}
		tx, err := pinned.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(sqlBytes)); err != nil {
			tx.Rollback()
			return fmt.Errorf("执行迁移 %s 失败: %w", file.name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			strings.TrimSuffix(file.name, ".sql"), time.Now().UTC()); err != nil {
			tx.Rollback()
			return fmt.Errorf("记录迁移版本 %s 失败: %w", file.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("提交迁移 %s 失败: %w", file.name, err)
		}
	}
	if _, err := pinned.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		return fmt.Errorf("恢复外键强制失败: %w", err)
	}
	if err := verifyForeignKeyIntegrity(ctx, pinned); err != nil {
		return err
	}
	return nil
}

// verifyForeignKeyIntegrity 在迁移后确认没有悬空引用，防止迁移副本漏行。
func verifyForeignKeyIntegrity(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("外键完整性检查失败: %w", err)
	}
	defer rows.Close()
	var table string
	if rows.Next() {
		if err := rows.Scan(&table, new(any), new(any), new(any)); err != nil {
			rows.Close()
			return err
		}
		return fmt.Errorf("迁移后外键完整性校验失败，表 %s 存在悬空引用", table)
	}
	return rows.Err()
}

func migrationFiles() ([]migrationFile, error) {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("读取迁移文件失败: %w", err)
	}
	files := make([]migrationFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := parseMigrationFilename(entry.Name())
		if err != nil {
			return nil, err
		}
		files = append(files, migrationFile{name: entry.Name(), version: version})
	}
	sortMigrationFiles(files)
	return files, nil
}

func sortMigrationFiles(files []migrationFile) {
	sort.Slice(files, func(i, j int) bool {
		left, right := files[i].version, files[j].version
		if left.major != right.major {
			return left.major < right.major
		}
		if left.minor != right.minor {
			return left.minor < right.minor
		}
		return left.patch < right.patch
	})
}

func parseMigrationFilename(name string) (semanticVersion, error) {
	matches := migrationFilenamePattern.FindStringSubmatch(name)
	if matches == nil {
		return semanticVersion{}, fmt.Errorf("迁移文件名 %s 不符合 vMAJOR.MINOR.PATCH.sql 规则", name)
	}
	parts := [3]int{}
	for i := range parts {
		value, err := strconv.Atoi(matches[i+1])
		if err != nil {
			return semanticVersion{}, fmt.Errorf("解析迁移文件版本 %s 失败: %w", name, err)
		}
		parts[i] = value
	}
	return semanticVersion{major: parts[0], minor: parts[1], patch: parts[2]}, nil
}
