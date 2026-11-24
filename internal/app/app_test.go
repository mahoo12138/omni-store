package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/omni-store/omnistore/internal/config"
	"github.com/omni-store/omnistore/internal/datadir"
	"github.com/omni-store/omnistore/internal/db"
)

func TestAppLifecycleRecoverAndGracefulWorkerShutdown(t *testing.T) {
	dataDir := t.TempDir()
	if err := datadir.Prepare(dataDir); err != nil {
		t.Fatalf("datadir.Prepare: %v", err)
	}
	cfg := config.Default()
	cfg.Data.Dir = dataDir
	cfg.Server.S3Enabled = true

	pool, err := db.OpenPool(filepath.Join(dataDir, "omnistore.db"))
	if err != nil {
		t.Fatalf("db.OpenPool: %v", err)
	}
	defer pool.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	application, err := New(cfg, pool, logger)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer application.Close()

	if len(application.recoverers) != 8 {
		t.Fatalf("recoverers len=%d want 8", len(application.recoverers))
	}
	if err := application.RecoverAll(); err != nil {
		t.Fatalf("RecoverAll: %v", err)
	}

	bgCtx, bgCancel := context.WithCancel(context.Background())
	waitWorkers := application.StartBackgroundWorkers(bgCtx)

	bgCancel()
	done := make(chan struct{})
	go func() {
		waitWorkers()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("background workers did not stop promptly after context cancel")
	}
	if application.HTTPServer() == nil || application.S3Server() == nil {
		t.Fatal("expected HTTPServer and S3Server to be constructed")
	}
}
