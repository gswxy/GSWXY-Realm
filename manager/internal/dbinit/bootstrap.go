// Package dbinit bootstraps the bundled MariaDB runtime: datadir install,
// random credentials, database creation and the ordered SQL import pipeline.
package dbinit

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "github.com/go-sql-driver/mysql"

	"github.com/gswxy/gswxy-realm/manager/internal/logging"
	"github.com/gswxy/gswxy-realm/manager/internal/platform"
)

// Credentials are stored with mode 0600 in var/state/.
type Credentials struct {
	Root     string `json:"root"`
	App      string `json:"app"`
	Port     int    `json:"port"`
	Username string `json:"username"`
}

// Databases created for AzerothCore + Playerbots.
var Databases = []string{"acore_auth", "acore_characters", "acore_world", "acore_playerbots"}

const appUser = "acore"

func RandomPassword(n int) string {
	raw := make([]byte, n*3/4+1)
	_, _ = rand.Read(raw)
	s := base64.RawURLEncoding.EncodeToString(raw)
	return s[:n] + "aA1!" // satisfy any policy; still high entropy
}

// Bootstrap performs first-run database setup. Idempotent: a completed
// datadir is detected and skipped.
func Bootstrap(p platform.Paths, log *logging.Logger, st StateRecorder,
	mysqldBin, installDbBin string, progress func(step string)) (*Credentials, error) {

	if err := os.MkdirAll(p.MySQLData(), 0o750); err != nil {
		return nil, err
	}

	creds, err := LoadCreds(p)
	if err != nil {
		return nil, err
	}

	if !datadirInitialized(p.MySQLData()) {
		progress("installing datadir")
		if err := runInstallDB(p, installDbBin, mysqldBin); err != nil {
			return nil, fmt.Errorf("mariadb-install-db: %w", err)
		}
	}

	if creds == nil {
		// Fresh credentials + a random high port; persisted 0600.
		port, err := freePort()
		if err != nil {
			return nil, err
		}
		creds = &Credentials{
			Root:     RandomPassword(32),
			App:      RandomPassword(32),
			Port:     port,
			Username: appUser,
		}
		if err := saveCreds(p, creds); err != nil {
			return nil, err
		}
		log.Info("generated new database credentials (port %d)", port)
	}
	st.SetDBPort(creds.Port)
	return creds, nil
}

func datadirInitialized(dir string) bool {
	for _, marker := range []string{"mysql", "performance_schema", "aria_log_control"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}

func runInstallDB(p platform.Paths, installDbBin, mysqldBin string) error {
	args := []string{
		"--defaults-file=" + myCnfPath(p),
		"--datadir=" + p.MySQLData(),
		"--basedir=" + p.MySQLRuntime(),
		"--auth-root-authentication-method=normal",
	}
	cmd := command(installDbBin, args)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// myCnfPath is where the Manager writes the generated my.cnf.
func myCnfPath(p platform.Paths) string { return filepath.Join(p.State(), "my.cnf") }

// MyCnfPath is exported for the supervisor launcher.
func MyCnfPath(p platform.Paths) string { return myCnfPath(p) }

// LoadCreds reads existing credentials, if any.
func LoadCreds(p platform.Paths) (*Credentials, error) {
	raw, err := os.ReadFile(p.DBCreds())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	c := &Credentials{}
	if err := json.Unmarshal(raw, c); err != nil {
		return nil, err
	}
	return c, nil
}

func saveCreds(p platform.Paths, c *Credentials) error {
	if err := os.MkdirAll(p.State(), 0o700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(c, "", "  ")
	// 0600: database secrets must not be group/world readable.
	return os.WriteFile(p.DBCreds(), raw, 0o600)
}

// ProvisionAccounts creates the databases and the least-privilege app user.
func ProvisionAccounts(p platform.Paths, creds *Credentials, log *logging.Logger) error {
	dsn := fmt.Sprintf("root:%s@tcp(127.0.0.1:%d)/?charset=utf8mb4&multiStatements=true", creds.Root, creds.Port)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	for _, name := range Databases {
		q := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", name)
		if _, err := db.Exec(q); err != nil {
			return fmt.Errorf("create %s: %w", name, err)
		}
	}
	// App user: localhost only, least privilege on the AC databases.
	stmts := []string{
		fmt.Sprintf("CREATE USER IF NOT EXISTS `%s`@`localhost` IDENTIFIED BY '%s'", appUser, creds.App),
		fmt.Sprintf("CREATE USER IF NOT EXISTS `%s`@`127.0.0.1` IDENTIFIED BY '%s'", appUser, creds.App),
	}
	for _, name := range Databases {
		stmts = append(stmts,
			fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO `%s`@`localhost`", name, appUser),
			fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO `%s`@`127.0.0.1`", name, appUser))
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("provision: %w", err)
		}
	}
	if _, err := db.Exec("FLUSH PRIVILEGES"); err != nil {
		return err
	}
	log.Info("databases and app user provisioned")
	return nil
}

// SQLFile is one file in the import pipeline.
type SQLFile struct {
	Path     string
	Database string
	Label    string
}

// BuildPipeline assembles the ordered SQL import plan:
// base -> core updates -> playerbots module SQL -> GSWXY locale.
func BuildPipeline(p platform.Paths) []SQLFile {
	var files []SQLFile
	add := func(dir, db, label string) {
		if fs, err := walkSQL(dir); err == nil {
			for _, f := range fs {
				files = append(files, SQLFile{Path: f, Database: db, Label: label})
			}
		}
	}
	base := p.SQLDir()
	add(filepath.Join(base, "base", "db_auth"), "acore_auth", "core/base")
	add(filepath.Join(base, "base", "db_characters"), "acore_characters", "core/base")
	add(filepath.Join(base, "base", "db_world"), "acore_world", "core/base")
	add(filepath.Join(base, "updates", "db_auth"), "acore_auth", "core/updates")
	add(filepath.Join(base, "updates", "db_characters"), "acore_characters", "core/updates")
	add(filepath.Join(base, "updates", "db_world"), "acore_world", "core/updates")

	mod := filepath.Join(p.AppDest, "modules", "mod-playerbots", "data", "sql")
	add(filepath.Join(mod, "playerbots"), "acore_playerbots", "playerbots")
	add(filepath.Join(mod, "characters"), "acore_characters", "playerbots")
	add(filepath.Join(mod, "world"), "acore_world", "playerbots")

	add(p.LocaleDir(), "*", "locale/zhCN")
	return files
}

func walkSQL(dir string) ([]string, error) {
	var out []string
	walkErr := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode().IsRegular() && strings.HasSuffix(info.Name(), ".sql") {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out, walkErr
}

func freePort() (int, error) {
	l, err := netListen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	tcp, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("no tcp addr")
	}
	return tcp.Port, nil
}

// StateRecorder avoids an import cycle with package state.
type StateRecorder interface {
	SetDBPort(port int) error
}
