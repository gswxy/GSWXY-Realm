// Package platform resolves runtime paths and identity for fnOS native apps.
//
// Under fnOS the package lifecycle provides TRIM_* environment variables:
//
//	TRIM_APPDEST  payload directory (upgradable, == /var/apps/<app>/target)
//	TRIM_PKGVAR   persistent data   (survives upgrade/uninstall)
//	TRIM_PKGETC   persistent config
//	TRIM_PKGHOME  app home
//	TRIM_PKGTMP   temp
//	TRIM_PKGMETA  metadata
//	TRIM_APPNAME  package id
//
// In developer mode (no fnOS env) everything is rooted at GSRM_HOME.
package platform

import (
	"os"
	"path/filepath"
	"strings"
)

// Paths is the resolved directory layout of one installation.
type Paths struct {
	AppName string
	AppDest string // payload (binaries, mysql runtime, sql, web) - replaced on upgrade
	Var     string // persistent user data (mysql datadir, client-data, backups, logs, state)
	Etc     string // persistent config
	Home    string
	Tmp     string
	Meta    string

	OnFnOS bool
}

// Detect resolves paths from fnOS environment or developer fallback.
func Detect() Paths {
	appdest := os.Getenv("TRIM_APPDEST")
	appname := os.Getenv("TRIM_APPNAME")
	if appname == "" {
		appname = "com.gswxy.realm"
	}
	if appdest == "" {
		root := os.Getenv("GSRM_HOME")
		if root == "" {
			root = "./gsrealm-dev"
		}
		return Paths{
			AppName: appname,
			AppDest: filepath.Join(root, "target"),
			Var:     filepath.Join(root, "var"),
			Etc:     filepath.Join(root, "etc"),
			Home:    filepath.Join(root, "home"),
			Tmp:     filepath.Join(root, "tmp"),
			Meta:    filepath.Join(root, "meta"),
		}
	}
	// fnOS: derive the sibling roots from TRIM_APPDEST's volume.
	// TRIM_APPDEST points at the target payload; TRIM_PKGVAR etc. are
	// provided directly by the lifecycle when scripts run, but the long
	// running daemon may have been started through a different shell, so
	// resolve robustly: prefer the official vars, fall back to layout.
	v := os.Getenv("TRIM_PKGVAR")
	if v == "" {
		v = guessSibling(appdest, "@appdata")
	}
	e := os.Getenv("TRIM_PKGETC")
	if e == "" {
		e = guessSibling(appdest, "@appconf")
	}
	h := os.Getenv("TRIM_PKGHOME")
	if h == "" {
		h = guessSibling(appdest, "@apphome")
	}
	t := os.Getenv("TRIM_PKGTMP")
	if t == "" {
		t = guessSibling(appdest, "@apptemp")
	}
	m := os.Getenv("TRIM_PKGMETA")
	if m == "" {
		m = guessSibling(appdest, "@appmeta")
	}
	return Paths{
		AppName: appname,
		AppDest: appdest,
		Var:     v,
		Etc:     e,
		Home:    h,
		Tmp:     t,
		Meta:    m,
		OnFnOS:  true,
	}
}

// guessSibling mirrors the fnOS directory mapping for the payload:
//   /vol1/@appcenter/<app>  ->  /vol1/@appdata/<app> (etc.)
func guessSibling(appdest, group string) string {
	dir := filepath.Dir(appdest)   // /vol1/@appcenter
	vol := filepath.Dir(dir)       // /vol1
	return filepath.Join(vol, group, filepath.Base(appdest))
}

// Subdirectory helpers (created on demand by EnsureDirs).

func (p Paths) MySQLData() string    { return filepath.Join(p.Var, "mysql") }
func (p Paths) ClientData() string   { return filepath.Join(p.Var, "client-data") }
func (p Paths) Downloads() string    { return filepath.Join(p.Var, "downloads") }
func (p Paths) Backups() string      { return filepath.Join(p.Var, "backups") }
func (p Paths) Logs() string         { return filepath.Join(p.Var, "logs") }
func (p Paths) State() string        { return filepath.Join(p.Var, "state") }
func (p Paths) RunConfig() string    { return filepath.Join(p.Var, "config", "run") }
func (p Paths) UserConfig() string   { return filepath.Join(p.Var, "config") }
func (p Paths) BinDir() string       { return filepath.Join(p.AppDest, "bin") }
func (p Paths) MySQLRuntime() string { return filepath.Join(p.AppDest, "mysql") }
func (p Paths) SQLDir() string       { return filepath.Join(p.AppDest, "data", "sql") }
func (p Paths) LocaleDir() string    { return filepath.Join(p.AppDest, "locale") }
func (p Paths) EtcDist() string      { return filepath.Join(p.AppDest, "etc") }
func (p Paths) DataDir() string      { return filepath.Join(p.AppDest, "data") }
func (p Paths) WebDir() string       { return filepath.Join(p.AppDest, "web") }

// StateFile / credentials / build info locations.
func (p Paths) StateFile() string     { return filepath.Join(p.State(), "state.json") }
func (p Paths) DBCreds() string       { return filepath.Join(p.State(), "db-credentials.json") }
func (p Paths) BuildInfo() string     { return filepath.Join(p.AppDest, "build-info.json") }
func (p Paths) ResourcesJSON() string { return filepath.Join(p.AppDest, "resources.json") }
func (p Paths) ManagerLog() string    { return filepath.Join(p.Logs(), "gswxy-manager.log") }
func (p Paths) AuditLog() string      { return filepath.Join(p.Logs(), "audit.log") }

// EnsureDirs creates the whole persistent directory tree with safe modes.
func (p Paths) EnsureDirs() error {
	for _, d := range []string{
		p.MySQLData(), p.ClientData(), p.Downloads(), p.Backups(),
		p.Logs(), p.State(), p.RunConfig(), p.UserConfig(),
		filepath.Join(p.UserConfig(), "modules"),
		filepath.Join(p.UserConfig(), "run", "modules"),
		filepath.Join(p.Downloads(), "manual"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// IsUnder reports whether child resolves within root (path traversal guard).
func IsUnder(root, child string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absChild, err := filepath.Abs(child)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absChild)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
