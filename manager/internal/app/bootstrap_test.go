package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gswxy/gswxy-realm/manager/internal/confman"
	"github.com/gswxy/gswxy-realm/manager/internal/platform"
)

// 安装向导写入的 bootstrap.json 必须在 Manager 启动时被一次性消费：
// 密码/名称/机器人数量真正落地，文件随后删除。
func bootstrapHome(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "gsrm-boot-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) }) // logger fd may lock files on Windows
	return root
}

func TestConsumeBootstrapAppliesWizardValues(t *testing.T) {
	root := bootstrapHome(t)
	t.Setenv("GSRM_HOME", root)
	p := platform.Detect()
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(p.State(), 0o700)
	boot := `{"admin_password":"supersecret1","realm_name":"艾泽旅伴","min_bots":"200"}`
	if err := os.WriteFile(filepath.Join(p.State(), "bootstrap.json"), []byte(boot), 0o600); err != nil {
		t.Fatal(err)
	}

	a, err := New(p)
	if err != nil {
		t.Fatal(err)
	}

	if !a.Admin.Initialized() {
		t.Fatal("admin password not applied from bootstrap")
	}
	if !a.Admin.Verify("supersecret1") {
		t.Fatal("admin password mismatch after bootstrap")
	}
	if _, err := os.Stat(filepath.Join(p.State(), "bootstrap.json")); !os.IsNotExist(err) {
		t.Fatal("bootstrap.json must be consumed (deleted) after apply")
	}
	if got := a.State.Get().Realm.Name; got != "艾泽旅伴" {
		t.Fatalf("realm name not applied: %q", got)
	}
	vals, err := confman.ParseUserFile(filepath.Join(p.UserConfig(), "playerbots.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if vals["AiPlayerbot.MinRandomBots"] != "200" {
		t.Fatalf("min bots not applied: %+v", vals)
	}
	if max := vals["AiPlayerbot.MaxRandomBots"]; max == "" || max == "100" {
		t.Fatalf("max bots not derived: %q", max)
	}
}

// 短密码与越界数量：字段被跳过/收敛，而不是失败或保留文件。
func TestConsumeBootstrapValidation(t *testing.T) {
	root := bootstrapHome(t)
	t.Setenv("GSRM_HOME", root)
	p := platform.Detect()
	_ = p.EnsureDirs()
	_ = os.MkdirAll(p.State(), 0o700)
	boot := `{"admin_password":"short","realm_name":"","min_bots":"99999"}`
	_ = os.WriteFile(filepath.Join(p.State(), "bootstrap.json"), []byte(boot), 0o600)

	a, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	if a.Admin.Initialized() {
		t.Fatal("short password must be skipped")
	}
	if _, err := os.Stat(filepath.Join(p.State(), "bootstrap.json")); !os.IsNotExist(err) {
		t.Fatal("bootstrap.json must be removed even when partially invalid")
	}
	vals, _ := confman.ParseUserFile(filepath.Join(p.UserConfig(), "playerbots.conf"))
	if vals["AiPlayerbot.MinRandomBots"] != "500" {
		t.Fatalf("min bots must clamp to ceiling: %+v", vals)
	}
}

// 已设置密码的老安装收到残留 bootstrap：绝不覆盖现有密码。
func TestConsumeBootstrapNeverOverwritesPassword(t *testing.T) {
	root := bootstrapHome(t)
	t.Setenv("GSRM_HOME", root)
	p := platform.Detect()
	_ = p.EnsureDirs()
	_ = os.MkdirAll(p.State(), 0o700)

	a, err := New(p) // no bootstrap file
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Admin.SetPassword("original-pass-1"); err != nil {
		t.Fatal(err)
	}
	boot := `{"admin_password":"attacker-1234","realm_name":"evil"}`
	_ = os.WriteFile(filepath.Join(p.State(), "bootstrap.json"), []byte(boot), 0o600)
	a.ConsumeBootstrap()

	if !a.Admin.Verify("original-pass-1") {
		t.Fatal("existing password was overwritten by stale bootstrap")
	}
	if strings.Contains(string(mustRead(t, filepath.Join(p.State(), "admin.json"))), "attacker") {
		t.Fatal("bootstrap password leaked into admin store")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
