// Package backup implements backup/restore of ALL user databases (auth,
// characters, world user-modified tables, playerbots), plus user config and
// state, into a single archive with a verified manifest. Client data
// (dbc/maps/...) is never backed up — it is re-downloadable.
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
	"sync/atomic"
	"time"

	"github.com/gswxy/gswxy-realm/manager/internal/dbinit"
	"github.com/gswxy/gswxy-realm/manager/internal/logging"
	"github.com/gswxy/gswxy-realm/manager/internal/platform"
	"github.com/gswxy/gswxy-realm/manager/internal/version"
)

// Manifest records provenance of one backup archive.
type Manifest struct {
	Format           string            `json:"format"` // gswxy-backup/1
	CreatedAt        string            `json:"created_at"`
	GSWXYVersion     string            `json:"gswxy_version"`
	CoreCommit       string            `json:"core_commit"`
	PlayerbotsCommit string            `json:"playerbots_commit"`
	DBVersion        string            `json:"db_version,omitempty"`
	DataVersion      string            `json:"data_version,omitempty"`
	LocaleVersion    string            `json:"locale_version,omitempty"`
	Databases        []string          `json:"databases"`
	WorldTables      []string          `json:"world_tables,omitempty"`
	Files            []string          `json:"files"`
	Checksums        map[string]string `json:"checksums"` // member -> sha256
}

// World tables carried in backups (the ones locale import + common admin
// workflows modify). Base game data that is untouched stays re-importable
// from the FPK payload, keeping archives small.
var WorldTables = []string{
	"creature_template", "item_template", "quest_template",
	"gameobject_template", "npc_text", "page_text", "gossip_menu_option",
	"broadcast_text", "points_of_interest", "acore_string",
}

// Retention: at most keepCount archives AND keepBytes total (oldest pruned
// first) so automatic daily backups cannot silently fill the volume.
const (
	keepCount  = 30
	keepBytes  = 24 << 30 // 24 GiB
	memberCap  = 8 << 30  // single-member size cap while unpacking
	archiveCap = 32 << 30 // total unpacked cap while unpacking
)

// Job status surfaced to the WebUI.
type Job struct {
	Active bool   `json:"active"`
	Kind   string `json:"kind,omitempty"` // backup | restore
	Phase  string `json:"phase,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Manager creates and restores archives.
type Manager struct {
	Paths   platform.Paths
	Log     *logging.Logger
	DB      *sql.DB
	Version VersionInfo

	// StopGameServers / StartGameServers are injected by the app package:
	// restore must quiesce worldserver/authserver (never mysqld — the
	// import needs it) and bring back whichever were running before.
	StopGameServers  func() (wereRunning bool)
	StartGameServers func() error

	jobMu   atomic.Bool
	jobKind atomic.Value // string
	jobPh   atomic.Value // string
	jobErr  atomic.Value // string
}

// VersionInfo feeds the backup manifest.
type VersionInfo struct {
	GSWXY            string
	CoreCommit       string
	PlayerbotsCommit string
	DataVersion      string
	LocaleVersion    string
}

func (m *Manager) setPhase(kind, phase string) {
	m.jobKind.Store(kind)
	m.jobPh.Store(phase)
	m.Log.Info("backup job %s: %s", kind, phase)
}

func (m *Manager) jobFail(kind string, err error) error {
	m.jobErr.Store(err.Error())
	m.Log.Error("backup job %s failed: %v", kind, err)
	return err
}

// Status returns the current/last job state (value copies).
func (m *Manager) Status() Job {
	j := Job{}
	if v, ok := m.jobKind.Load().(string); ok {
		j.Kind = v
	}
	if v, ok := m.jobPh.Load().(string); ok {
		j.Phase = v
	}
	if v, ok := m.jobErr.Load().(string); ok {
		j.Error = v
	}
	j.Active = m.jobMu.Load()
	return j
}

// dumpTargets is the ordered dump plan. acore_playerbots 必须完整备份：
// 机器人绑定（account_links）、自定义策略、任务/装备缓存都在这库里。
var dumpTargets = []struct{ db, file string }{
	{"acore_auth", "auth.sql"},
	{"acore_characters", "characters.sql"},
	{"acore_world", "world.sql"}, // tracked tables only
	{"acore_playerbots", "playerbots.sql"},
}

// Create writes backups/<stamp>.tar.gz and returns its path. One job at a
// time; failures return the error (never reported as success).
func (m *Manager) Create(dumpBin, rootPass string) (string, error) {
	if !m.jobMu.CompareAndSwap(false, true) {
		return "", fmt.Errorf("已有备份/恢复任务在进行中")
	}
	defer m.jobMu.Store(false)
	m.jobErr.Store("")
	m.setPhase("backup", "准备")

	stamp := time.Now().Format("20060102-150405")
	name := fmt.Sprintf("gswxy-backup-%s.tar.gz", stamp)
	path := filepath.Join(m.Paths.Backups(), name)

	tmp, err := os.MkdirTemp(m.Paths.Tmp, "backup-")
	if err != nil {
		return "", m.jobFail("backup", err)
	}
	defer os.RemoveAll(tmp)

	manifest := Manifest{
		Format:           "gswxy-backup/1",
		CreatedAt:        time.Now().Format(time.RFC3339),
		GSWXYVersion:     m.Version.GSWXY,
		CoreCommit:       m.Version.CoreCommit,
		PlayerbotsCommit: m.Version.PlayerbotsCommit,
		DataVersion:      m.Version.DataVersion,
		LocaleVersion:    m.Version.LocaleVersion,
		Databases:        []string{"acore_auth", "acore_characters", "acore_world(部分表)", "acore_playerbots"},
		WorldTables:      WorldTables,
		Checksums:        map[string]string{},
	}

	for _, d := range dumpTargets {
		m.setPhase("backup", "导出 "+d.db)
		out := filepath.Join(tmp, d.file)
		tables := []string(nil)
		if d.db == "acore_world" {
			tables = WorldTables
		}
		if err := dumpDatabase(m.Paths, dumpBin, rootPass, d.db, out, tables); err != nil {
			return "", m.jobFail("backup", fmt.Errorf("dump %s: %w", d.db, err))
		}
		sum, err := fileSHA256(out)
		if err != nil {
			return "", m.jobFail("backup", err)
		}
		manifest.Checksums[d.file] = sum
		manifest.Files = append(manifest.Files, d.file)
	}

	m.setPhase("backup", "打包配置与状态")
	copyTree(m.Paths.UserConfig(), filepath.Join(tmp, "config"))
	copyFile2(m.Paths.StateFile(), filepath.Join(tmp, "state.json"))
	copyFile2(m.Paths.BuildInfo(), filepath.Join(tmp, "build-info.json"))
	for _, f := range []string{"state.json", "build-info.json"} {
		if _, err := os.Stat(filepath.Join(tmp, f)); err == nil {
			manifest.Files = append(manifest.Files, f)
		}
	}
	manifest.Files = append(manifest.Files, "config/")

	m.setPhase("backup", "压缩归档")
	mraw, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, "manifest.json"), mraw, 0o600); err != nil {
		return "", m.jobFail("backup", err)
	}
	if err := packDir(tmp, path); err != nil {
		return "", m.jobFail("backup", err)
	}
	m.Log.Audit("system", "backup.create", name)
	m.setPhase("backup", "完成")
	m.prune()
	return path, nil
}

func dumpDatabase(paths platform.Paths, dumpBin, pass, db, out string, tables []string) error {
	args := []string{
		"--defaults-extra-file=" + defaultsFile(pass),
		"--default-character-set=utf8mb4",
		"--single-transaction",
		"--quick",
		db,
	}
	args = append(args, tables...)
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := exec.Command(dumpBin, args...)
	cmd.Stdout = f
	cmd.Stderr = os.Stderr
	cmd.Env = dbinit.ClientEnv(paths)
	return cmd.Run()
}

// defaultsFile writes a 0600 client credentials file pinned to the bundled
// instance (TCP 127.0.0.1 + random port). Unique per invocation: concurrent
// dump/restore calls must not clobber each other's credentials.
var defaultsSeq atomic.Int64

// exported hook; set by the app package at startup (port + creds).
var CredsHook func() (user, pass string, port int)

func defaultsFile(pass string) string {
	seq := defaultsSeq.Add(1)
	path := filepath.Join(os.TempDir(), fmt.Sprintf("gswxy-mysql-auth-%d-%d.cnf", os.Getpid(), seq))
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
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
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

// prune enforces both retention rules: newest keepCount archives, and a
// total size budget (oldest deleted first beyond it).
func (m *Manager) prune() {
	files, _ := filepath.Glob(filepath.Join(m.Paths.Backups(), "gswxy-backup-*.tar.gz"))
	sort.Strings(files) // stamps sort chronologically
	if len(files) > keepCount {
		for _, f := range files[:len(files)-keepCount] {
			_ = os.Remove(f)
		}
		files = files[len(files)-keepCount:]
	}
	var total int64
	sizes := map[string]int64{}
	for _, f := range files {
		st, _ := os.Stat(f)
		sizes[f] = st.Size()
		total += st.Size()
	}
	for _, f := range files { // oldest first
		if total <= keepBytes {
			break
		}
		total -= sizes[f]
		_ = os.Remove(f)
		m.Log.Info("pruned old backup %s (size budget)", filepath.Base(f))
	}
}

// ListItem is one archive row for the UI.
type ListItem struct {
	File string `json:"file"`
	Size int64  `json:"size"`
	At   string `json:"at"` // parsed from the stamp when possible
}

// List returns archives sorted newest-first.
func (m *Manager) List() []ListItem {
	files, _ := filepath.Glob(filepath.Join(m.Paths.Backups(), "gswxy-backup-*.tar.gz"))
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	out := make([]ListItem, 0, len(files))
	for _, f := range files {
		st, _ := os.Stat(f)
		size := int64(0)
		if st != nil {
			size = st.Size()
		}
		at := ""
		if t, err := time.ParseInLocation("20060102-150405",
			strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "gswxy-backup-"), ".tar.gz"),
			time.Local); err == nil {
			at = t.Format(time.RFC3339)
		}
		out = append(out, ListItem{File: filepath.Base(f), Size: size, At: at})
	}
	return out
}

// Restore pre-checks the archive, snapshots current state, quiesces the
// game servers, re-imports every database and finally merges state.
func (m *Manager) Restore(archive, mysqlBin, dumpBin, rootPass string) error {
	if !m.jobMu.CompareAndSwap(false, true) {
		return fmt.Errorf("已有备份/恢复任务在进行中")
	}
	defer m.jobMu.Store(false)
	m.jobErr.Store("")

	// Path traversal guard: the archive name must be a plain file name.
	if strings.ContainsAny(archive, "/\\") || strings.Contains(archive, "..") {
		return m.jobFail("restore", fmt.Errorf("非法的备份路径"))
	}
	full := filepath.Join(m.Paths.Backups(), archive)
	if _, err := os.Stat(full); err != nil {
		return m.jobFail("restore", err)
	}

	m.setPhase("restore", "校验备份档案")
	tmp, err := os.MkdirTemp(m.Paths.Tmp, "restore-")
	if err != nil {
		return m.jobFail("restore", err)
	}
	defer os.RemoveAll(tmp)
	if err := unpack(full, tmp); err != nil {
		return m.jobFail("restore", err)
	}
	manifest, err := verifyManifest(tmp)
	if err != nil {
		return m.jobFail("restore", err)
	}
	// 恢复到比当前安装旧的程序上可能带入其不认识的结构 —— 拒绝。
	if cmp := version.Compare(manifest.GSWXYVersion, m.Version.GSWXY); cmp > 0 {
		return m.jobFail("restore", fmt.Errorf(
			"备份来自更新版本 %s（当前 %s），为避免结构不兼容已拒绝恢复；请先升级应用",
			manifest.GSWXYVersion, m.Version.GSWXY))
	}

	// Never destroy silently: snapshot current state first.
	m.setPhase("restore", "生成恢复前自动备份")
	if _, err := m.Create(dumpBin, rootPass); err != nil {
		return m.jobFail("restore", fmt.Errorf("恢复前自动备份失败: %w", err))
	}

	// Quiesce game servers (worldserver/authserver) before importing.
	m.setPhase("restore", "停止游戏服务")
	wereRunning := false
	if m.StopGameServers != nil {
		wereRunning = m.StopGameServers()
	}

	for _, pair := range [][2]string{
		{"auth.sql", "acore_auth"},
		{"characters.sql", "acore_characters"},
		{"world.sql", "acore_world"},
		{"playerbots.sql", "acore_playerbots"},
	} {
		p := filepath.Join(tmp, pair[0])
		if _, err := os.Stat(p); err != nil {
			continue // archive without this member (older format)
		}
		m.setPhase("restore", "导入 "+pair[1])
		cmd := exec.Command(mysqlBin,
			"--defaults-extra-file="+defaultsFile(rootPass),
			"--default-character-set=utf8mb4", pair[1])
		in, err := os.Open(p)
		if err != nil {
			return m.jobFail("restore", err)
		}
		cmd.Stdin = in
		cmd.Env = dbinit.ClientEnv(m.Paths)
		out, err := cmd.CombinedOutput()
		in.Close()
		if err != nil {
			return m.jobFail("restore", fmt.Errorf("恢复 %s: %v: %s", pair[1], err, tail(string(out))))
		}
	}

	m.setPhase("restore", "恢复用户配置与状态")
	copyTree(filepath.Join(tmp, "config"), m.Paths.UserConfig())
	if err := mergeStateFile(filepath.Join(tmp, "state.json"), m.Paths.StateFile()); err != nil {
		m.Log.Warn("restore: merge state: %v", err)
	}

	if wereRunning && m.StartGameServers != nil {
		m.setPhase("restore", "重启游戏服务")
		if err := m.StartGameServers(); err != nil {
			m.jobErr.Store("服务重启失败: " + err.Error())
			m.Log.Error("restore: restart servers: %v", err)
		}
	}
	m.Log.Audit("system", "backup.restore", archive)
	m.setPhase("restore", "完成")
	return nil
}

// verifyManifest checks the archive format and every recorded member
// checksum before anything touches the live databases.
func verifyManifest(dir string) (*Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("备份缺少 manifest.json: %w", err)
	}
	var mf Manifest
	if err := json.Unmarshal(raw, &mf); err != nil {
		return nil, fmt.Errorf("manifest 解析失败: %w", err)
	}
	if mf.Format != "gswxy-backup/1" {
		return nil, fmt.Errorf("未知的备份格式: %s", mf.Format)
	}
	for name, want := range mf.Checksums {
		sum, err := fileSHA256(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("备份成员缺失: %s: %w", name, err)
		}
		if sum != want {
			return nil, fmt.Errorf("备份成员校验失败: %s", name)
		}
	}
	// No checksums recorded (very old archive) — require at least the SQL
	// members to exist so a truncated archive fails here, not mid-restore.
	if len(mf.Checksums) == 0 {
		for _, f := range []string{"auth.sql", "characters.sql"} {
			if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
				return nil, fmt.Errorf("备份缺少数据库导出: %s", f)
			}
		}
	}
	return &mf, nil
}

// mergeStateFile merges a restored state.json into the local one: machine
// specific facts (database port/socket, client data, setup progress, last
// download) stay LOCAL — overwriting them with the backup's values would
// point this install at a database that does not exist here. Realm name,
// locale and patch bookkeeping come from the backup (they describe the
// restored databases).
func mergeStateFile(src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		return nil // archive without state.json
	}
	cur, err := platform.ReadState(dst)
	if err != nil {
		cur = map[string]any{}
	}
	restored, err := platform.ReadState(src)
	if err != nil {
		return err
	}
	for _, key := range []string{"database", "client_data", "setup", "download"} {
		if v, ok := cur[key]; ok {
			restored[key] = v
		} else {
			delete(restored, key)
		}
	}
	for _, key := range []string{"locale", "patches"} {
		if v, ok := restored[key]; ok {
			cur[key] = v
		}
	}
	// realm: take the name from the backup; addresses are machine-local.
	if r, ok := restored["realm"].(map[string]any); ok {
		if c, ok2 := cur["realm"].(map[string]any); ok2 {
			if name, ok3 := r["name"]; ok3 {
				c["name"] = name
			}
			r["address"] = c["address"]
			r["local_address"] = c["local_address"]
		}
	}
	raw, err := json.MarshalIndent(restored, "", "  ")
	if err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// unpack extracts the archive with traversal, type and size guards. Only
// regular files and directories are honored; symlinks/hardlinks/devices
// are rejected outright.
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
	var total int64
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
		switch hdr.Typeflag {
		case tar.TypeDir:
			_ = os.MkdirAll(filepath.Join(dst, name), 0o755)
			continue
		case tar.TypeReg:
		default:
			return fmt.Errorf("备份内含非普通文件成员: %s", hdr.Name)
		}
		if hdr.Size > memberCap || total+hdr.Size > archiveCap {
			return fmt.Errorf("备份成员过大: %s (%d bytes)", hdr.Name, hdr.Size)
		}
		out := filepath.Join(dst, name)
		_ = os.MkdirAll(filepath.Dir(out), 0o755)
		w, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		n, err := io.Copy(w, tr)
		w.Close()
		if err != nil {
			return err
		}
		total += n
	}
}

func fileSHA256(path string) (string, error) {
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

func tail(s string) string {
	if len(s) > 400 {
		return s[len(s)-400:]
	}
	return s
}

// Checksum returns the sha256 of a file (for the UI).
func Checksum(path string) (string, error) { return fileSHA256(path) }
