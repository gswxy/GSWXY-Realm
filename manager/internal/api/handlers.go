package api

import (
	"database/sql"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gswxy/gswxy-realm/manager/internal/accounts"
	"github.com/gswxy/gswxy-realm/manager/internal/app"
	"github.com/gswxy/gswxy-realm/manager/internal/confman"
)

// ---- auth handlers ----

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if decodeJSON(r, &req) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	if !s.Auth.AllowLogin(clientIP(r)) {
		fail(w, 429, "尝试过于频繁，请稍后再试")
		return
	}
	if !s.Admin.Verify(req.Password) {
		fail(w, 401, "密码错误")
		return
	}
	tok := s.Auth.Issue("admin")
	http.SetCookie(w, s.Auth.secureCookie())
	writeJSON(w, 200, map[string]string{"token": tok, "csrf": csrfOf(tok)})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "gswxy_session", Value: "", MaxAge: -1, Path: "/"})
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"initialized": s.Admin.Initialized(),
		"auth_needed": !s.skipAuth(),
	})
}

func (s *Server) handleSetupPassword(w http.ResponseWriter, r *http.Request) {
	if s.Admin.Initialized() && !s.skipAuth() {
		fail(w, 403, "管理员密码已设置")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if decodeJSON(r, &req) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	if err := s.Admin.SetPassword(req.Password); err != nil {
		fail(w, 400, err.Error())
		return
	}
	tok := s.Auth.Issue("admin")
	http.SetCookie(w, s.Auth.secureCookie())
	writeJSON(w, 200, map[string]string{"token": tok, "csrf": csrfOf(tok)})
}

// ---- lifecycle ----

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if err := s.App.StartAll(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.App.StopAll()
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target string `json:"target"`
	}
	_ = decodeJSON(r, &req)
	switch req.Target {
	case "worldserver":
		_ = s.App.Sup.Stop("worldserver")
		if err := s.App.Sup.Start("worldserver"); err != nil {
			fail(w, 500, err.Error())
			return
		}
	case "", "all":
		s.App.StopAll()
		if err := s.App.StartAll(); err != nil {
			fail(w, 500, err.Error())
			return
		}
	default:
		fail(w, 400, "未知目标")
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// ---- overview / preflight / setup ----

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	st := s.State.Get()
	out := map[string]any{
		"processes":   s.App.Sup.StatusSnapshot(),
		"setup":       st.Setup,
		"realm":       st.Realm,
		"locale":      st.Locale,
		"client_data": st.ClientData,
		"version":     s.App.Ver,
	}
	if db, err := s.App.DB(); err == nil {
		var realPlayers, online int
		_ = db.QueryRow(`SELECT COUNT(*) FROM acore_characters.characters
			WHERE online = 1 AND account NOT IN
			  (SELECT DISTINCT account FROM acore_characters.playerbots)`).Scan(&realPlayers)
		_ = db.QueryRow(`SELECT COUNT(*) FROM acore_characters.characters WHERE online = 1`).Scan(&online)
		bots := online - realPlayers
		out["population"] = map[string]int{
			"real_players": realPlayers, "bots": bots, "total": online,
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) handlePreflight(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.App.Preflight())
}

func (s *Server) handleSetupStart(w http.ResponseWriter, r *http.Request) {
	go func() {
		if err := s.App.RunSetup(func(step, detail string) {
			s.Log.Info("setup %s: %s", step, detail)
		}); err != nil {
			s.Log.Error("setup failed: %v", err)
		}
	}()
	writeJSON(w, 200, map[string]string{"ok": "started"})
}

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.State.Get().Setup)
}

// ---- client data ----

func (s *Server) handleDataStatus(w http.ResponseWriter, r *http.Request) {
	res, err := s.App.CD.LoadResource()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	st := s.State.Get()
	writeJSON(w, 200, map[string]any{
		"required":  res.Version,
		"label":     res.Label,
		"installed": st.ClientData,
		"progress":  s.App.CD.ProgressSnapshot(),
		"missing":   s.App.CD.RequiredDirs(res),
	})
}

func (s *Server) handleDataDownload(w http.ResponseWriter, r *http.Request) {
	res, err := s.App.CD.LoadResource()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if err := s.App.CD.Download(res, ""); err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) handleDataCancel(w http.ResponseWriter, r *http.Request) {
	s.App.CD.Cancel()
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// handleDataImport accepts an uploaded Data.zip (tier-3 manual source).
func (s *Server) handleDataImport(w http.ResponseWriter, r *http.Request) {
	res, err := s.App.CD.LoadResource()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, res.SizeBytes+(1<<30))
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		fail(w, 400, "上传失败: "+err.Error())
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		fail(w, 400, "请上传 Data.zip 文件")
		return
	}
	defer file.Close()
	dst := filepath.Join(s.App.Paths.Downloads(), "manual", res.Filename)
	_ = os.MkdirAll(filepath.Dir(dst), 0o755)
	out, err := os.Create(dst)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if _, err := io.Copy(out, file); err != nil {
		out.Close()
		fail(w, 500, err.Error())
		return
	}
	out.Close()
	if err := s.App.CD.Download(res, dst); err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// ---- config ----

func (s *Server) handleConfigList(w http.ResponseWriter, r *http.Request) {
	conf := r.URL.Query().Get("conf")
	if !validConf(conf) {
		fail(w, 400, "未知配置文件")
		return
	}
	dist, err := confman.ParseDist(s.App.DistFor(conf))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	eff, err := s.App.CM.Resolve(conf, dist)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, eff)
}

// handleConfigSchema returns the generated config-schema.json (zhCN).
func (s *Server) handleConfigSchema(w http.ResponseWriter, r *http.Request) {
	raw, err := os.ReadFile(filepath.Join(s.App.Paths.AppDest, "config-schema.json"))
	if err != nil {
		writeJSON(w, 200, map[string]any{})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(raw)
}

func validConf(c string) bool {
	switch c {
	case "worldserver.conf", "authserver.conf", "playerbots.conf":
		return true
	}
	return false
}

func (s *Server) handleConfigSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Conf  string `json:"conf"`
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if decodeJSON(r, &req) != nil || !validConf(req.Conf) ||
		req.Key == "" || !confKeySafe(req.Key) {
		fail(w, 400, "参数不合法")
		return
	}
	if err := s.App.CM.SetUser(req.Conf, req.Key, req.Value); err != nil {
		fail(w, 500, err.Error())
		return
	}
	_ = s.App.RegenerateAll()
	writeJSON(w, 200, map[string]string{"ok": "1", "restart_hint": restartHint(s.App, req.Conf, req.Key)})
}

func confKeySafe(k string) bool {
	if k == "" || len(k) > 120 {
		return false
	}
	for _, ch := range k {
		ok := ch == '.' || ch == '_' ||
			(ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')
		if !ok {
			return false
		}
	}
	return true
}

func restartHint(a *app.App, conf, key string) string {
	if sc, ok := a.ConfigSchemas()[conf]; ok {
		if e, ok2 := sc.Get(key); ok2 && e.Restart {
			return "该配置需要重启服务器后生效"
		}
	}
	return "大部分配置需重启 WorldServer 后生效"
}

func (s *Server) handleConfigRaw(w http.ResponseWriter, r *http.Request) {
	conf := r.URL.Query().Get("conf")
	if !validConf(conf) {
		fail(w, 400, "未知配置文件")
		return
	}
	raw, err := s.App.CM.ReadUserRaw(conf)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	run, _ := os.ReadFile(s.App.CM.RunPath(conf))
	writeJSON(w, 200, map[string]string{"user": raw, "run": string(run)})
}

func (s *Server) handleConfigRawSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Conf    string `json:"conf"`
		Content string `json:"content"`
	}
	if decodeJSON(r, &req) != nil || !validConf(req.Conf) {
		fail(w, 400, "参数不合法")
		return
	}
	if err := s.App.CM.WriteUserRaw(req.Conf, req.Content); err != nil {
		fail(w, 500, err.Error())
		return
	}
	_ = s.App.RegenerateAll()
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) handleConfigReset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Conf string `json:"conf"`
		Key  string `json:"key"`
	}
	if decodeJSON(r, &req) != nil || !validConf(req.Conf) {
		fail(w, 400, "参数不合法")
		return
	}
	var err error
	if req.Key != "" {
		err = s.App.CM.DeleteUserKey(req.Conf, req.Key)
	} else {
		err = s.App.CM.ResetUser(req.Conf)
	}
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	_ = s.App.RegenerateAll()
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) handleConfigRegen(w http.ResponseWriter, r *http.Request) {
	if err := s.App.RegenerateAll(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// ---- playerbot ----

func (s *Server) handleBotSummary(w http.ResponseWriter, r *http.Request) {
	db, err := s.App.DB()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	out := map[string]any{}
	var namePool, online, alliance, horde int
	_ = db.QueryRow(`SELECT COUNT(*) FROM acore_playerbots.playerbots_names`).Scan(&namePool)
	_ = db.QueryRow(`SELECT COUNT(*) FROM acore_characters.characters WHERE online = 1`).Scan(&online)
	_ = db.QueryRow(`SELECT COUNT(*) FROM acore_characters.characters WHERE online = 1 AND race IN (1,3,4,7,11)`).Scan(&alliance)
	_ = db.QueryRow(`SELECT COUNT(*) FROM acore_characters.characters WHERE online = 1 AND race IN (2,5,6,8,10)`).Scan(&horde)
	out["name_pool"] = namePool
	out["online_total"] = online
	out["alliance"] = alliance
	out["horde"] = horde
	out["bots"] = online - s.realPlayers(db)
	out["real_players"] = s.realPlayers(db)

	levels := map[string]int{}
	if rows, err := db.Query(`SELECT level, COUNT(*) FROM acore_characters.characters GROUP BY level`); err == nil {
		for rows.Next() {
			var lv, n int
			if rows.Scan(&lv, &n) == nil {
				levels[strconv.Itoa(lv)] = n
			}
		}
		rows.Close()
	}
	out["levels"] = levels

	classes := map[string]int{}
	if rows, err := db.Query(`SELECT class, COUNT(*) FROM acore_characters.characters GROUP BY class`); err == nil {
		for rows.Next() {
			var cl, n int
			if rows.Scan(&cl, &n) == nil {
				classes[strconv.Itoa(cl)] = n
			}
		}
		rows.Close()
	}
	out["classes"] = classes
	writeJSON(w, 200, out)
}

func (s *Server) realPlayers(db *sql.DB) int {
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM acore_characters.characters
		WHERE online = 1 AND account NOT IN (SELECT account FROM acore_playerbots.playerbots_accounts)`).Scan(&n)
	return n
}

func (s *Server) handleBotProfiles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.App.BotProfiles())
}

func (s *Server) handleBotApplyProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if decodeJSON(r, &req) != nil {
		fail(w, 400, "参数不合法")
		return
	}
	diff, err := s.App.ApplyBotProfile(req.Name)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"applied": req.Name, "changes": diff})
}

// ---- accounts ----

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	db, err := s.App.DB()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	list, err := (&accounts.Manager{DB: db}).List(r.URL.Query().Get("q"), 100)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, list)
}

func (s *Server) handleAccountCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		GMLevel  int    `json:"gmlevel"`
	}
	if decodeJSON(r, &req) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	db, err := s.App.DB()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	mgr := &accounts.Manager{DB: db}
	id, err := mgr.Create(req.Username, req.Password)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if req.GMLevel > 0 {
		_ = mgr.SetGMLevel(req.Username, req.GMLevel)
	}
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *Server) handleAccountPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if decodeJSON(r, &req) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	db, err := s.App.DB()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if err := (&accounts.Manager{DB: db}).ChangePassword(req.Username, req.Password); err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) handleAccountGM(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		GMLevel  int    `json:"gmlevel"`
	}
	if decodeJSON(r, &req) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	db, err := s.App.DB()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if err := (&accounts.Manager{DB: db}).SetGMLevel(req.Username, req.GMLevel); err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) handleAccountBan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Reason   string `json:"reason"`
		Duration string `json:"duration"`
	}
	if decodeJSON(r, &req) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	db, err := s.App.DB()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if err := (&accounts.Manager{DB: db}).Ban(req.Username, req.Reason, req.Duration); err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) handleAccountUnban(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
	}
	if decodeJSON(r, &req) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	db, err := s.App.DB()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if err := (&accounts.Manager{DB: db}).Unban(req.Username); err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) handleCharacters(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	db, err := s.App.DB()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	list, err := (&accounts.Manager{DB: db}).Characters(id)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, list)
}

// ---- console ----

func (s *Server) handleConsoleTail(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"lines":  s.App.Sup.ConsoleTail("worldserver", 100),
		"status": s.App.Sup.StatusSnapshot(),
	})
}

func (s *Server) handleConsoleSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Command string `json:"command"`
	}
	if decodeJSON(r, &req) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	cmd := strings.TrimSpace(req.Command)
	if cmd == "" || strings.ContainsAny(cmd, "\n\r") {
		fail(w, 400, "命令不能为空或包含换行")
		return
	}
	// Single-line AC console command written directly to the child's
	// stdin pipe; no shell anywhere in this path.
	if err := s.App.Sup.WriteConsole("worldserver", cmd); err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// ---- logs ----

func (s *Server) handleLogList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.App.LogFiles())
}

func (s *Server) handleLogTail(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	n := 200
	if v, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && v > 0 && v <= 2000 {
		n = v
	}
	lines, err := s.App.TailLog(name, n, r.URL.Query().Get("filter"))
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, lines)
}

// ---- backups ----

func (s *Server) handleBackupList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.App.BK.List())
}

func (s *Server) handleBackupCreate(w http.ResponseWriter, r *http.Request) {
	creds, err := s.App.LoadDBCreds()
	if err != nil {
		fail(w, 500, "数据库尚未初始化")
		return
	}
	path, err := s.App.BK.Create(s.App.Bins.MysqlDump, creds.Root)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"file": filepath.Base(path)})
}

func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		File string `json:"file"`
	}
	if decodeJSON(r, &req) != nil || req.File == "" {
		fail(w, 400, "参数不合法")
		return
	}
	creds, err := s.App.LoadDBCreds()
	if err != nil {
		fail(w, 500, "数据库尚未初始化")
		return
	}
	go func() {
		if err := s.App.BK.Restore(req.File, s.App.Bins.MysqlClient, s.App.Bins.MysqlDump, creds.Root); err != nil {
			s.Log.Error("restore failed: %v", err)
		} else {
			s.App.RecreateDB()
		}
	}()
	writeJSON(w, 200, map[string]string{"ok": "restore_started"})
}

// ---- version / realm ----

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	st := s.State.Get()
	writeJSON(w, 200, map[string]any{
		"build":       s.App.Ver,
		"client_data": st.ClientData,
		"locale":      st.Locale,
	})
}

func (s *Server) handleRealmName(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if decodeJSON(r, &req) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	if err := s.App.SetRealmName(req.Name); err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}
