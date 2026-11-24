package security

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestSourceRootBlocksTOCTOUSymlinkSwap 模拟在 check 与 use 之间并发替换为软链接的 TOCTOU 竞态：
// 无论软链接指向源根外部还是源根内部文件，OpenNoFollow / OpenFileNoFollow / Rename / RemoveAll 均必须拒绝。
func TestSourceRootBlocksTOCTOUSymlinkSwap(t *testing.T) {
	rootDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideSecret := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideSecret, []byte("top-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rootDir, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "dir", "file.txt"), []byte("normal"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("parent dir swapped to outside symlink before OpenNoFollow", func(t *testing.T) {
		sr, err := OpenSourceRoot(rootDir)
		if err != nil {
			t.Fatal(err)
		}
		defer sr.Close()

		// 准备一个合法目录 sub/secret.txt，在 pre-check 通过后、openat 执行前将 sub 替换为指向 outsideDir 的软链接。
		sub := filepath.Join(rootDir, "sub-open")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "secret.txt"), []byte("inside"), 0o600); err != nil {
			t.Fatal(err)
		}
		sr.SetBeforeUseHook(func(rel string) {
			if rel == "sub-open/secret.txt" {
				_ = os.RemoveAll(sub)
				_ = os.Symlink(outsideDir, sub)
			}
		})

		f, _, err := sr.OpenNoFollow("sub-open/secret.txt")
		if err == nil {
			_ = f.Close()
			t.Fatal("TOCTOU parent symlink escape unexpectedly succeeded")
		}
		if !errors.Is(err, ErrSymlink) {
			t.Fatalf("expected ErrSymlink, got %v", err)
		}
	})

	t.Run("final file swapped to intra-root symlink before OpenNoFollow", func(t *testing.T) {
		sr, err := OpenSourceRoot(rootDir)
		if err != nil {
			t.Fatal(err)
		}
		defer sr.Close()

		target := filepath.Join(rootDir, "dir", "victim.txt")
		if err := os.WriteFile(target, []byte("victim"), 0o600); err != nil {
			t.Fatal(err)
		}
		sr.SetBeforeUseHook(func(rel string) {
			if rel == "dir/victim.txt" {
				_ = os.Remove(target)
				_ = os.Symlink("file.txt", target)
			}
		})

		f, _, err := sr.OpenNoFollow("dir/victim.txt")
		if err == nil {
			_ = f.Close()
			t.Fatal("TOCTOU intra-root symlink open unexpectedly succeeded")
		}
		if !errors.Is(err, ErrSymlink) {
			t.Fatalf("expected ErrSymlink, got %v", err)
		}
	})

	t.Run("target parent swapped to outside symlink before OpenFileNoFollow create", func(t *testing.T) {
		sr, err := OpenSourceRoot(rootDir)
		if err != nil {
			t.Fatal(err)
		}
		defer sr.Close()

		sub := filepath.Join(rootDir, "sub-write")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		sr.SetBeforeUseHook(func(rel string) {
			if rel == "sub-write/pwned.txt" {
				_ = os.RemoveAll(sub)
				_ = os.Symlink(outsideDir, sub)
			}
		})

		f, err := sr.OpenFileNoFollow("sub-write/pwned.txt", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_ = f.Close()
			t.Fatal("TOCTOU write escape unexpectedly succeeded")
		}
		if !errors.Is(err, ErrSymlink) {
			t.Fatalf("expected ErrSymlink, got %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(outsideDir, "pwned.txt")); !os.IsNotExist(statErr) {
			t.Fatalf("outside file was created via symlink escape: %v", statErr)
		}
	})

	t.Run("parent swapped to outside symlink before RemoveAll", func(t *testing.T) {
		sr, err := OpenSourceRoot(rootDir)
		if err != nil {
			t.Fatal(err)
		}
		defer sr.Close()

		sub := filepath.Join(rootDir, "sub-del")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "secret.txt"), []byte("inside"), 0o600); err != nil {
			t.Fatal(err)
		}
		sr.SetBeforeUseHook(func(rel string) {
			if rel == "sub-del/secret.txt" {
				_ = os.RemoveAll(sub)
				_ = os.Symlink(outsideDir, sub)
			}
		})

		if err := sr.RemoveAll("sub-del/secret.txt"); !errors.Is(err, ErrSymlink) {
			t.Fatalf("expected ErrSymlink on RemoveAll escape, got %v", err)
		}
		if content, err := os.ReadFile(outsideSecret); err != nil || string(content) != "top-secret" {
			t.Fatalf("outside secret was modified or deleted: content=%q err=%v", content, err)
		}
	})
}
