package journal

import (
	"os"
	"path/filepath"
	"testing"
)

type sampleOp struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

func TestJournalWriteReadMarkerAndOrphanCleanup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ops")
	target := filepath.Join(dir, "op-1.json")
	if err := WriteJSONExclusive(dir, target, sampleOp{ID: "op-1", Path: "a/b"}); err != nil {
		t.Fatalf("WriteJSONExclusive: %v", err)
	}
	// 重复写入相同目标必须失败，防止覆盖未恢复日志。
	if err := WriteJSONExclusive(dir, target, sampleOp{ID: "op-1", Path: "a/b"}); err == nil {
		t.Fatal("duplicate WriteJSONExclusive unexpectedly succeeded")
	}
	got, err := ReadStrictJSON[sampleOp](target)
	if err != nil || got.ID != "op-1" || got.Path != "a/b" {
		t.Fatalf("ReadStrictJSON got=%+v err=%v", got, err)
	}
	// 未知字段必须被拒绝。
	unknown := filepath.Join(dir, "unknown.json")
	if err := os.WriteFile(unknown, []byte(`{"id":"x","extra":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadStrictJSON[sampleOp](unknown); err == nil {
		t.Fatal("ReadStrictJSON accepted unknown field")
	}

	marker := filepath.Join(dir, "op-1.database-ready")
	if err := CreateMarker(marker, dir); err != nil {
		t.Fatalf("CreateMarker: %v", err)
	}
	// 重复创建阶段标记必须幂等成功。
	if err := CreateMarker(marker, dir); err != nil {
		t.Fatalf("idempotent CreateMarker: %v", err)
	}
	ok, err := MarkerExists(marker)
	if err != nil || !ok {
		t.Fatalf("MarkerExists=%v err=%v", ok, err)
	}

	// 孤儿标记与残留临时文件应被 CleanOrphans 清理，但保留有效日志的标记。
	orphanTmp := filepath.Join(dir, ".operation-stale.tmp")
	orphanMarker := filepath.Join(dir, "op-gone.database-ready")
	_ = os.WriteFile(orphanTmp, nil, 0o600)
	_ = os.WriteFile(orphanMarker, nil, 0o600)
	if err := CleanOrphans(dir, ".database-ready"); err != nil {
		t.Fatalf("CleanOrphans: %v", err)
	}
	for _, removed := range []string{orphanTmp, orphanMarker} {
		if _, err := os.Stat(removed); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, err=%v", removed, err)
		}
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("valid marker was removed: %v", err)
	}
}
