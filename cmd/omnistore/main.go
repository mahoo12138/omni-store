// OmniStore 入口。
//
// 用法:
//
//	omnistore server [--config path]                                启动 HTTP 服务
//	omnistore restore <backup.zip> [--config path] [--force]         离线恢复系统备份
//	omnistore admin reset-password --username <name> [--config path]  紧急重置密码
//	omnistore version                                                查看构建版本
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/omni-store/omnistore/internal/app"
	"github.com/omni-store/omnistore/internal/audit"
	"github.com/omni-store/omnistore/internal/auth"
	"github.com/omni-store/omnistore/internal/backup"
	"github.com/omni-store/omnistore/internal/buildinfo"
	"github.com/omni-store/omnistore/internal/config"
	"github.com/omni-store/omnistore/internal/datadir"
	"github.com/omni-store/omnistore/internal/db"
	"github.com/omni-store/omnistore/internal/users"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "server":
		err = runServer(os.Args[2:])
	case "restore":
		err = runRestore(os.Args[2:])
	case "admin":
		err = runAdmin(os.Args[2:])
	case "version", "-v", "--version":
		printVersion()
		return
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`OmniStore - 轻量级自部署存储中心

用法:
  omnistore server [--config path]
      启动 HTTP 服务

  omnistore admin reset-password --username <name> [--password <new>] [--config path]
      紧急重置用户密码（只能在能访问数据库的机器上执行）
      不指定 --password 时自动生成随机密码并打印一次

  omnistore version
      显示版本、Git commit、构建时间和 Go 版本`)
}

func printVersion() {
	info := buildinfo.Current()
	fmt.Printf("OmniStore %s\n", info.Version)
	fmt.Printf("commit: %s\n", info.Commit)
	fmt.Printf("built: %s\n", info.BuildTime)
	fmt.Printf("go: %s (%s/%s)\n", info.GoVersion, runtime.GOOS, runtime.GOARCH)
}

func runServer(args []string) error {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	configFile := fs.String("config", "", "配置文件路径（默认 ./config.yaml，可用 OMNISTORE_CONFIG_FILE 指定）")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configFile)
	if err != nil {
		return err
	}

	logger := newLogger(cfg.Log.Level)

	if err := datadir.Prepare(cfg.Data.Dir); err != nil {
		return err
	}

	pool, err := db.OpenPool(cfg.DatabasePath())
	if err != nil {
		return err
	}
	defer pool.Close()
	logger.Info("数据库就绪", "path", cfg.DatabasePath())

	application, err := app.New(cfg, pool, logger)
	if err != nil {
		return err
	}
	defer application.Close()

	if err := application.RecoverAll(); err != nil {
		return err
	}

	bgCtx, bgCancel := context.WithCancel(context.Background())
	waitWorkers := application.StartBackgroundWorkers(bgCtx)
	defer func() {
		bgCancel()
		waitWorkers()
	}()

	srv := application.HTTPServer()
	servers := []*http.Server{srv}
	errCh := make(chan error, 2)
	go func() {
		logger.Info("HTTP 服务启动", "addr", cfg.Server.HTTPAddr, "public_url", cfg.Server.PublicURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	if cfg.Server.S3Enabled {
		s3Srv := application.S3Server()
		servers = append(servers, s3Srv)
		go func() {
			logger.Info("S3 服务启动", "addr", cfg.Server.S3Addr, "style", "path")
			if err := s3Srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	var runErr error
	select {
	case err := <-errCh:
		runErr = err
	case sig := <-stop:
		logger.Info("收到退出信号，正在关闭", "signal", sig.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, running := range servers {
		if err := running.Shutdown(ctx); err != nil && runErr == nil {
			runErr = err
		}
	}
	return runErr
}

// runAdmin 处理 admin 子命令。MVP 只有 reset-password（README §8.8）。
// runRestore 执行离线系统备份恢复；必须在服务停止后运行。
func runRestore(args []string) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "--") {
		return fmt.Errorf("用法: omnistore restore <backup.zip> [--config path] [--force]")
	}
	backupPath := args[0]
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	configFile := fs.String("config", "", "配置文件路径")
	force := fs.Bool("force", false, "跳过数据目录非空确认（用于自动化）")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	cfg, err := config.Load(*configFile)
	if err != nil {
		return err
	}
	if err := datadir.Prepare(cfg.Data.Dir); err != nil {
		return err
	}
	// 目标数据库已存在时要求 --force，防止误把恢复跑在活跃实例上。
	// （dataDir.Prepare 建出的 keys/tmp 等目录不算活跃实例。）
	if _, err := os.Stat(cfg.DatabasePath()); err == nil && !*force {
		return fmt.Errorf("目标数据库 %s 已存在；确认要在其上恢复请加 --force（当前数据库会先备份到 pre-restore-* 目录）", cfg.DatabasePath())
	}

	report, err := backup.Restore(context.Background(), backup.RestoreOptions{
		BackupPath: backupPath,
		Cfg:        cfg,
		Force:      *force,
	})
	if err != nil {
		return err
	}
	fmt.Println("恢复完成。报告：")
	fmt.Printf("  备份格式: v%d（实例版本 %s）\n", report.FormatVersion, report.AppVersionInBackup)
	fmt.Printf("  数据库: %v；快照最高迁移: %s\n", report.DatabaseRestored, report.MaxMigrationRestored)
	fmt.Printf("  密钥: %d 个\n", len(report.KeysRestored))
	fmt.Printf("  预恢复备份: %s\n", report.PreRestoreBackupDir)
	for _, source := range report.Sources {
		state := "根目录存在"
		if !source.RootExists {
			state = "根目录缺失，需要重新绑定"
		}
		fmt.Printf("  存储源 %s（%s）→ %s: %s\n", source.Name, source.Key, source.RootPath, state)
	}
	for _, warning := range report.Warnings {
		fmt.Printf("  ⚠ %s\n", warning)
	}
	return nil
}

func runAdmin(args []string) error {
	if len(args) < 1 || args[0] != "reset-password" {
		return fmt.Errorf("用法: omnistore admin reset-password --username <name> [--password <new>]")
	}

	fs := flag.NewFlagSet("reset-password", flag.ExitOnError)
	username := fs.String("username", "", "要重置密码的用户名")
	password := fs.String("password", "", "新密码（不指定时自动生成）")
	configFile := fs.String("config", "", "配置文件路径")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *username == "" {
		return fmt.Errorf("必须指定 --username")
	}

	cfg, err := config.Load(*configFile)
	if err != nil {
		return err
	}
	if err := datadir.Prepare(cfg.Data.Dir); err != nil {
		return err
	}
	dbConn, err := db.Open(cfg.DatabasePath())
	if err != nil {
		return err
	}
	defer dbConn.Close()

	userSvc := users.NewService(dbConn)
	u, err := userSvc.GetByUsername(*username)
	if err != nil {
		return fmt.Errorf("用户 %s 不存在", *username)
	}

	newPassword := *password
	generated := false
	if newPassword == "" {
		newPassword = auth.NewRandomToken("", 12)
		generated = true
	}
	if err := userSvc.UpdatePassword(u.ID, newPassword); err != nil {
		return err
	}

	// 审计：actor_type = system, entry_type = cli，不记录明文密码。
	auditLogger := audit.New(dbConn, cfg.Audit.Enabled, cfg.Audit.MaxEntries, newLogger(cfg.Log.Level))
	auditLogger.Log(audit.Entry{
		ActorType: audit.ActorSystem,
		EntryType: audit.EntryCLI,
		Action:    "reset_password",
		Status:    audit.StatusSuccess,
	})

	fmt.Printf("用户 %s 的密码已重置。\n", *username)
	if generated {
		fmt.Printf("新密码（只显示一次，请立即登录后修改）: %s\n", newPassword)
	}
	return nil
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lv}))
}
