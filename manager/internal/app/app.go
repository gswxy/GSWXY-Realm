// Package app composes every Manager subsystem into one orchestrator:
// setup flow, process supervision, database bootstrap, client data and
// configuration management.
package app

import (
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/gswxy/gswxy-realm/manager/internal/accounts"
	"github.com/gswxy/gswxy-realm/manager/internal/admin"
	"github.com/gswxy/gswxy-realm/manager/internal/backup"
	"github.com/gswxy/gswxy-realm/manager/internal/clientdata"
	"github.com/gswxy/gswxy-realm/manager/internal/confman"
	"github.com/gswxy/gswxy-realm/manager/internal/dbinit"
	"github.com/gswxy/gswxy-realm/manager/internal/logging"
	"github.com/gswxy/gswxy-realm/manager/internal/platform"
	"github.com/gswxy/gswxy-realm/manager/internal/proc"
	"github.com/gswxy/gswxy-realm/manager/internal/recommend"
	"github.com/gswxy/gswxy-realm/manager/internal/state"
	"github.com/gswxy/gswxy-realm/manager/internal/version"
)

// Binaries resolves bundled executable paths.
type Binaries struct {
	Worldserver  string
	Authserver   string
	MariaDBD     string
	InstallDB    string
	MysqlClient  string
	MysqlDump    string
	MysqlAdmin   string
}

// App is the root orchestrator.
type App struct {
	Paths platform.Paths
	Log   *logging.Logger
	State *state.Store
	Admin *admin.Store
	Sup   *proc.Supervisor
	CD    *clientdata.Manager
	CM    *confman.Manager
	BK    *backup.Manager
	Acc   *accounts.Manager
	Ver   version.Info
	Bins  Binaries

	schemaMu sync.Mutex
	schemas  map[string]*confman.Schema

	dbMu sync.Mutex
	db   *sql.DB

	startMu sync.Mutex
}

// New builds the App from resolved paths.
func New(p platform.Paths) (*App, error) {
	log, err := logging.New(p.ManagerLog())
	if err != nil {
		return nil, err
	}
	if err := p.EnsureDirs(); err != nil {
		return nil, err
	}
	st, err := state.Open(p.StateFile())
	if err != nil {
		return nil, err
	}
	ad, err := admin.Open(p.State())
	if err != nil {
		return nil, err
	}
	a := &App{
		Paths: p,
		Log:   log,
		State: st,
		Admin: ad,
		Sup:   proc.NewSupervisor(),
		CD:    clientdata.NewManager(p, log),
		Ver:   version.Load(p.BuildInfo()),
	}
	a.CM = &confman.Manager{
		UserDir: p.UserConfig(),
		RunDir:  p.RunConfig(),
		DistDir: p.EtcDist(),
		Rec:     recommend.New(),
	}
	a.BK = &backup.Manager{Paths: p, Log: log, Version: a.versionInfo()}
	// Backup 工具链通过该钩子拿到内置数据库的 TCP 端口与凭据。
	backup.CredsHook = func() (user, pass string, port int) {
		c, err := dbinit.LoadCreds(p)
		if err != nil || c == nil {
			return "", "", 0
		}
		return "root", c.Root, c.Port
	}
	a.Bins = a.resolveBins()
	a.registerProcs()
	return a, nil
}

func (a *App) versionInfo() backup.VersionInfo {
	d := a.State.Get()
	return backup.VersionInfo{
		GSWXY:            a.Ver.Version,
		CoreCommit:       a.Ver.Core.Commit,
		PlayerbotsCommit: a.Ver.Playerbots.Commit,
		DataVersion:      d.ClientData.Version,
		LocaleVersion:    d.Locale.Version,
	}
}

func (a *App) resolveBins() Binaries {
	b := Binaries{
		Worldserver: filepath.Join(a.Paths.BinDir(), "worldserver"),
		Authserver:  filepath.Join(a.Paths.BinDir(), "authserver"),
		MysqlClient: filepath.Join(a.Paths.MySQLRuntime(), "bin", "mariadb"),
		MysqlDump:   filepath.Join(a.Paths.MySQLRuntime(), "bin", "mariadb-dump"),
		MysqlAdmin:  filepath.Join(a.Paths.MySQLRuntime(), "bin", "mariadb-admin"),
	}
	// MariaDB bintar layout keeps the daemon as mariadbd (mysqld alias).
	for _, name := range []string{"mariadbd", "mysqld"} {
		c := filepath.Join(a.Paths.MySQLRuntime(), "bin", name)
		if _, err := os.Stat(c); err == nil {
			b.MariaDBD = c
			break
		}
	}
	for _, name := range []string{"mariadb-install-db", "mysql_install_db"} {
		c := filepath.Join(a.Paths.MySQLRuntime(), "bin", name)
		if _, err := os.Stat(c); err == nil {
			b.InstallDB = c
			break
		}
	}
	// Fall back to bare names when the payload layout differs.
	if b.MariaDBD == "" {
		b.MariaDBD = "mariadbd"
	}
	if b.InstallDB == "" {
		b.InstallDB = "mariadb-install-db"
	}
	return b
}

// registerProcs wires the three managed processes.
func (a *App) registerProcs() {
	creds, _ := dbinit.LoadCreds(a.Paths)

	a.registerMysqld()
	if creds != nil && creds.Port > 0 {
		a.registerServers()
	}
}

// registerMysqld (re-)registers the mariadbd spec. Called again after
// credentials are generated so the spec carries the real port.
func (a *App) registerMysqld() {
	st := a.State.Get()
	mysqldArgs := []string{
		"--defaults-file=" + dbinit.MyCnfPath(a.Paths),
		"--datadir=" + a.Paths.MySQLData(),
		"--basedir=" + a.Paths.MySQLRuntime(),
		"--bind-address=127.0.0.1",
		"--socket=" + a.socketPath(),
		"--port=" + strconv.Itoa(st.Database.Port),
		"--skip-name-resolve",
	}
	a.Sup.Register(proc.Spec{
		Name: "mysqld", Bin: a.Bins.MariaDBD, Args: mysqldArgs,
		Dir: a.Paths.MySQLRuntime(), PidFile: filepath.Join(a.Paths.Var, "mysqld.pid"),
		StopGrace: 60 * time.Second,
	})
}

// registerServers registers authserver/worldserver (DB credentials known).
func (a *App) registerServers() {
	worldArgs := []string{"-c", a.CM.RunPath("worldserver.conf")}
	a.Sup.Register(proc.Spec{
		Name: "worldserver", Bin: a.Bins.Worldserver, Args: worldArgs,
		Dir: a.Paths.BinDir(), PidFile: filepath.Join(a.Paths.Var, "worldserver.pid"),
		Stdin: true, StopGrace: 45 * time.Second,
		Env: a.serverEnv(),
	})
	authArgs := []string{"-c", a.CM.RunPath("authserver.conf")}
	a.Sup.Register(proc.Spec{
		Name: "authserver", Bin: a.Bins.Authserver, Args: authArgs,
		Dir: a.Paths.BinDir(), PidFile: filepath.Join(a.Paths.Var, "authserver.pid"),
		StopGrace: 30 * time.Second,
		Env:       a.serverEnv(),
	})
}

func (a *App) socketPath() string {
	return filepath.Join(a.Paths.Var, "mysql.sock")
}

// serverEnv passes DB credentials to the servers through their generated
// config files, not the environment (env vars can leak into logs).
func (a *App) serverEnv() []string {
	return []string{"GSRM_DATA_DIR=" + a.Paths.DataDir()}
}

// DB lazily opens the app-user connection.
func (a *App) DB() (*sql.DB, error) {
	a.dbMu.Lock()
	defer a.dbMu.Unlock()
	if a.db != nil {
		return a.db, nil
	}
	creds, err := dbinit.LoadCreds(a.Paths)
	if err != nil || creds == nil {
		return nil, fmt.Errorf("数据库尚未初始化")
	}
	dsn := fmt.Sprintf("%s:%s@tcp(127.0.0.1:%d)/?charset=utf8mb4&parseTime=false",
		creds.Username, creds.App, creds.Port)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	a.db = db
	return db, nil
}

// RecreateDB closes the cached connection (after restore).
func (a *App) RecreateDB() {
	a.dbMu.Lock()
	if a.db != nil {
		_ = a.db.Close()
		a.db = nil
	}
	a.dbMu.Unlock()
}

// StopAll stops every managed process in dependency order.
func (a *App) StopAll() {
	// world/auth first, then the database.
	for _, name := range []string{"worldserver", "authserver"} {
		if a.Sup.Running(name) {
			_ = a.Sup.Stop(name)
		}
	}
	if a.Sup.Running("mysqld") {
		// graceful DB shutdown through mariadb-admin (never SIGKILL first).
		creds, err := dbinit.LoadCreds(a.Paths)
		if err == nil {
			ctx := exec.Command(a.Bins.MysqlAdmin,
				"--socket="+a.socketPath(), "-u", "root",
				"--password="+creds.Root, "shutdown")
			if out, err := ctx.CombinedOutput(); err != nil {
				a.Log.Warn("mariadb-admin shutdown: %v: %s", err, strings.TrimSpace(string(out)))
			}
			deadline := time.Now().Add(60 * time.Second)
			for a.Sup.Running("mysqld") && time.Now().Before(deadline) {
				time.Sleep(500 * time.Millisecond)
			}
		}
	}
	a.Sup.StopAll()
}

// ---- setup flow ----

// PreflightCheck is one startup readiness probe.
type PreflightCheck struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Detail  string `json:"detail"`
	FixHint string `json:"fix_hint,omitempty"`
}

// Preflight runs the checklist shown before starting services.
func (a *App) Preflight() []PreflightCheck {
	var out []PreflightCheck
	add := func(name string, ok bool, detail, hint string) {
		out = append(out, PreflightCheck{Name: name, Passed: ok, Detail: detail, FixHint: hint})
	}

	// architecture
	if a.Paths.OnFnOS || runtimeIsLinux() {
		add("系统架构 x86_64", runtimeArch() == "x86_64", runtimeArch(), "GSWXY Realm 仅支持 x86_64")
	}

	// payload integrity
	for _, b := range []string{a.Bins.Worldserver, a.Bins.Authserver} {
		_, err := os.Stat(b)
		add("服务端程序 "+filepath.Base(b), err == nil, b, "应用安装不完整，请重新安装 FPK")
	}

	// disk space
	if st, err := statFS(a.Paths.Var); err == nil {
		add("磁盘空间", st.availMB > 8192,
			fmt.Sprintf("剩余 %d MB", st.availMB),
			"请清理 NAS 存储空间（客户端数据解压约需 4 GB）")
	}

	// database runtime
	_, err := os.Stat(a.Bins.MariaDBD)
	add("内置数据库运行时", err == nil, a.Bins.MariaDBD, "应用安装不完整，请重新安装 FPK")

	// client data
	d := a.State.Get()
	missing := []string{}
	if d.ClientData.Installed {
		res, err := a.CD.LoadResource()
		if err == nil {
			missing = a.CD.RequiredDirs(res)
		}
		add("客户端数据", len(missing) == 0,
			"版本 "+d.ClientData.Version,
			"请在「数据」页面重新下载或导入")
	} else {
		add("客户端数据", false, "尚未下载", "请在「数据」页面下载或手动导入 Data.zip")
	}

	// config presence
	for _, conf := range confman.ConfFiles {
		_, err := os.Stat(a.CM.RunPath(conf))
		add("配置文件 "+conf, err == nil, a.CM.RunPath(conf),
			"点击「配置 → 重新生成运行配置」")
	}

	// ports
	for _, port := range []int{3724, 8085} {
		add(fmt.Sprintf("端口 %d 可用", port), !portBusy(port),
			fmt.Sprintf("端口 %d", port), "其他程序占用了端口，请修改或停止占用的程序")
	}
	return out
}

// ReadyToStart reports whether preflight passed.
func (a *App) ReadyToStart() (bool, string) {
	for _, c := range a.Preflight() {
		if !c.Passed {
			return false, c.Name + ": " + c.Detail
		}
	}
	return true, ""
}

// StartAll brings mysqld → servers up after checks.
func (a *App) StartAll() error {
	a.startMu.Lock()
	defer a.startMu.Unlock()
	if ok, why := a.ReadyToStart(); !ok {
		return fmt.Errorf("自检未通过: %s", why)
	}
	if err := a.Sup.Start("mysqld"); err != nil {
		return err
	}
	// wait for DB to accept connections.
	if err := waitTCP("127.0.0.1", a.State.Get().Database.Port, 90*time.Second); err != nil {
		return fmt.Errorf("数据库启动超时: %w", err)
	}
	if err := a.Sup.Start("authserver"); err != nil {
		return err
	}
	return a.Sup.Start("worldserver")
}

// waitTCP polls until addr accepts connections.
func waitTCP(host string, port int, d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 2*time.Second)
		if err == nil {
			_ = c.Close()
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s:%d", host, port)
}

// ---- state recorder bridging ----

// SetDBPort implements dbinit.StateRecorder.
func (a *App) SetDBPort(port int) error {
	return a.State.Update(func(d *state.Data) { d.Database.Port = port })
}

// IsApplied / MarkApplied implements dbinit.PatchApplier.
func (a *App) IsApplied(label string) bool {
	_, ok := a.State.Get().Patches[label]
	return ok
}

func (a *App) MarkApplied(label, sum string) error {
	return a.State.Update(func(d *state.Data) {
		d.Patches[label] = state.PatchRecord{AppliedAt: time.Now().Format(time.RFC3339), Checksum: sum}
	})
}

// RunSetup executes the first-run pipeline step by step. Re-runnable:
// it resumes from the first incomplete step and clears stale errors.
func (a *App) RunSetup(progress func(step, detail string)) error {
	st := a.State.Get()
	if st.Setup.InProgress {
		return fmt.Errorf("初始化已在进行中")
	}
	step := st.PendingStep()
	if step == state.StepDone {
		return nil
	}
	_ = a.State.Update(func(d *state.Data) {
		d.Setup.Current = step
		d.Setup.Error = ""
		d.Setup.InProgress = true
	})
	setErr := func(err error) {
		_ = a.State.Update(func(d *state.Data) { d.Setup.Error = err.Error() })
	}

	fail := func(err error) error {
		setErr(err)
		if progress != nil {
			progress(step, "失败: "+err.Error())
		}
		return err
	}

	for {
		switch step {
		case state.StepEnvCheck:
			if progress != nil {
				progress(step, "检查运行环境")
			}
			if _, err := os.Stat(a.Bins.MariaDBD); err != nil {
				return fail(fmt.Errorf("数据库运行时缺失"))
			}
			_ = a.State.MarkStepDone(step)
			step = state.NextStep(step)

		case state.StepDBInit:
			if progress != nil {
				progress(step, "初始化内置数据库")
			}
			// Phase A: datadir + my.cnf + credentials (before mariadbd).
			if _, err := dbinit.BootstrapPrepare(a.Paths, a.Log, a,
				a.Bins.MariaDBD, a.Bins.InstallDB,
				func(detail string) {
					if progress != nil {
						progress(step, "初始化内置数据库: "+detail)
					}
				}); err != nil {
				return fail(err)
			}
			// Re-register with the real port, then start.
			a.registerMysqld()
			if !a.Sup.Running("mysqld") {
				if err := a.Sup.Start("mysqld"); err != nil {
					return fail(err)
				}
			}
			port := a.State.Get().Database.Port
			if err := waitTCP("127.0.0.1", port, 90*time.Second); err != nil {
				return fail(err)
			}
			// Phase B: root password, databases, app user.
			creds, err := dbinit.LoadCreds(a.Paths)
			if err != nil || creds == nil {
				return fail(fmt.Errorf("数据库凭据缺失"))
			}
			if err := dbinit.ProvisionAccounts(a.Paths, creds, a.Log); err != nil {
				return fail(err)
			}
			a.registerServers()
			_ = a.State.MarkStepDone(step)
			step = state.NextStep(step)

		case state.StepDBImport:
			if progress != nil {
				progress(step, "导入数据库（基础 + 更新 + 模块）")
			}
			creds, _ := dbinit.LoadCreds(a.Paths)
			files := dbinit.BuildPipeline(a.Paths)
			var localeFiles, coreFiles []dbinit.SQLFile
			for _, f := range files {
				if f.Label == "locale/zhCN" {
					localeFiles = append(localeFiles, f)
				} else {
					coreFiles = append(coreFiles, f)
				}
			}
			if err := dbinit.ImportAll(a.Paths, a.Bins.MysqlClient, creds.Root, coreFiles,
				a, a.Log, func(done, total int, label string) {
					if progress != nil {
						progress(step, fmt.Sprintf("数据库导入 %d/%d: %s", done, total, filepath.Base(label)))
					}
				}); err != nil {
				return fail(err)
			}
			_ = a.State.MarkStepDone(step)
			step = state.NextStep(step)

		case state.StepPlayerbotInit:
			if progress != nil {
				progress(step, "初始化 Playerbot 数据")
			}
			// Playerbots module SQL ran in the pipeline; ensure bot tables
			// exist by checking one marker table.
			if db, err := a.DB(); err == nil {
				var n int
				_ = db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables
					WHERE table_schema='acore_playerbots'`).Scan(&n)
				if n == 0 {
					return fail(fmt.Errorf("Playerbots 数据库为空，模块 SQL 未导入"))
				}
			}
			_ = a.State.MarkStepDone(step)
			step = state.NextStep(step)

		case state.StepLocaleImport:
			if progress != nil {
				progress(step, "导入 GSWXY 中文数据")
			}
			creds, _ := dbinit.LoadCreds(a.Paths)
			files := dbinit.BuildPipeline(a.Paths)
			var localeFiles []dbinit.SQLFile
			for _, f := range files {
				if f.Label == "locale/zhCN" {
					localeFiles = append(localeFiles, f)
				}
			}
			if len(localeFiles) > 0 {
				if err := dbinit.ImportAll(a.Paths, a.Bins.MysqlClient, creds.Root, localeFiles,
					a, a.Log, func(done, total int, label string) {
						if progress != nil {
							progress(step, fmt.Sprintf("中文数据 %d/%d: %s", done, total, filepath.Base(label)))
						}
					}); err != nil {
					return fail(err)
				}
			}
			_ = a.State.Update(func(d *state.Data) {
				d.Locale.Version = a.Ver.Locale.Version
				d.Locale.ImportedAt = time.Now().Format(time.RFC3339)
			})
			_ = a.State.MarkStepDone(step)
			step = state.NextStep(step)

		case state.StepClientData:
			if progress != nil {
				progress(step, "客户端数据（可稍后在「数据」页下载）")
			}
			// Not fatal: data can be downloaded later via the UI.
			_ = a.State.MarkStepDone(step)
			step = state.NextStep(step)

		case state.StepRealm:
			if progress != nil {
				progress(step, "注册 Realm 与最终配置")
			}
			if err := a.regenerateFull(); err != nil {
				return fail(err)
			}
			if err := a.registerRealm(); err != nil {
				return fail(err)
			}
			_ = a.State.MarkStepDone(step)
			step = state.NextStep(step)

		case state.StepDone:
			_ = a.State.Update(func(d *state.Data) {
				d.Setup.InProgress = false
				d.Setup.Initialized = true
				d.Setup.Error = ""
			})
			if progress != nil {
				progress(step, "初始化完成")
			}
			return nil

		default:
			return fail(fmt.Errorf("未知的初始化步骤: %s", step))
		}
	}
}

// regenerateConfigs writes the run configs from the three layers.
func (a *App) regenerateConfigs() error {
	for _, conf := range confman.ConfFiles {
		dist, err := confman.ParseDist(a.distFor(conf))
		if err != nil {
			return fmt.Errorf("解析 %s.dist: %w", conf, err)
		}
		if err := a.CM.Generate(conf, dist); err != nil {
			return fmt.Errorf("生成 %s: %w", conf, err)
		}
	}
	return nil
}

// distFor resolves the .conf.dist path for a logical conf name.
func (a *App) distFor(conf string) string {
	if conf == "playerbots.conf" {
		return filepath.Join(a.Paths.EtcDist(), "modules", "playerbots.conf.dist")
	}
	return filepath.Join(a.Paths.EtcDist(), conf+".dist")
}

// RegenerateConfigs is the API entry.
func (a *App) RegenerateConfigs() error { return a.regenerateConfigs() }
