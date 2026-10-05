package app

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gswxy/gswxy-realm/manager/internal/dbinit"
)

// EnsureDataLinks links target/data/<dir> -> var/client-data/<dir> so the
// upgradable payload keeps one persistent copy of the multi-GB client data.
func (a *App) EnsureDataLinks() {
	_ = os.MkdirAll(a.Paths.DataDir(), 0o755)
	for _, d := range []string{"dbc", "maps", "vmaps", "mmaps", "Cameras"} {
		link := filepath.Join(a.Paths.DataDir(), d)
		target := filepath.Join(a.Paths.ClientData(), d)
		if _, err := os.Stat(target); err != nil {
			continue // data not downloaded yet
		}
		if cur, err := os.Readlink(link); err == nil && cur == target {
			continue
		}
		_ = os.RemoveAll(link)
		_ = os.Symlink(target, link)
	}
}

// reDBInfo matches AC connection info lines: Key = host;port;user;pass;db
var reDBInfo = regexp.MustCompile(`^([A-Za-z]*DatabaseInfo)\s*=\s*(.*)$`)

// dbLine builds one connection string. Port 0 means "default" — we always
// write the real random port.
func (a *App) dbLine(host string, user, pass, db string) string {
	creds, err := dbinit.LoadCreds(a.Paths)
	if err != nil || creds == nil {
		return fmt.Sprintf("%s;0;%s;%s;%s", host, user, "PASSWORD_NOT_READY", db)
	}
	return fmt.Sprintf("%s;%d;%s;%s;%s", host, creds.Port, user, pass, db)
}

// injectCredentials rewrites the generated run configs with the real
// database connection strings (never stored in the repo or UI).
func (a *App) injectCredentials() error {
	replacements := map[string]string{
		"LoginDatabaseInfo":      a.dbLine("127.0.0.1", "acore", "PLACEHOLDER", "acore_auth"),
		"WorldDatabaseInfo":      a.dbLine("127.0.0.1", "acore", "PLACEHOLDER", "acore_world"),
		"CharacterDatabaseInfo":  a.dbLine("127.0.0.1", "acore", "PLACEHOLDER", "acore_characters"),
		"PlayerbotsDatabaseInfo": a.dbLine("127.0.0.1", "acore", "PLACEHOLDER", "acore_playerbots"),
	}
	creds, err := dbinit.LoadCreds(a.Paths)
	if err != nil || creds == nil {
		return fmt.Errorf("数据库凭据缺失")
	}
	for conf := range map[string]bool{"worldserver.conf": true, "authserver.conf": true} {
		path := a.CM.RunPath(conf)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue // module confs have no DB info
		}
		lines := strings.Split(string(raw), "\n")
		for i, line := range lines {
			if m := reDBInfo.FindStringSubmatch(line); m != nil {
				if rep, ok := replacements[m[1]]; ok {
					lines[i] = fmt.Sprintf("%s = %s", m[1], strings.ReplaceAll(rep, "PLACEHOLDER", creds.App))
				}
			}
		}
		_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600)
	}
	return nil
}

// regenerateFull generates configs then injects credentials + DataDir.
func (a *App) regenerateFull() error {
	if err := a.regenerateConfigs(); err != nil {
		return err
	}
	if err := a.injectCredentials(); err != nil {
		return err
	}
	// Point DataDir at the payload data dir (symlinks to client-data).
	worldPath := a.CM.RunPath("worldserver.conf")
	raw, err := os.ReadFile(worldPath)
	if err == nil {
		re := regexp.MustCompile(`(?m)^DataDir\s*=\s*(.*)$`)
		raw = re.ReplaceAll(raw, []byte("DataDir = \""+a.Paths.DataDir()+"\""))
		_ = os.WriteFile(worldPath, raw, 0o600)
	}
	a.EnsureDataLinks()
	return nil
}

// RegenerateAll is the API entry (also used by setup).
func (a *App) RegenerateAll() error { return a.regenerateFull() }
