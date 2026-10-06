// Three-layer configuration model and final conf generation.
package confman

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Layer identifies one of the three configuration sources.
const (
	LayerUpstream   = "upstream"
	LayerRecommended = "gswxy"
	LayerUser       = "user"
)

// ConfFile names of the managed configs.
var ConfFiles = []string{"worldserver.conf", "authserver.conf", "playerbots.conf"}

// RecSource provides GSWXY recommended values (embedded JSON in the binary).
type RecSource interface {
	Recommended(conf, key string) (string, bool)
}

// Effective is the resolved value triple for one key.
type Effective struct {
	Key         string `json:"key"`
	Conf        string `json:"conf"`
	Upstream    string `json:"upstream"`
	Recommended string `json:"recommended"`
	User        string `json:"user"`
	Value       string `json:"value"` // effective
	Overridden  bool   `json:"overridden"`
	UpstreamChanged string `json:"upstream_changed,omitempty"` // upstream default differs from recorded baseline
}

// Manager renders final configs from the three layers.
type Manager struct {
	UserDir  string // persistent user config dir (var/config)
	RunDir   string // generated run dir            (var/config/run)
	DistDir  string // payload etc dir              (target/etc)
	Rec      RecSource
}

// ParseUserFile reads `key = value` pairs from a user conf file.
func ParseUserFile(path string) (map[string]string, error) {
	vals := map[string]string{}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return vals, nil
		}
		return nil, err
	}
	re := regexp.MustCompile(`^([A-Za-z][A-Za-z0-9._]*)\s*=\s*(.*?)\s*$`)
	for _, line := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if m := re.FindStringSubmatch(line); m != nil {
			vals[m[1]] = strings.Trim(m[2], `"`)
		}
	}
	return vals, nil
}

// UserPath is the persistent user config file for a logical conf name.
func (m *Manager) UserPath(conf string) string {
	return filepath.Join(m.UserDir, conf)
}

// RunPath is the generated config handed to the server binary.
func (m *Manager) RunPath(conf string) string {
	return filepath.Join(m.RunDir, conf)
}

// Resolve computes the effective value for every key of a conf.
func (m *Manager) Resolve(conf string, dist *Schema) ([]Effective, error) {
	user, err := ParseUserFile(m.UserPath(conf))
	if err != nil {
		return nil, err
	}
	out := make([]Effective, 0, len(dist.Entries))
	for _, e := range dist.Entries {
		eff := Effective{
			Key:      e.Key,
			Conf:     conf,
			Upstream: e.Default,
			User:     "",
			Value:    e.Default,
		}
		if rec, ok := m.Rec.Recommended(conf, e.Key); ok {
			eff.Recommended = rec
			eff.Value = rec
		}
		if u, ok := user[e.Key]; ok {
			eff.User = u
			eff.Value = u
			eff.Overridden = u != eff.Upstream
		}
		out = append(out, eff)
	}
	return out, nil
}

// Generate writes the final conf: full document with every key resolved,
// so the raw editor shows a complete, greppable file.
func (m *Manager) Generate(conf string, dist *Schema) error {
	if err := os.MkdirAll(m.RunDir, 0o755); err != nil {
		return err
	}
	eff, err := m.Resolve(conf, dist)
	if err != nil {
		return err
	}
	bySec := map[string][]Effective{}
	var order []string
	for _, e := range eff {
		sec := sectionOf(dist, e.Key)
		if _, ok := bySec[sec]; !ok {
			order = append(order, sec)
		}
		bySec[sec] = append(bySec[sec], e)
	}
	var b strings.Builder
	b.WriteString("[//]: # (由 GSWXY Manager 生成 —— 请勿直接编辑本文件；编辑上层用户配置或使用 WebUI)\n")
	b.WriteString("[worldserver]\n")
	if conf == "authserver.conf" {
		b.WriteString("[authserver]\n")
	}
	for _, sec := range order {
		entries := bySec[sec]
		sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
		fmt.Fprintf(&b, "\n###############################################\n# %s\n###############################################\n", sec)
		for _, e := range entries {
			cmt := commentOf(dist, e.Key)
			if cmt != "" {
				for _, l := range strings.Split(cmt, "\n") {
					b.WriteString("# " + l + "\n")
				}
			}
			fmt.Fprintf(&b, "%s = %s\n", e.Key, quoteValue(e.Value))
		}
	}
	path := m.RunPath(conf)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func sectionOf(s *Schema, key string) string {
	if e, ok := s.Get(key); ok {
		return e.Section
	}
	return "General"
}

func commentOf(s *Schema, key string) string {
	if e, ok := s.Get(key); ok {
		return e.Comment
	}
	return ""
}

// quoteValue re-quotes strings that contain spaces (AC conf convention).
func quoteValue(v string) string {
	if v == "" {
		return `""`
	}
	if strings.ContainsAny(v, " \t") && !strings.HasPrefix(v, "\"") {
		return `"` + v + `"`
	}
	return v
}

// SetUser writes/updates one key in the user layer (atomic + audited path).
func (m *Manager) SetUser(conf, key, value string) error {
	path := m.UserPath(conf)
	lines := []string{}
	raw, _ := os.ReadFile(path)
	if len(raw) > 0 {
		lines = strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	}
	re := regexp.MustCompile(`^([A-Za-z][A-Za-z0-9._]*)\s*=`)
	found := false
	for i, l := range lines {
		if mm := re.FindStringSubmatch(l); mm != nil && mm[1] == key {
			lines[i] = fmt.Sprintf("%s = %s", key, quoteValue(value))
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, fmt.Sprintf("%s = %s", key, quoteValue(value)))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// DeleteUserKey removes a key from the user layer (falls back to default).
func (m *Manager) DeleteUserKey(conf, key string) error {
	path := m.UserPath(conf)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	re := regexp.MustCompile(`^([A-Za-z][A-Za-z0-9._]*)\s*=`)
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if mm := re.FindStringSubmatch(l); mm != nil && mm[1] == key {
			continue
		}
		lines = append(lines, l)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadUserRaw returns the full user conf file for the raw editor.
func (m *Manager) ReadUserRaw(conf string) (string, error) {
	raw, err := os.ReadFile(m.UserPath(conf))
	if os.IsNotExist(err) {
		return "", nil
	}
	return string(raw), err
}

// WriteUserRaw replaces the user conf file (raw editor save).
func (m *Manager) WriteUserRaw(conf, content string) error {
	if err := os.MkdirAll(m.UserDir, 0o755); err != nil {
		return err
	}
	tmp := m.UserPath(conf) + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.UserPath(conf))
}

// ResetUser removes the user conf file entirely (restore defaults).
func (m *Manager) ResetUser(conf string) error {
	err := os.Remove(m.UserPath(conf))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ModuleConfs returns the module .conf.dist names found in the payload.
func ModuleConfs(distDir string) []string {
	matches, _ := filepath.Glob(filepath.Join(distDir, "modules", "*.conf.dist"))
	out := make([]string, 0, len(matches))
	for _, mm := range matches {
		out = append(out, filepath.Base(mm))
	}
	sort.Strings(out)
	return out
}

// GenerateModule writes a module's generated conf next to its .dist
// (AC loads 'etc/modules/<name>.conf' relative to the binary's CWD —
// the standard stock-install layout).
func (m *Manager) GenerateModule(name string, dist *Schema) error {
	if err := os.MkdirAll(filepath.Join(m.DistDir, "modules"), 0o755); err != nil {
		return err
	}
	eff, err := m.Resolve(name, dist)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("[//]: # (由 GSWXY Manager 生成 —— 模块运行配置)\n")
	for _, e := range eff {
		cmt := ""
		if ce, ok := dist.Get(e.Key); ok {
			cmt = ce.Comment
		}
		if cmt != "" {
			for _, l := range strings.Split(cmt, "\n") {
				b.WriteString("# " + l + "\n")
			}
		}
		fmt.Fprintf(&b, "%s = %s\n", e.Key, quoteValue(e.Value))
	}
	path := filepath.Join(m.DistDir, "modules", name)
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
