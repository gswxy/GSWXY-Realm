package backup

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gswxy/gswxy-realm/manager/internal/logging"
	"github.com/gswxy/gswxy-realm/manager/internal/platform"
)

// fakeJob wires a Manager with in-memory dump/import implementations so the
// full Create/Restore control flow (locks, phases, hooks, ordering) is
// tested without mysqldump.
type fakeJob struct {
	mu sync.Mutex
	// dumpFail, when non-empty, makes the dumper fail for that database.
	dumpFail string
	// importFail, when non-empty, makes the importer fail for that database.
	importFail string
	// slow blocks dump runs until released (concurrency tests).
	slow   chan struct{}
	dumped []string
	// imported records databases actually imported.
	imported []string
	stopCalls   int
	startCalls  int
	stopErr     error
	startErr    error
	lastStart   [2]bool
}

func newFakeManager(t *testing.T) (*Manager, *fakeJob) {
	t.Helper()
	// 不用 t.TempDir：Logger 会持有日志句柄，Windows 下强删会失败。
	root, err := os.MkdirTemp("", "gsrm-bkjob-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	paths := platform.Paths{
		AppName: "test",
		AppDest: filepath.Join(root, "target"),
		Var:     filepath.Join(root, "var"),
		Etc:     filepath.Join(root, "etc"),
		Tmp:     filepath.Join(root, "tmp"),
	}
	for _, d := range []string{paths.Backups(), paths.Tmp, paths.UserConfig(), paths.State(), paths.MySQLData()} {
		_ = os.MkdirAll(d, 0o755)
	}
	_ = os.WriteFile(paths.StateFile(), []byte(`{"database":{"port":33000}}`), 0o600)

	log, err := logging.New(filepath.Join(root, "mgr.log"))
	if err != nil {
		t.Fatal(err)
	}
	fj := &fakeJob{}
	m := &Manager{
		Paths: paths, Log: log,
		Version: VersionInfo{GSWXY: "1.1.0", CoreCommit: "c", PlayerbotsCommit: "p"},
	}
	m.execDump = func(bin string, args []string, stdout io.Writer) error {
		fj.mu.Lock()
		if fj.slow != nil {
			fj.mu.Unlock()
			<-fj.slow
			fj.mu.Lock()
		}
		db := args[len(args)-1]
		for _, a := range args { // world dumps carry table names after db
			if a == db {
				break
			}
		}
		if fj.dumpFail == db {
			fj.mu.Unlock()
			return fmt.Errorf("dump %s failed (fake)", db)
		}
		fj.dumped = append(fj.dumped, db)
		fj.mu.Unlock()
		fmt.Fprintf(stdout, "-- fake dump for %s\n", db)
		return nil
	}
	m.execImport = func(bin string, args []string, stdin io.Reader) ([]byte, error) {
		_, _ = io.ReadAll(stdin)
		db := args[len(args)-1]
		fj.mu.Lock()
		defer fj.mu.Unlock()
		if fj.importFail == db {
			return []byte("ERROR 1064 fake syntax error"), fmt.Errorf("exit status 1")
		}
		fj.imported = append(fj.imported, db)
		return nil, nil
	}
	m.StopGameServers = func() (bool, bool, error) {
		fj.mu.Lock()
		defer fj.mu.Unlock()
		fj.stopCalls++
		return true, true, fj.stopErr
	}
	m.StartGameServers = func(world, auth bool) error {
		fj.mu.Lock()
		defer fj.mu.Unlock()
		fj.startCalls++
		fj.lastStart = [2]bool{world, auth}
		return fj.startErr
	}
	return m, fj
}

func (f *fakeJob) snapshot() (dumped, imported []string, stopCalls, startCalls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.dumped...), append([]string{}, f.imported...), f.stopCalls, f.startCalls
}

func waitForPhase(t *testing.T, m *Manager, substr string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(m.Status().Phase, substr) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// 正常创建备份：四库全部导出、归档可解析。
func TestJobCreateNormal(t *testing.T) {
	m, fj := newFakeManager(t)
	path, err := m.Create("mysqldump", "rootpw")
	if err != nil {
		t.Fatal(err)
	}
	dumped, _, _, _ := fj.snapshot()
	if len(dumped) != 4 {
		t.Fatalf("dumped = %v, want 4 databases", dumped)
	}
	if st := m.Status(); st.Phase != "完成" || st.Error != "" {
		t.Fatalf("final status = %+v", st)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("archive missing: %v", err)
	}
}

// 正常恢复：先快照（同锁！）→ 停服 → 按序导入 → 拉回服务。
// 这是此前 Restore→Create 死锁缺陷的直接回归测试。
func TestJobRestoreNormal(t *testing.T) {
	m, fj := newFakeManager(t)
	path, err := m.Create("mysqldump", "rootpw")
	if err != nil {
		t.Fatal(err)
	}
	// 用户配置在备份之后新增（恢复应保留备份内内容 + 覆盖回来）
	if err := m.Restore(filepath.Base(path), "mysql", "mysqldump", "rootpw"); err != nil {
		t.Fatalf("restore failed (deadlock regression?): %v", err)
	}
	dumped, imported, stopCalls, startCalls := fj.snapshot()
	// 4 (archive) + 4 (pre-restore snapshot) dumps
	if len(dumped) != 8 {
		t.Fatalf("dumped = %v, want 8 (archive + snapshot)", dumped)
	}
	wantImport := []string{"acore_auth", "acore_characters", "acore_world", "acore_playerbots"}
	if len(imported) != 4 || imported[0] != wantImport[0] || imported[3] != wantImport[3] {
		t.Fatalf("imported = %v, want %v", imported, wantImport)
	}
	if stopCalls != 1 || startCalls != 1 {
		t.Fatalf("stop=%d start=%d, want 1/1", stopCalls, startCalls)
	}
	if fj.lastStart != [2]bool{true, true} {
		t.Fatalf("restart flags = %v, want both", fj.lastStart)
	}
	st := m.Status()
	if st.Error != "" || st.Phase != "完成" {
		t.Fatalf("final status = %+v", st)
	}
}

// 恢复前快照失败：必须拒绝恢复，且不停止服务、不导入任何数据库。
func TestJobRestorePreSnapshotFail(t *testing.T) {
	m, fj := newFakeManager(t)
	path, err := m.Create("mysqldump", "rootpw")
	if err != nil {
		t.Fatal(err)
	}
	fj.mu.Lock()
	fj.dumpFail = "acore_auth" // 快照第一步就失败
	fj.mu.Unlock()

	err = m.Restore(filepath.Base(path), "mysql", "mysqldump", "rootpw")
	if err == nil || !strings.Contains(err.Error(), "恢复前快照失败") {
		t.Fatalf("want pre-snapshot refusal, got: %v", err)
	}
	_, imported, stopCalls, _ := fj.snapshot()
	if len(imported) != 0 || stopCalls != 0 {
		t.Fatalf("dangerous steps ran: imported=%v stop=%d", imported, stopCalls)
	}
	if st := m.Status(); st.Active {
		t.Fatal("job lock must be released after failure")
	}
}

// 两个备份任务并发：第二个必须被互斥锁拒绝。
func TestJobConcurrentCreate(t *testing.T) {
	m, fj := newFakeManager(t)
	fj.mu.Lock()
	fj.slow = make(chan struct{})
	fj.mu.Unlock()
	done := make(chan error, 1)
	go func() { _, err := m.Create("mysqldump", "pw"); done <- err }()
	if !waitForPhase(t, m, "导出", 3*time.Second) {
		t.Fatal("first create never started")
	}
	if _, err := m.Create("mysqldump", "pw"); err == nil || !strings.Contains(err.Error(), "进行中") {
		t.Fatalf("second create must be rejected, got %v", err)
	}
	close(fj.slow)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// 备份与恢复互斥（任一方向）。
func TestJobBackupAndRestoreMutualExclusion(t *testing.T) {
	m, fj := newFakeManager(t)
	path, err := m.Create("mysqldump", "pw")
	if err != nil {
		t.Fatal(err)
	}
	fj.mu.Lock()
	fj.slow = make(chan struct{})
	fj.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- m.Restore(filepath.Base(path), "mysql", "mysqldump", "pw") }()
	// 「生成恢复前快照」阶段转瞬即逝；等快照导出被 slow 卡住的稳定阶段。
	if !waitForPhase(t, m, "导出", 3*time.Second) {
		t.Fatal("restore never started")
	}
	if _, err := m.Create("mysqldump", "pw"); err == nil || !strings.Contains(err.Error(), "进行中") {
		t.Fatalf("create during restore must be rejected, got %v", err)
	}
	close(fj.slow)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// 归档损坏（成员被篡改）：校验失败即拒绝，不进入停服/导入阶段。
func TestJobRestoreCorruptArchive(t *testing.T) {
	m, fj := newFakeManager(t)
	path, err := m.Create("mysqldump", "pw")
	if err != nil {
		t.Fatal(err)
	}
	// 解包 → 篡改 → 重打包（用包内工具复刻一个坏档）
	bad := filepath.Join(m.Paths.Backups(), "gswxy-backup-bad.tar.gz")
	raw, _ := os.ReadFile(path)
	_ = os.WriteFile(bad, raw, 0o600)
	// 直接翻转归档内一段字节（gzip 层会损坏 → unpack 或校验失败）
	for i := 200; i < 260 && i < len(raw); i++ {
		raw[i] ^= 0xFF
	}
	_ = os.WriteFile(bad, raw, 0o600)

	err = m.Restore("gswxy-backup-bad.tar.gz", "mysql", "mysqldump", "pw")
	if err == nil {
		t.Fatal("corrupt archive must be rejected")
	}
	_, imported, stopCalls, _ := fj.snapshot()
	if len(imported) != 0 || stopCalls != 0 {
		t.Fatalf("corrupt archive reached dangerous steps: imported=%v stop=%d", imported, stopCalls)
	}
}

// 数据库导入失败：明确报告部分恢复状态与回滚快照名，服务不重启。
func TestJobRestoreImportFail(t *testing.T) {
	m, fj := newFakeManager(t)
	path, err := m.Create("mysqldump", "pw")
	if err != nil {
		t.Fatal(err)
	}
	fj.mu.Lock()
	fj.importFail = "acore_world"
	fj.mu.Unlock()

	err = m.Restore(filepath.Base(path), "mysql", "mysqldump", "pw")
	if err == nil || !strings.Contains(err.Error(), "部分恢复") {
		t.Fatalf("want partial-failure report, got %v", err)
	}
	if !strings.Contains(err.Error(), "gswxy-backup-") {
		t.Fatal("error must reference the pre-restore snapshot for rollback")
	}
	_, imported, _, startCalls := fj.snapshot()
	if len(imported) != 2 { // auth + characters imported before world failed
		t.Fatalf("imported = %v, want 2 before failure", imported)
	}
	if startCalls != 0 {
		t.Fatal("services must NOT restart after failed import")
	}
}

// 服务停止失败：放弃恢复，不导入。
func TestJobRestoreStopFail(t *testing.T) {
	m, fj := newFakeManager(t)
	path, err := m.Create("mysqldump", "pw")
	if err != nil {
		t.Fatal(err)
	}
	fj.mu.Lock()
	fj.stopErr = fmt.Errorf("worldserver 未在超时内停止")
	fj.mu.Unlock()

	err = m.Restore(filepath.Base(path), "mysql", "mysqldump", "pw")
	if err == nil || !strings.Contains(err.Error(), "无法确认游戏服务已停止") {
		t.Fatalf("want stop-failure refusal, got %v", err)
	}
	_, imported, _, _ := fj.snapshot()
	if len(imported) != 0 {
		t.Fatalf("import ran despite stop failure: %v", imported)
	}
}

// 恢复成功但服务重启失败：数据已恢复，错误如实入状态、不算导入失败。
func TestJobRestoreRestartFail(t *testing.T) {
	m, fj := newFakeManager(t)
	path, err := m.Create("mysqldump", "pw")
	if err != nil {
		t.Fatal(err)
	}
	fj.mu.Lock()
	fj.startErr = fmt.Errorf("port busy")
	fj.mu.Unlock()

	if err := m.Restore(filepath.Base(path), "mysql", "mysqldump", "pw"); err != nil {
		t.Fatalf("data restore succeeded, restart failure must not fail the job: %v", err)
	}
	st := m.Status()
	if !strings.Contains(st.Error, "重启失败") {
		t.Fatalf("status error = %q, want restart-failure note", st.Error)
	}
	if !strings.Contains(st.Phase, "手动启动") {
		t.Fatalf("phase = %q", st.Phase)
	}
}

// 只跑 worldserver 的场景：恢复后只拉起 world，不碰 auth。
func TestJobRestoreRestartsOnlyWhatRan(t *testing.T) {
	m, fj := newFakeManager(t)
	path, err := m.Create("mysqldump", "pw")
	if err != nil {
		t.Fatal(err)
	}
	m.StopGameServers = func() (bool, bool, error) { return true, false, nil }
	if err := m.Restore(filepath.Base(path), "mysql", "mysqldump", "pw"); err != nil {
		t.Fatal(err)
	}
	if fj.lastStart != [2]bool{true, false} {
		t.Fatalf("restart flags = %v, want world-only", fj.lastStart)
	}
}
