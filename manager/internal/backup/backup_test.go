package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildArchive packs files (name -> content) into a tar.gz the same way
// packDir does (regular files only, relative names).
func buildArchive(t *testing.T, path string, files map[string]string, headerRewrite func(hdr *tar.Header)) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(content))}
		if headerRewrite != nil {
			headerRewrite(hdr)
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
}

func TestUnpackRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	arc := filepath.Join(dir, "evil.tar.gz")
	buildArchive(t, arc, map[string]string{"ok.txt": "hi"}, func(h *tar.Header) {
		if h.Name == "ok.txt" {
			h.Name = "../escape.txt"
		}
	})
	err := unpack(arc, filepath.Join(dir, "out"))
	if err == nil || !strings.Contains(err.Error(), "越界") {
		t.Fatalf("traversal not rejected: %v", err)
	}
}

func TestUnpackRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	arc := filepath.Join(dir, "link.tar.gz")
	f, err := os.Create(arc)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "lnk", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	tw.Close()
	gz.Close()
	f.Close()
	if err := unpack(arc, filepath.Join(dir, "out")); err == nil {
		t.Fatal("symlink member must be rejected")
	}
}

func TestUnpackRejectsOversize(t *testing.T) {
	dir := t.TempDir()
	arc := filepath.Join(dir, "big.tar.gz")
	buildArchive(t, arc, map[string]string{"big.bin": "x"}, func(h *tar.Header) {
		if h.Name == "big.bin" {
			h.Size = memberCap + 1 // header lies about size; reader hits EOF early
			h.Name = "big.bin"
		}
	})
	// The declared size alone must trip the cap before reading.
	if err := unpack(arc, filepath.Join(dir, "out")); err == nil {
		t.Fatal("oversize member must be rejected")
	}
}

func sha256Str(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func writeVerifiedArchive(t *testing.T, dir string, sqls map[string]string) string {
	t.Helper()
	staging := filepath.Join(dir, "stage")
	for name, content := range sqls {
		_ = os.MkdirAll(staging, 0o755)
		if err := os.WriteFile(filepath.Join(staging, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	checksums := map[string]string{}
	for name, content := range sqls {
		checksums[name] = sha256Str(content)
	}
	mf := Manifest{Format: "gswxy-backup/1", GSWXYVersion: "1.0.0", Checksums: checksums}
	raw, _ := json.Marshal(mf)
	if err := os.WriteFile(filepath.Join(staging, "manifest.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	arc := filepath.Join(dir, "good.tar.gz")
	if err := packDir(staging, arc); err != nil {
		t.Fatal(err)
	}
	return arc
}

func TestVerifyManifestOKAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	arc := writeVerifiedArchive(t, dir, map[string]string{"auth.sql": "SELECT 1;", "characters.sql": "SELECT 2;"})
	out := filepath.Join(dir, "out1")
	if err := unpack(arc, out); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyManifest(out); err != nil {
		t.Fatalf("good archive rejected: %v", err)
	}
	// Corrupt one member after unpack: checksum must fail.
	if err := os.WriteFile(filepath.Join(out, "auth.sql"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyManifest(out); err == nil {
		t.Fatal("corrupted member passed checksum")
	}
}

func TestVerifyManifestUnknownFormat(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	_ = os.MkdirAll(out, 0o755)
	raw, _ := json.Marshal(Manifest{Format: "gswxy-backup/9"})
	_ = os.WriteFile(filepath.Join(out, "manifest.json"), raw, 0o600)
	if _, err := verifyManifest(out); err == nil {
		t.Fatal("unknown format accepted")
	}
}

// mergeStateFile：本机事实（数据库端口/客户端数据/初始化进度）必须保留
// 本地值——备份里的端口属于源机器，覆盖会让本机连不上数据库。
func TestMergeStatePreservesMachineLocal(t *testing.T) {
	dir := t.TempDir()
	local := fmt.Sprintf(`{"database":{"port":53001},"client_data":{"version":"v20.0","installed":true},
		"setup":{"initialized":true},"realm":{"name":"旧名","address":"1.2.3.4"},
		"locale":{"version":"1.0"},"patches":{"a":{"applied_at":"now"}}}`, )
	restored := fmt.Sprintf(`{"database":{"port":53002},"client_data":{"version":"v9.0","installed":false},
		"setup":{"initialized":false},"realm":{"name":"备份里的名","address":"9.9.9.9"},
		"locale":{"version":"2.0"},"patches":{"b":{"applied_at":"then"}}}`, )
	lp := filepath.Join(dir, "state.json")
	rp := filepath.Join(dir, "restored-state.json")
	_ = os.WriteFile(lp, []byte(local), 0o600)
	_ = os.WriteFile(rp, []byte(restored), 0o600)

	if err := mergeStateFile(rp, lp); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(lp)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(got, &m)
	db := m["database"].(map[string]any)
	if db["port"].(float64) != 53001 {
		t.Fatalf("local DB port overwritten: %v", db["port"])
	}
	cd := m["client_data"].(map[string]any)
	if cd["version"] != "v20.0" || cd["installed"] != true {
		t.Fatalf("local client data overwritten: %v", cd)
	}
	if m["setup"].(map[string]any)["initialized"] != true {
		t.Fatal("local setup progress overwritten")
	}
	realm := m["realm"].(map[string]any)
	if realm["name"] != "备份里的名" {
		t.Fatalf("realm name not taken from backup: %v", realm["name"])
	}
	if realm["address"] != "1.2.3.4" {
		t.Fatalf("machine-local realm address overwritten: %v", realm["address"])
	}
	if m["locale"].(map[string]any)["version"] != "2.0" {
		t.Fatal("locale version not taken from backup")
	}
}

func TestMergeStateMissingSourceNoop(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "state.json")
	_ = os.WriteFile(dst, []byte(`{"database":{"port":1}}`), 0o600)
	if err := mergeStateFile(filepath.Join(dir, "none.json"), dst); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(dst)
	if !strings.Contains(string(raw), `"port":1`) {
		t.Fatal("state must be untouched when archive has no state.json")
	}
}
