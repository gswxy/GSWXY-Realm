package mirrors

import (
	"os"
	"path/filepath"
	"testing"
)

// Fill 展开占位符后必须得到完整 https URL。
func TestClientDataMirrorFill(t *testing.T) {
	m := ClientDataMirror{Name: "x", URL: "https://m.example/gh/wowgaming/client-data/releases/download/{version}/{filename}"}
	got := m.Fill("v20.0", "Data.zip")
	want := "https://m.example/gh/wowgaming/client-data/releases/download/v20.0/Data.zip"
	if got != want {
		t.Fatalf("fill = %q", got)
	}
}

func TestFPKMirrorFill(t *testing.T) {
	f := FPKMirror{Name: "x", URL: "https://m.example/gh/gswxy/GSWXY-Realm/releases/download/{tag}/{filename}"}
	got := f.Fill("v1.1.0", "GSWXY-Realm-1.1.0-x86_64.fpk")
	if got != "https://m.example/gh/gswxy/GSWXY-Realm/releases/download/v1.1.0/GSWXY-Realm-1.1.0-x86_64.fpk" {
		t.Fatalf("fill = %q", got)
	}
}

// 非 https 线路一律丢弃（禁止明文或未知协议）。
func TestLoadFiltersNonHTTPS(t *testing.T) {
	dir := t.TempDir()
	doc := `{
	  "client_data_mirrors": [
	    {"name":"好","url":"https://a.example/x/{version}/{filename}"},
	    {"name":"坏http","url":"http://b.example/x/{version}/{filename}"}
	  ],
	  "github_api_mirrors": ["https://c.example/api", "http://d.example/api"],
	  "fpk_mirror": {"name":"坏","url":"http://e.example/{tag}/{filename}"}
	}`
	_ = os.WriteFile(filepath.Join(dir, "mirrors.json"), []byte(doc), 0o600)
	c := Load(dir)
	if len(c.ClientDataMirrors) != 1 || c.ClientDataMirrors[0].Name != "好" {
		t.Fatalf("data mirrors = %+v", c.ClientDataMirrors)
	}
	if len(c.APIMirrors) != 1 {
		t.Fatalf("api mirrors = %+v", c.APIMirrors)
	}
	if c.FPKMirror != nil {
		t.Fatal("non-https fpk mirror must be dropped")
	}
}

// 配置文件缺失/损坏时回退默认，不得 panic。
func TestLoadFallback(t *testing.T) {
	c := Load(t.TempDir()) // no file
	if len(c.APIMirrors) == 0 {
		t.Fatal("default config must keep an API mirror")
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "mirrors.json"), []byte("{broken"), 0o600)
	c2 := Load(dir)
	if len(c2.APIMirrors) == 0 {
		t.Fatal("broken config must fall back to defaults")
	}
}

// 仓库自带的正式配置必须可解析且全部 https。
func TestShippedConfigIsValid(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "resources", "mirrors.json"))
	if err != nil {
		t.Skip("shipped config not reachable from test sandbox")
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "mirrors.json"), raw, 0o600)
	c := Load(dir)
	if len(c.ClientDataMirrors) == 0 {
		t.Fatal("shipped config has no data mirrors")
	}
	for _, m := range c.ClientDataMirrors {
		if m.URL[:8] != "https://" {
			t.Fatalf("non-https mirror shipped: %s", m.URL)
		}
	}
}
