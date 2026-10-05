// Package backup implements backup/restore: auth + characters + the world
// tables GSWXY touches, plus user config and state, into a single archive
// with a manifest. Client data (dbc/maps/...) is never backed up.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gswxy/gswxy-realm/manager/internal/logging"
	"github.com/gswxy/gswxy-realm/manager/internal/platform"
)

// Manifest records provenance of one backup archive.
type Manifest struct {
	Format          string   `json:"format"` // gswxy-backup/1
	CreatedAt       string   `json:"created_at"`
	GSWXYVersion    string   `json:"gswxy_version"`
	CoreCommit      string   `json:"core_commit"`
	PlayerbotsCommit string  `json:"playerbots_commit"`
	DBVersion       string   `json:"db_version,omitempty"`
	DataVersion     string   `json:"data_version,omitempty"`
	LocaleVersion   string   `json:"locale_version,omitempty"`
	Databases       []string `json:"databases"`
	WorldTables     []string `json:"world_tables,omitempty"`
	Files           []string `json:"files"`
}

// World tables carried in backups (the ones locale import + common admin
// workflows modify). Base game data that is untouched stays re-importable
// from the FPK payload, keeping archives small.
var WorldTables = []string{
	"creature_template", "item_template", "quest_template",
	"gameobject_template", "npc_text", "page_text", "gossip_menu_option",
	"broadcast_text", "points_of_interest", "acore_string",
}

// Manager creates and restores archives.
type Manager struct {
	Paths    platform.Paths
	Log      *logging.Logger
	DB       *sql.DB
	Version  VersionInfo
}

// VersionInfo feeds the backup manifest.
type VersionInfo struct {
	GSWXY            string
	CoreCommit       string
	PlayerbotsCommit string
	DataVersion      string
	LocaleVersion    string
}

// Create writes backups/<stamp>.tar.gz and returns its path.
func (m *Manager) Create(dumpBin, rootPass string) (string, error) {
	stamp := time.Now().Format("20060102-150405")
	name := fmt.Sprintf("gswxy-backup-%s.tar.gz", stamp)
	path := filepath.Join(m.Paths.Backups(), name)

	tmp, err := os.MkdirTemp(m.Paths.Tmp, "backup-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	manifest := Manifest{
		Format:          "gswxy-backup/1",
		CreatedAt:       time.Now().Format(time.RFC3339),
		GSWXYVersion:    m.Version.GSWXY,
		CoreCommit:      m.Version.CoreCommit,
		PlayerbotsCommit: m.Version.PlayerbotsCommit,
		DataVersion:     m.Version.DataVersion,
		LocaleVersion:   m.Version.LocaleVersion,
		Databases:       []string{"acore_auth", "acore_characters"},
		WorldTables:     WorldTables,
	}

	// 1. database dumps (consistent per-database).
	for _, d := range []struct{ db, file string }{
		{"acore_auth", "auth.sql"},
		{"acore_characters", "characters.sql"},
	} {
		out := filepath.Join(tmp, d.file)
		if err := dumpDatabase(dumpBin, rootPass, d.db, out, nil); err != nil {
			return "", fmt.Errorf("dump %s: %w", d.db, err)
		}
	}
	// world: only the tracked tables.
	worldOut := filepath.Join(tmp, "world.sql")
	if err := dumpDatabase(dumpBin, rootPass, "acore_world", worldOut, WorldTables); err != nil {
		return "", fmt.Errorf("dump world tables: %w", err)
	}

	// 2. user config + state.
	copyTree(m.Paths.UserConfig(), filepath.Join(tmp, "config"))
	copyFile2(m.Paths.StateFile(), filepath.Join(tmp, "state.json"))

	// 3. version metadata snapshot.
	copyFile2(m.Paths.BuildInfo(), filepath.Join(tmp, "build-info.json"))

	// 4. pack with manifest.
	manifest.Files = []string{"auth.sql", "characters.sql", "world.sql",
		"config/", "state.json", "build-info.json"}
	mraw, _ := json.MarshalIndent(manifest, "", "  ")
	_ = os.WriteFile(filepath.Join(tmp, "manifest.json"), mraw, 0o644)

	if err := packDir(tmp, path); err != nil {
		return "", err
	}
	m.Log.Audit("system", "backup.create", name)
	m.prune(30)
	return path, nil
}

func dumpDatabase(dumpBin, pass, db, out string, tables []string) error {
	args := []string{
		"--defaults-extra-file=" + defaultsFile(dumpBin, pass),
		"--default-character-set=utf8mb4",
		"--single-transaction",
		"--quick",
	}
	if len(tables) > 0 {
		args = append(args, db)
		args = append(args, tables...)
	} else {
		args = append(args, db)
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := exec.Command(dumpBin, args...)
	cmd.Stdout = f
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8"}
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

// defaultsFile writes a 0600 client credentials file pinned to the
// bundled instance (TCP 127.0.0.1 + random port). The port comes from
// the stored credentials via the hook the app package installs.
// exported hook; set by the app package at startup
var CredsHook func() (user, pass string, port int)

func defaultsFile(dumpBin, pass string) string {
	path := "/tmp/.gswxy-mysqldump-auth"
	content := "[client]\nuser=root\npassword=" + pass + "\n"
	if CredsHook != nil {
		if u, pw, port := CredsHook(); port > 0 {
			content = fmt.Sprintf("[client]\nhost=127.0.0.1\nport=%d\nuser=%s\npassword=%s\n", port, u, pw)
		}
	}
	_ = os.WriteFile(path, []byte(content), 0o600)
	return path
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && info.IsDir() {
				return os.MkdirAll(filepath.Join(dst, filepath.Base(p)), 0o755)
			}
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		return copyFile2(p, filepath.Join(dst, rel))
	})
}

func copyFile2(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err // missing optional files are fine at their call sites
	}
	defer in.Close()
	_ = os.MkdirAll(filepath.Dir(dst), 0o755)
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func packDir(src, dst string) error {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	var files []string
	_ = filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	for _, p := range files {
		rel, _ := filepath.Rel(src, p)
		hdr := &tar.Header{
			Name: filepath.ToSlash(rel),
			Mode: 0o600,
			Size: fileSize(p),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		if _, err := io.Copy(tw, f); err != nil {
			f.Close()
			return err
		}
		f.Close()
	}
	return nil
}

func fileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

// prune keeps only the newest keep archives.
func (m *Manager) prune(keep int) {
	files, _ := filepath.Glob(filepath.Join(m.Paths.Backups(), "gswxy-backup-*.tar.gz"))
	sort.Strings(files)
	if len(files) <= keep {
		return
	}
	for _, f := range files[:len(files)-keep] {
		_ = os.Remove(f)
	}
}

// List returns archive names sorted newest-first.
func (m *Manager) List() []string {
	files, _ := filepath.Glob(filepath.Join(m.Paths.Backups(), "gswxy-backup-*.tar.gz"))
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	out := make([]string, 0, len(files))
	for _, f := range files {
		st, _ := os.Stat(f)
		size := int64(0)
		if st != nil {
			size = st.Size()
		}
		out = append(out, fmt.Sprintf("%s|%d", filepath.Base(f), size))
	}
	return out
}

// Restore pre-checks the archive, snapshots current state, then restores.
func (m *Manager) Restore(archive, mysqlBin, dumpBin, rootPass string) error {
	// Path traversal guard: the archive name must be a plain file name.
	if strings.ContainsAny(archive, "/\\") || strings.Contains(archive, "..") {
		return fmt.Errorf("非法的备份路径")
	}
	full := filepath.Join(m.Paths.Backups(), archive)
	if _, err := os.Stat(full); err != nil {
		return err
	}

	// 1. auto-backup current state first (never destroy silently).
	if _, err := m.Create(dumpBin, rootPass); err != nil {
		return fmt.Errorf("恢复前自动备份失败: %w", err)
	}

	// 2. unpack to temp (validated member paths).
	tmp, err := os.MkdirTemp(m.Paths.Tmp, "restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := unpack(full, tmp); err != nil {
		return err
	}

	// 3. re-import dumps (drop + recreate is implicit in the dump file).
	for _, pair := range [][2]string{{"auth.sql", "acore_auth"},
		{"characters.sql", "acore_characters"}, {"world.sql", "acore_world"}} {
		p := filepath.Join(tmp, pair[0])
		if _, err := os.Stat(p); err != nil {
			continue
		}
		cmd := exec.Command(mysqlBin,
			"--defaults-extra-file="+defaultsFile(dumpBin, rootPass),
			"--default-character-set=utf8mb4", pair[1])
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		cmd.Stdin = in
		cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin"}
		if out, err := cmd.CombinedOutput(); err != nil {
			in.Close()
			return fmt.Errorf("恢复 %s: %v: %s", pair[1], err, tail(string(out)))
		}
		in.Close()
	}

	// 4. restore user config + state (state last so it wins on next boot).
	copyTree(filepath.Join(tmp, "config"), m.Paths.UserConfig())
	copyFile2(filepath.Join(tmp, "state.json"), m.Paths.StateFile())
	m.Log.Audit("system", "backup.restore", archive)
	return nil
}

func unpack(archive, dst string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(filepath.FromSlash(hdr.Name))
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			return fmt.Errorf("备份内路径越界: %s", hdr.Name)
		}
		out := filepath.Join(dst, name)
		if hdr.Typeflag == tar.TypeDir {
			_ = os.MkdirAll(out, 0o755)
			continue
		}
		_ = os.MkdirAll(filepath.Dir(out), 0o755)
		w, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, tr); err != nil {
			w.Close()
			return err
		}
		w.Close()
	}
}

func tail(s string) string {
	if len(s) > 400 {
		return s[len(s)-400:]
	}
	return s
}

// Checksum returns the sha256 of a file (for the UI).
func Checksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
