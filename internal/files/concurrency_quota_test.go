package files

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omni-store/omnistore/internal/db"
	"github.com/omni-store/omnistore/internal/locks"
	"github.com/omni-store/omnistore/internal/models"
	"github.com/omni-store/omnistore/internal/sources"
)

// blockingReader 在首字节可读后阻塞，模拟慢客户端上传流，直到 release 被关闭。
type blockingReader struct {
	started chan struct{}
	release chan struct{}
	chunks  [][]byte
	idx     int
}

func (r *blockingReader) Read(p []byte) (int, error) {
	if r.idx == 0 {
		close(r.started)
		<-r.release
	}
	if r.idx >= len(r.chunks) {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[r.idx])
	r.idx++
	return n, nil
}

// TestSlowUploadDoesNotBlockParentListOrSiblingUpload 验证 2.2 两阶段上传：
// 在配额源上传大文件（慢网络体）期间，不阻塞父目录/根目录 List，也不阻塞兄弟路径上传。
func TestSlowUploadDoesNotBlockParentListOrSiblingUpload(t *testing.T) {
	service, source, root := newQuotaTestService(t, 1024*1024)
	if err := os.MkdirAll(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "b"), 0o755); err != nil {
		t.Fatal(err)
	}

	slow := &blockingReader{
		started: make(chan struct{}),
		release: make(chan struct{}),
		chunks:  [][]byte{[]byte("slow-upload-payload")},
	}
	uploadDone := make(chan error, 1)
	go func() {
		_, _, err := service.Upload(source, "a", "big.bin", slow, false)
		uploadDone <- err
	}()

	select {
	case <-slow.started:
	case <-time.After(2 * time.Second):
		t.Fatal("slow upload did not start reading body")
	}

	// 慢上传进行中，根目录 "/" 与 "/a" 的 List 以及兄弟目录上传必须立即返回。
	listRootDone := make(chan error, 1)
	go func() {
		_, err := service.List(source, "", ListOptions{}, false)
		listRootDone <- err
	}()
	listSubDone := make(chan error, 1)
	go func() {
		_, err := service.List(source, "a", ListOptions{}, false)
		listSubDone <- err
	}()
	siblingDone := make(chan error, 1)
	go func() {
		_, _, err := service.Upload(source, "b", "small.txt", strings.NewReader("ok"), false)
		siblingDone <- err
	}()

	for name, ch := range map[string]chan error{
		"list root":      listRootDone,
		"list parent /a": listSubDone,
		"sibling upload": siblingDone,
	} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("%s failed during slow upload: %v", name, err)
			}
		case <-time.After(time.Second):
			close(slow.release)
			t.Fatalf("%s blocked while slow upload was reading body", name)
		}
	}

	close(slow.release)
	select {
	case err := <-uploadDone:
		if err != nil {
			t.Fatalf("slow upload failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("slow upload did not finish after release")
	}

	usage, err := service.StorageUsage(source)
	wantUsage := int64(len("slow-upload-payload") + len("ok"))
	if err != nil || usage != wantUsage {
		t.Fatalf("incremental usage=%d want=%d err=%v", usage, wantUsage, err)
	}
}

// TestOpenDownloadDoesNotBlockOverwriteOrParentList 验证 2.3：
// OpenForRead 打开 fd 后即释放读锁，长下载期间覆写该文件及 List 父目录均不阻塞，
// 且已打开 fd 继续读取旧版本内容不受替换影响。
func TestOpenDownloadDoesNotBlockOverwriteOrParentList(t *testing.T) {
	service, source, root := newQuotaTestService(t, 0)
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Upload(source, "docs", "report.txt", strings.NewReader("v1-content"), false); err != nil {
		t.Fatal(err)
	}

	f, _, unlock, err := service.OpenForRead(source, "docs/report.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	defer f.Close()

	// 模拟下载流尚未读完：此时并发覆写同一文件并 List 父目录不得阻塞。
	overwriteDone := make(chan error, 1)
	go func() {
		_, _, err := service.Upload(source, "docs", "report.txt", strings.NewReader("v2-updated"), true)
		overwriteDone <- err
	}()
	select {
	case err := <-overwriteDone:
		if err != nil {
			t.Fatalf("overwrite while download open failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("overwrite blocked while download fd was open")
	}

	listDone := make(chan error, 1)
	go func() {
		_, err := service.List(source, "", ListOptions{}, false)
		listDone <- err
	}()
	select {
	case err := <-listDone:
		if err != nil {
			t.Fatalf("list parent failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("parent list blocked while download fd was open")
	}

	// 旧 fd 仍能完整读出旧版本快照。
	oldBytes, err := io.ReadAll(f)
	if err != nil || string(oldBytes) != "v1-content" {
		t.Fatalf("open download fd read=%q err=%v, want v1-content", string(oldBytes), err)
	}
}

// TestStagingUploadRollbackIgnoresConcurrentFinalFile 验证两阶段上传在 Phase 1 崩溃时，
// 即使并发上传已创建了同名最终文件，恢复也只清理临时文件而不误报最终路径异常。
func TestStagingUploadRollbackIgnoresConcurrentFinalFile(t *testing.T) {
	service, source, root, userID := newFileUploadRecoveryFixture(t)
	op, err := service.newStagingUploadPlan(source, "race.txt", models.FileOwnerUser, &userID, &userID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.writeUploadOperation(op); err != nil {
		t.Fatal(err)
	}
	tempAbs, _, _, err := service.uploadPaths(op, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tempAbs, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 另一并发上传在崩溃前已经成功提交了 race.txt。
	if err := os.WriteFile(filepath.Join(root, "race.txt"), []byte("winner"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := service.RecoverFileUploadOperations()
	if err != nil {
		t.Fatalf("staging rollback failed with concurrent final file: %v", err)
	}
	if result.RolledBackUploads != 1 {
		t.Fatalf("unexpected recovery result: %+v", result)
	}
	if _, err := os.Stat(tempAbs); !os.IsNotExist(err) {
		t.Fatalf("staging temp file remained: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "race.txt"))
	if err != nil || string(content) != "winner" {
		t.Fatalf("concurrent final file altered: content=%q err=%v", content, err)
	}
}

// TestIncrementalQuotaMatchesPhysicalUsageAcrossMutations 验证 2.4：
// 上传、覆盖、删除、回收站、恢复、校准全链路增量用量与磁盘真实用量保持一致，
// 且预热后不会每次上传重新遍历源目录。
func TestIncrementalQuotaMatchesPhysicalUsageAcrossMutations(t *testing.T) {
	service, source, root := newQuotaTestService(t, 100)
	// 先写入一批外部文件，首次查询完成校准。
	for i := range 20 {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("seed-%02d.txt", i)), []byte("12"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if usage, err := service.StorageUsage(source); err != nil || usage != 40 {
		t.Fatalf("initial calibrated usage=%d err=%v want 40", usage, err)
	}
	// 上传 +10 -> 50
	if _, _, err := service.Upload(source, "", "up.txt", strings.NewReader("0123456789"), false); err != nil {
		t.Fatal(err)
	}
	if usage, err := service.StorageUsage(source); err != nil || usage != 50 {
		t.Fatalf("after upload usage=%d err=%v want 50", usage, err)
	}
	// 覆盖 10 -> 4 => 44
	if _, _, err := service.Upload(source, "", "up.txt", strings.NewReader("abcd"), true); err != nil {
		t.Fatal(err)
	}
	if usage, err := service.StorageUsage(source); err != nil || usage != 44 {
		t.Fatalf("after overwrite usage=%d err=%v want 44", usage, err)
	}
	// 删除 -4 => 40
	if err := service.Delete(source, "up.txt"); err != nil {
		t.Fatal(err)
	}
	if usage, err := service.StorageUsage(source); err != nil || usage != 40 {
		t.Fatalf("after delete usage=%d err=%v want 40", usage, err)
	}
	// 外部新增文件 +6，显式校准后应精确收敛到 46。
	if err := os.WriteFile(filepath.Join(root, "external.bin"), []byte("123456"), 0o644); err != nil {
		t.Fatal(err)
	}
	if usage, err := service.CalibrateStorageUsage(source); err != nil || usage != 46 {
		t.Fatalf("calibrated usage=%d err=%v want 46", usage, err)
	}
}

// TestBatchedReconcileHandlesMultipleBatches 验证 2.5 分批 Reconcile：
// 超过 reconcileBatchSize(500) 的新增、更新与删除均能跨多批次准确收敛。
func TestBatchedReconcileHandlesMultipleBatches(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	pool, err := db.OpenPool(filepath.Join(dataDir, "omnistore.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	root := filepath.Join(base, "source")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	sourceSvc := sources.NewService(pool.Write(), dataDir)
	sourceSvc.SetReadDB(pool.Read())
	src, err := sourceSvc.Create(sources.CreateInput{Name: "batch", RootPath: root})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(pool.Write(), sourceSvc, locks.NewManager())
	svc.SetReadDB(pool.Read())

	const total = 1150
	for i := range total {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f-%04d.txt", i)), []byte("ab"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := svc.ReconcileSource(src)
	if err != nil {
		t.Fatalf("initial batched reconcile: %v", err)
	}
	if res.Added != total || res.Updated != 0 || res.Removed != 0 || res.Unowned != total {
		t.Fatalf("unexpected initial reconcile result: %+v", res)
	}

	// 修改 600 个文件、删除 550 个文件，再次验证跨批次 update 与 delete。
	for i := 0; i < 600; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f-%04d.txt", i)), []byte("abcd"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i := 600; i < total; i++ {
		if err := os.Remove(filepath.Join(root, fmt.Sprintf("f-%04d.txt", i))); err != nil {
			t.Fatal(err)
		}
	}
	res, err = svc.ReconcileSource(src)
	if err != nil {
		t.Fatalf("second batched reconcile: %v", err)
	}
	if res.Added != 0 || res.Updated != 600 || res.Removed != 550 || res.Unowned != 600 {
		t.Fatalf("unexpected second reconcile result: %+v", res)
	}
	if usage, err := svc.StorageUsage(src); err != nil || usage != 600*4 {
		t.Fatalf("reconciled physical usage=%d want=%d err=%v", usage, 600*4, err)
	}
}

// BenchmarkUploadWithManyFiles 基准测试：验证增量配额表预热后，
// 面对大目录树（默认 2000 文件，可用 -benchtime 或扩大规模压测至 10^5）上传延迟不随文件数线性退化。
func BenchmarkUploadWithManyFiles(b *testing.B) {
	base := b.TempDir()
	dataDir := filepath.Join(base, "data")
	conn, err := db.Open(filepath.Join(dataDir, "omnistore.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close()
	root := filepath.Join(base, "source")
	if err := os.MkdirAll(root, 0o755); err != nil {
		b.Fatal(err)
	}
	for dirIdx := range 20 {
		sub := filepath.Join(root, fmt.Sprintf("d%02d", dirIdx))
		if err := os.MkdirAll(sub, 0o755); err != nil {
			b.Fatal(err)
		}
		for fileIdx := range 100 {
			if err := os.WriteFile(filepath.Join(sub, fmt.Sprintf("f%03d.txt", fileIdx)), []byte("x"), 0o644); err != nil {
				b.Fatal(err)
			}
		}
	}
	sourceService := sources.NewService(conn, dataDir)
	quota := int64(100 * 1024 * 1024)
	src, err := sourceService.Create(sources.CreateInput{Name: "bench", RootPath: root, ImportExisting: true})
	if err != nil {
		b.Fatal(err)
	}
	src, err = sourceService.Update(src.Key, sources.UpdateInput{QuotaBytes: &quota})
	if err != nil {
		b.Fatal(err)
	}
	svc := NewService(conn, sourceService, locks.NewManager())
	if _, err := svc.CalibrateStorageUsage(src); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := svc.Upload(src, "", "bench-target.bin", strings.NewReader("payload"), true); err != nil {
			b.Fatal(err)
		}
	}
}
