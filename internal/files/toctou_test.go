package files

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFilesServiceBlocksSymlinkTOCTOURace 端到端验证 2.6：
// 在 files.Service 的 OpenForRead、Upload、Delete 执行过程中，通过注入 hook
// 在路径预检（Lstat）与真正文件系统 syscall 之间把父目录替换为指向源外的软链接，
// 断言全部被 *os.Root 阻断（返回 ErrUnsupported）且绝不读写或删除源外文件。
func TestFilesServiceBlocksSymlinkTOCTOURace(t *testing.T) {
	service, source, root := newQuotaTestService(t, 0)
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("outside-secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	srcRoot, err := service.sources.OpenRoot(source)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("OpenForRead blocks TOCTOU parent swap", func(t *testing.T) {
		sub := filepath.Join(root, "swap-read")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "secret.txt"), []byte("inside"), 0o600); err != nil {
			t.Fatal(err)
		}
		srcRoot.SetBeforeUseHook(func(rel string) {
			if rel == "swap-read/secret.txt" {
				_ = os.RemoveAll(sub)
				_ = os.Symlink(outside, sub)
			}
		})
		defer srcRoot.SetBeforeUseHook(nil)

		f, _, unlock, err := service.OpenForRead(source, "swap-read/secret.txt")
		if err == nil {
			unlock()
			_ = f.Close()
			t.Fatal("OpenForRead unexpectedly bypassed symlink TOCTOU")
		}
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("expected ErrUnsupported, got %v", err)
		}
	})

	t.Run("Upload blocks TOCTOU parent swap before temp creation", func(t *testing.T) {
		sub := filepath.Join(root, "swap-upload")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		srcRoot.SetBeforeUseHook(func(rel string) {
			if strings.HasPrefix(rel, "swap-upload/.omnistore-upload-") {
				_ = os.RemoveAll(sub)
				_ = os.Symlink(outside, sub)
			}
		})
		defer srcRoot.SetBeforeUseHook(nil)

		if _, _, err := service.Upload(source, "swap-upload", "escaped.txt", strings.NewReader("pwn"), false); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("expected ErrUnsupported on Upload TOCTOU, got %v", err)
		}
		if _, err := os.Stat(filepath.Join(outside, "escaped.txt")); !os.IsNotExist(err) {
			t.Fatalf("outside file was written via Upload TOCTOU: %v", err)
		}
	})

	t.Run("Delete blocks TOCTOU parent swap before RemoveAll", func(t *testing.T) {
		sub := filepath.Join(root, "swap-del")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "secret.txt"), []byte("inside"), 0o600); err != nil {
			t.Fatal(err)
		}
		// 仅在真正 RemoveAll 时替换（跳过前面的 Lstat）。
		calls := 0
		srcRoot.SetBeforeUseHook(func(rel string) {
			if rel == "swap-del/secret.txt" {
				calls++
				if calls == 2 {
					_ = os.RemoveAll(sub)
					_ = os.Symlink(outside, sub)
				}
			}
		})
		defer srcRoot.SetBeforeUseHook(nil)

		if err := service.Delete(source, "swap-del/secret.txt"); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("expected ErrUnsupported on Delete TOCTOU, got %v", err)
		}
		content, err := os.ReadFile(outsideFile)
		if err != nil || string(content) != "outside-secret" {
			t.Fatalf("outside secret was touched by Delete TOCTOU: content=%q err=%v", content, err)
		}
	})
}
