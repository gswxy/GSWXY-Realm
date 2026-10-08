package app

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gswxy/gswxy-realm/manager/internal/confman"
	"github.com/gswxy/gswxy-realm/manager/internal/dbinit"
)

// DistFor exposes the .conf.dist path for a logical conf name (API layer).
func (a *App) DistFor(conf string) string { return a.distFor(conf) }

// LoadDBCreds exposes stored credentials to trusted internal callers only.
func (a *App) LoadDBCreds() (*dbinit.Credentials, error) {
	c, err := dbinit.LoadCreds(a.Paths)
	if err != nil || c == nil {
		return nil, fmt.Errorf("数据库尚未初始化")
	}
	return c, nil
}

// LogFiles enumerates viewable logs (Manager + children + AC appenders).
func (a *App) LogFiles() []map[string]string {
	var out []map[string]string
	add := func(name, path string) {
		if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() {
			out = append(out, map[string]string{
				"name": name, "path": path,
				"size": fmt.Sprintf("%d", st.Size()),
			})
		}
	}
	add("GSWXY Manager", a.Paths.ManagerLog())
	add("审计日志", a.Paths.AuditLog())
	dir := os.Getenv("GSRM_PROC_LOG_DIR")
	if dir == "" {
		dir = "/tmp/gsrealm-procs"
	}
	for _, n := range []string{"worldserver", "authserver", "mysqld"} {
		add(n, filepath.Join(dir, n+".log"))
	}
	extra, _ := filepath.Glob(filepath.Join(a.Paths.Var, "logs", "*.log"))
	for _, p := range extra {
		base := filepath.Base(p)
		if base != "gswxy-manager.log" && base != "audit.log" {
			add(base, p)
		}
	}
	return out
}

// TailLog returns the last n lines of a named log (whitelist + traversal
// safe), optionally filtered by substring.
func (a *App) TailLog(name string, n int, filter string) ([]string, error) {
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return nil, fmt.Errorf("非法日志名")
	}
	path := ""
	for _, f := range a.LogFiles() {
		if f["name"] == name {
			path = f["path"]
			break
		}
	}
	if path == "" {
		return nil, fmt.Errorf("日志不存在")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	ring := make([]string, n)
	i := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if filter != "" && !strings.Contains(line, filter) {
			continue
		}
		ring[i%n] = line
		i++
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, n)
	if i <= n {
		out = append(out, ring[:i]...)
	} else {
		out = append(out, ring[i%n:]...)
		out = append(out, ring[:i%n]...)
	}
	return out, nil
}

// ---- Playerbot profiles ----

//go:embed botprofiles.json
var botProfilesRaw []byte

type botProfile struct {
	Name        string            `json:"name"`
	Label       string            `json:"label"`
	Description string            `json:"description"`
	Values      map[string]string `json:"values"`
}

// BotProfiles lists the built-in profiles with current diff vs user layer.
func (a *App) BotProfiles() []map[string]any {
	var profiles []botProfile
	_ = json.Unmarshal(botProfilesRaw, &profiles)

	user, _ := confman.ParseUserFile(filepath.Join(a.Paths.UserConfig(), "playerbots.conf"))
	out := []map[string]any{}
	for _, p := range profiles {
		changes := map[string][3]string{} // key -> [upstream, user, profile]
		for k, v := range p.Values {
			entry := [3]string{"", user[k], v}
			if e, ok := a.schemaDefault("playerbots.conf", k); ok {
				entry[0] = e
			}
			changes[k] = entry
		}
		out = append(out, map[string]any{
			"name": p.Name, "label": p.Label, "description": p.Description,
			"changes": changes,
		})
	}
	return out
}

// ApplyBotProfile writes the profile values into the user layer and
// regenerates run configs. Returns the applied diff; a failed regeneration
// is an error (never "applied successfully").
func (a *App) ApplyBotProfile(name string) (map[string]string, error) {
	var profiles []botProfile
	_ = json.Unmarshal(botProfilesRaw, &profiles)
	var chosen *botProfile
	for i := range profiles {
		if profiles[i].Name == name {
			chosen = &profiles[i]
			break
		}
	}
	if chosen == nil {
		return nil, fmt.Errorf("未知的配置模板: %s", name)
	}
	diff := map[string]string{}
	userPath := filepath.Join(a.Paths.UserConfig(), "playerbots.conf")
	userVals, _ := confman.ParseUserFile(userPath)
	for k, v := range chosen.Values {
		if cur, ok := userVals[k]; ok && cur != v {
			diff[k] = v
		} else if !ok {
			if def, ok2 := a.schemaDefault("playerbots.conf", k); ok2 && def != v {
				diff[k] = v
			}
		}
		_ = a.CM.SetUser("playerbots.conf", k, v)
	}
	if err := a.RegenerateAll(); err != nil {
		return nil, fmt.Errorf("配置已写入但生成运行配置失败: %w", err)
	}
	a.Log.Audit("system", "playerbot.profile", chosen.Name)
	return diff, nil
}

// SetupRunning reports whether a setup goroutine is live in this process.
func (a *App) SetupRunning() bool { return a.setupRunning.Load() }

// schemaDefault returns (default, found) for one key.
func (a *App) schemaDefault(conf, key string) (string, bool) {
	s, err := a.ConfigSchemaCached(conf)
	if err != nil {
		return "", false
	}
	if e, ok := s.Get(key); ok {
		return e.Default, true
	}
	return "", false
}

// ConfigSchemaCached caches dist schemas per conf name.
func (a *App) ConfigSchemaCached(conf string) (*confman.Schema, error) {
	a.schemaMu.Lock()
	defer a.schemaMu.Unlock()
	if a.schemas == nil {
		a.schemas = map[string]*confman.Schema{}
	}
	if s, ok := a.schemas[conf]; ok {
		return s, nil
	}
	s, err := confman.ParseDist(a.distFor(conf))
	if err != nil {
		return nil, err
	}
	a.schemas[conf] = s
	return s, nil
}

// ConfigSchemas returns cached schemas for all confs (API helper).
func (a *App) ConfigSchemas() map[string]*confman.Schema {
	out := map[string]*confman.Schema{}
	for _, c := range confman.ConfFiles {
		if s, err := a.ConfigSchemaCached(c); err == nil {
			out[c] = s
		}
	}
	return out
}
