// SQL import runner: executes one SQL file through the bundled client,
// tracking completion per-file (resumable) and checksum verification.
package dbinit

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gswxy/gswxy-realm/manager/internal/logging"
	"github.com/gswxy/gswxy-realm/manager/internal/platform"
)

// PatchApplier records applied patches in the state store.
type PatchApplier interface {
	IsApplied(label string) bool
	MarkApplied(label string, checksum string) error
	// IsUpdateApplied reports whether <db>.updates already contains name.
	IsUpdateApplied(db, name string) bool
	// RecordUpdate inserts name into <db>.updates after manual import.
	RecordUpdate(db, name, sha1hex string) error
}

// sha1File computes the SHA-1 of a file (the hash AC records in updates).
func sha1File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(h.Sum(nil))), nil
}

// ChecksumFile computes the SHA-256 of a file.
func ChecksumFile(path string) (string, error) {
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

// ImportFile executes one .sql against db via the bundled mysql client.
// The client is used because upstream dumps rely on server-side features
// that are awkward through prepared statements; each file is one unit of
// progress. Locale files (pure DML) additionally run inside a transaction
// with rollback on failure.
func ImportFile(p platform.Paths, mysqlBin, rootPass, dbName, path string,
	log *logging.Logger) error {

	args := []string{
		"--defaults-extra-file=" + defaultsFile(p, rootPass),
		"--default-character-set=utf8mb4",
		"--max-allowed-packet=1G",
	}
	if dbName != "*" && dbName != "" {
		args = append(args, dbName)
	}
	if strings.Contains(path, string(os.PathSeparator)+"locale"+string(os.PathSeparator)) ||
		strings.Contains(path, "/locale/") {
		args = append(args, "--init-command=SET autocommit=0")
	}
	cmd := exec.Command(mysqlBin, args...)
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	cmd.Stdin = in
	// Never inherit the Manager's environment wholesale.
	cmd.Env = minimalEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Locale/DML import must leave the database untouched.
		if isTransactionalImport(path) {
			_ = exec.Command(mysqlBin,
				"--defaults-extra-file="+defaultsFile(p, rootPass),
				"-e", "ROLLBACK").Run()
		}
		return fmt.Errorf("%s: %s", err, tailLines(string(out), 12))
	}
	return nil
}

func isTransactionalImport(path string) bool {
	return strings.Contains(path, "/locale/") ||
		strings.Contains(path, string(os.PathSeparator)+"locale"+string(os.PathSeparator))
}

// ImportAll runs the pipeline, skipping already-applied patches.
func ImportAll(p platform.Paths, mysqlBin, rootPass string, files []SQLFile,
	applier PatchApplier, log *logging.Logger, progress func(done, total int, label string)) error {

	total := len(files)
	for i, f := range files {
		label := f.Label + ":" + f.Path
		if applier.IsApplied(label) {
			if progress != nil {
				progress(i, total, label+" (已应用，跳过)")
			}
			continue
		}
		base := filepath.Base(f.Path)
		if f.CheckUpdates && applier.IsUpdateApplied(f.Database, base) {
			// 已并入基础快照（ARCHIVED）——跳过并记录，保证幂等续跑。
			_ = applier.MarkApplied(label, "archived")
			if progress != nil {
				progress(i, total, label+" (已并入基础库，跳过)")
			}
			continue
		}
		if progress != nil {
			progress(i, total, label)
		}
		log.Info("importing %s -> %s", f.Path, f.Database)
		sum, err := ChecksumFile(f.Path)
		if err != nil {
			return err
		}
		if err := ImportFile(p, mysqlBin, rootPass, f.Database, f.Path, log); err != nil {
			return fmt.Errorf("import %s: %w", f.Path, err)
		}
		if f.CheckUpdates {
			// 记录到 updates 表，避免 worldserver 自动更新器重复应用。
			sum1, _ := sha1File(f.Path)
			_ = applier.RecordUpdate(f.Database, base, sum1)
		}
		if err := applier.MarkApplied(label, sum); err != nil {
			return err
		}
	}
	if progress != nil {
		progress(total, total, "完成")
	}
	return nil
}

// defaultsFile writes a per-invocation client credentials file (0600,
// avoids argv leaks) pinning the connection to the bundled server via
// TCP 127.0.0.1:<random port>; without it the client tries the system
// socket path /run/mysqld/mysqld.sock.
func defaultsFile(p platform.Paths, rootPass string) string {
	port := 0
	if creds, err := LoadCreds(p); err == nil && creds != nil {
		port = creds.Port
	}
	path := filepath.Join(p.Tmp, "mysql-import-auth.cnf")
	content := fmt.Sprintf("[client]\nhost=127.0.0.1\nport=%d\nuser=root\npassword=%s\n", port, rootPass)
	_ = os.WriteFile(path, []byte(content), 0o600)
	return path
}

func minimalEnv() []string {
	return []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
	}
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
