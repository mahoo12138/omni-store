// Package db 负责 SQLite 初始化、迁移和连接管理。
// 驱动使用 modernc.org/sqlite（pure Go、无 CGO）。
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	_ "modernc.org/sqlite"
)

// Pool 维护 SQLite WAL 读写分池：写池单连接避免 database is locked，
// 读池多连接（query_only=1）支撑高并发只读查询不被写事务或 Reconcile 阻塞。
type Pool struct {
	write *sql.DB
	read  *sql.DB
}

// Write 返回单连接写池。
func (p *Pool) Write() *sql.DB { return p.write }

// Read 返回多连接只读池。
func (p *Pool) Read() *sql.DB { return p.read }

// Close 关闭读写两个连接池。
func (p *Pool) Close() error {
	var err error
	if p.read != nil {
		err = errors.Join(err, p.read.Close())
	}
	if p.write != nil {
		err = errors.Join(err, p.write.Close())
	}
	return err
}

// OpenPool 打开（必要时创建）SQLite 数据库、执行迁移并返回读写分池。
func OpenPool(dbPath string) (*Pool, error) {
	writeConn, err := Open(dbPath)
	if err != nil {
		return nil, err
	}
	readConn, err := OpenReadOnly(dbPath)
	if err != nil {
		_ = writeConn.Close()
		return nil, err
	}
	return &Pool{write: writeConn, read: readConn}, nil
}

// OpenReadOnly 打开指向已初始化 SQLite 数据库的多连接只读池（WAL 并发读）。
func OpenReadOnly(dbPath string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", filepath.ToSlash(dbPath))
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开只读数据库连接池失败: %w", err)
	}
	readConns := runtime.NumCPU()
	if readConns < 4 {
		readConns = 4
	}
	conn.SetMaxOpenConns(readConns)
	conn.SetMaxIdleConns(readConns)
	if err := conn.Ping(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("连接只读数据库失败: %w", err)
	}
	return conn, nil
}

// Open 打开（必要时创建）SQLite 数据库并应用迁移。
func Open(dbPath string) (*sql.DB, error) {
	if err := prepareDatabasePath(dbPath); err != nil {
		return nil, err
	}

	// busy_timeout 缓解单实例内并发写；WAL + synchronous=NORMAL 兼顾崩溃安全与写入吞吐。
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)", filepath.ToSlash(dbPath))
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}

	// SQLite 单写者，限制连接数避免 database is locked。
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)

	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}
	if err := os.Chmod(dbPath, 0o600); err != nil {
		conn.Close()
		return nil, fmt.Errorf("收紧数据库文件权限失败: %w", err)
	}

	if err := Migrate(conn); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func prepareDatabasePath(dbPath string) error {
	dir := filepath.Dir(dbPath)
	dirExisted := true
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		dirExisted = false
	} else if err != nil {
		return fmt.Errorf("检查数据库目录失败: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建数据库目录失败: %w", err)
	}
	if !dirExisted {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("收紧数据库目录权限失败: %w", err)
		}
	}

	info, err := os.Lstat(dbPath)
	if os.IsNotExist(err) {
		file, createErr := os.OpenFile(dbPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if createErr != nil {
			return fmt.Errorf("安全创建数据库文件失败: %w", createErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			return fmt.Errorf("关闭新数据库文件失败: %w", closeErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("检查数据库文件失败: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("数据库路径必须是普通文件且不能是符号链接")
	}
	if err := os.Chmod(dbPath, 0o600); err != nil {
		return fmt.Errorf("收紧数据库文件权限失败: %w", err)
	}
	return nil
}
