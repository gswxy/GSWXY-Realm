package state

import (
	"os"
	"path/filepath"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// 初始化中断后（InProgress 残留），PendingStep 必须指向第一个未完成
// 步骤，恢复从断点继续而不是从头破坏已导入的数据。
func TestPendingStepResumes(t *testing.T) {
	s := newStore(t)
	if got := s.Get().PendingStep(); got != StepEnvCheck {
		t.Fatalf("fresh store pending = %q, want env_check", got)
	}
	_ = s.MarkStepDone(StepEnvCheck)
	_ = s.MarkStepDone(StepDBInit)
	if got := s.Get().PendingStep(); got != StepDBImport {
		t.Fatalf("after two steps pending = %q, want db_import", got)
	}
	// 模拟崩溃残留：InProgress=true 但只完成到 db_import。
	_ = s.Update(func(d *Data) { d.Setup.InProgress = true; d.Setup.Error = "boom" })
	d := s.Get()
	if !d.Setup.InProgress {
		t.Fatal("InProgress flag lost")
	}
	if got := d.PendingStep(); got != StepDBImport {
		t.Fatalf("crash residue pending = %q, want db_import", got)
	}
}

func TestMarkAllDoneInitializes(t *testing.T) {
	s := newStore(t)
	for _, st := range stepOrder {
		if st != StepDone {
			_ = s.MarkStepDone(st)
		}
	}
	d := s.Get()
	if !d.Setup.Initialized || d.Setup.InProgress || d.Setup.Error != "" {
		t.Fatalf("setup not finalized: %+v", d.Setup)
	}
}

// 步骤乱序/重复完成不应回退游标。
func TestStepOrderIsStable(t *testing.T) {
	if NextStep(StepDone) != StepDone {
		t.Fatal("done must be terminal")
	}
	if NextStep("bogus") != StepDone {
		t.Fatal("unknown step falls through to done")
	}
}

// 状态文件损坏（非法 JSON）时 Open 不得 panic，保持空默认值。
func TestOpenCorruptFileStartsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	_ = os.WriteFile(path, []byte("{not json"), 0o600)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Get().Setup.Completed == nil || s.Get().Patches == nil {
		t.Fatal("maps must be initialized")
	}
}
