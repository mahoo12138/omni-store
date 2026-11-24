package db

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestPoolAllowsConcurrentReadsDuringWriteTransaction(t *testing.T) {
	pool, err := OpenPool(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatalf("OpenPool: %v", err)
	}
	defer pool.Close()

	// 在写池持有未提交写事务期间，读池必须能够并发读取已提交数据且不被阻塞。
	tx, err := pool.Write().Begin()
	if err != nil {
		t.Fatalf("begin write tx: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO system_settings (key, value, updated_at) VALUES ('pending', '1', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("insert in write tx: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		var wg sync.WaitGroup
		errCh := make(chan error, 8)
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var count int
				if err := pool.Read().QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
					errCh <- err
				}
			}()
		}
		wg.Wait()
		close(errCh)
		for err := range errCh {
			done <- err
			return
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("concurrent read failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read pool blocked while write transaction was open")
	}

	// 只读池必须拒绝写操作（query_only=1）。
	if _, err := pool.Read().Exec(`INSERT INTO system_settings (key, value, updated_at) VALUES ('ro', '1', CURRENT_TIMESTAMP)`); err == nil {
		t.Fatal("read pool unexpectedly permitted a write")
	}
}
