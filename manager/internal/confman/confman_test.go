package confman

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleDist = `[worldserver]

###################################################################################################
# RATE SETTINGS
#
#    Rate.XP.Kill
#        Description: XP rates
#        Default: 1
###################################################################################################

Rate.XP.Kill = 1

###################################################################################################
# CONSOLE
###################################################################################################

#    Enable console
Console.Enable = true

WorldServerPort = 8085
Motd = "Welcome traveler"
`

func writeDist(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "worldserver.conf.dist")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseDistSectionsAndComments(t *testing.T) {
	sc, err := ParseDist(writeDist(t, sampleDist))
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Entries) != 4 {
		t.Fatalf("entries = %d, want 4", len(sc.Entries))
	}
	rate, ok := sc.Get("Rate.XP.Kill")
	if !ok {
		t.Fatal("Rate.XP.Kill missing")
	}
	if rate.Section != "RATE SETTINGS" {
		t.Fatalf("section not parsed: %q", rate.Section)
	}
	if rate.Comment == "" {
		t.Fatal("comment not attached")
	}
	if rate.Type != "int" {
		t.Fatalf("Rate.XP.Kill type = %s", rate.Type)
	}
	cons, _ := sc.Get("Console.Enable")
	if cons.Type != "bool" { // default is "true" in the sample
		t.Fatalf("Console.Enable type = %s", cons.Type)
	}
	if cons.Section != "CONSOLE" {
		t.Fatalf("second section not tracked: %q", cons.Section)
	}
	motd, _ := sc.Get("Motd")
	if motd.Type != "string" || motd.Default != "Welcome traveler" {
		t.Fatalf("Motd misparsed: %+v", motd)
	}
	port, _ := sc.Get("WorldServerPort")
	if port.Default != "8085" {
		t.Fatalf("WorldServerPort default = %q", port.Default)
	}
	if len(sc.Sections) != 2 {
		t.Fatalf("sections = %v, want [RATE SETTINGS CONSOLE]", sc.Sections)
	}
}

// 目录页（SECTION INDEX）里的同类标题行没有紧跟边框，不得被当作节；
// 其后第一个真正的节标题生效。
func TestParseDistSkipsTableOfContents(t *testing.T) {
	dist := `###################################################################################################
# SECTION INDEX
#   SERVER SYSTEM SETTINGS
#    DATABASE & CONNECTIONS
###################################################################################################

###################################################################################################
# NETWORK SETTINGS
###################################################################################################

WorldServerPort = 8085
`
	sc, err := ParseDist(writeDist(t, dist))
	if err != nil {
		t.Fatal(err)
	}
	if sc.Entries[0].Section != "NETWORK SETTINGS" {
		t.Fatalf("TOC entry leaked as section: %q", sc.Entries[0].Section)
	}
}

// Effective 必须带上 Section/Comment/Type/Restart —— 配置页的分组下拉
// 与说明文案依赖这些字段（历史缺陷：前端读了后端没给的字段）。
func TestResolveCarriesSchemaFields(t *testing.T) {
	dir := t.TempDir()
	dist, err := ParseDist(writeDist(t, sampleDist))
	if err != nil {
		t.Fatal(err)
	}
	userDir := filepath.Join(dir, "user")
	_ = os.MkdirAll(userDir, 0o755)
	_ = os.WriteFile(filepath.Join(userDir, "worldserver.conf"),
		[]byte("Rate.XP.Kill = 5\n"), 0o600)
	m := &Manager{
		UserDir: userDir,
		RunDir:  filepath.Join(dir, "run"),
		DistDir: dir,
		Rec:     staticRec{"worldserver.conf": {"Console.Enable": "0"}},
	}
	eff, err := m.Resolve("worldserver.conf", dist)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]Effective{}
	for _, e := range eff {
		byKey[e.Key] = e
	}
	rate := byKey["Rate.XP.Kill"]
	if rate.User != "5" || rate.Value != "5" || !rate.Overridden {
		t.Fatalf("user layer not applied: %+v", rate)
	}
	if rate.Section == "" || rate.Comment == "" || rate.Type == "" {
		t.Fatalf("schema fields missing from Effective: %+v", rate)
	}
	cons := byKey["Console.Enable"]
	if cons.Recommended != "0" || cons.Value != "0" {
		t.Fatalf("recommended layer not applied: %+v", cons)
	}
	port := byKey["WorldServerPort"]
	if port.Value != "8085" || port.Overridden {
		t.Fatalf("upstream default wrong: %+v", port)
	}
}

type staticRec map[string]map[string]string

func (s staticRec) Recommended(conf, key string) (string, bool) {
	if m, ok := s[conf]; ok {
		v, ok2 := m[key]
		return v, ok2
	}
	return "", false
}

func TestValidateValue(t *testing.T) {
	cases := []struct {
		typ, val string
		ok       bool
	}{
		{"bool", "1", true},
		{"bool", "true", true},
		{"bool", "yes", true},
		{"bool", "2", false},
		{"bool", "maybe", false},
		{"int", "8085", true},
		{"int", "-5", true},
		{"int", "8.5", false},
		{"int", "", false},
		{"float", "0.5", true},
		{"float", "abc", false},
		{"string", "anything", true},
		{"string", "line\nbreak", false},
	}
	for _, c := range cases {
		err := ValidateValue(&Entry{Key: "K", Type: c.typ}, c.val)
		if (err == nil) != c.ok {
			t.Errorf("ValidateValue(%s, %q) err=%v, want ok=%v", c.typ, c.val, err, c.ok)
		}
	}
}

// SetUser → ParseUserFile roundtrip（模板/向导写入路径的基石）。
func TestSetUserRoundtrip(t *testing.T) {
	dir := t.TempDir()
	m := &Manager{UserDir: dir, RunDir: filepath.Join(dir, "run"), DistDir: dir}
	if err := m.SetUser("playerbots.conf", "AiPlayerbot.MinRandomBots", "80"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetUser("playerbots.conf", "AiPlayerbot.WorldChat", "hello world"); err != nil {
		t.Fatal(err)
	}
	vals, err := ParseUserFile(m.UserPath("playerbots.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if vals["AiPlayerbot.MinRandomBots"] != "80" {
		t.Fatalf("roundtrip lost: %+v", vals)
	}
	if vals["AiPlayerbot.WorldChat"] != "hello world" {
		t.Fatalf("quoted roundtrip lost: %+v", vals)
	}
	// update in place
	_ = m.SetUser("playerbots.conf", "AiPlayerbot.MinRandomBots", "120")
	vals, _ = ParseUserFile(m.UserPath("playerbots.conf"))
	if vals["AiPlayerbot.MinRandomBots"] != "120" {
		t.Fatal("update in place failed")
	}
	// delete
	if err := m.DeleteUserKey("playerbots.conf", "AiPlayerbot.MinRandomBots"); err != nil {
		t.Fatal(err)
	}
	vals, _ = ParseUserFile(m.UserPath("playerbots.conf"))
	if _, ok := vals["AiPlayerbot.MinRandomBots"]; ok {
		t.Fatal("delete failed")
	}
}
