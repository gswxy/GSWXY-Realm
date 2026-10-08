package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gswxy/gswxy-realm/manager/internal/accounts"
	"github.com/gswxy/gswxy-realm/manager/internal/app"
	"github.com/gswxy/gswxy-realm/manager/internal/confman"
	"github.com/gswxy/gswxy-realm/manager/internal/state"
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
	http.SetCookie(w, s.Auth.secureCookie(tok))
	writeJSON(w, 200, map[string]string{"token": tok, "csrf": csrfOf(tok)})
}

// handleLogout rotates the signing key: the cookie is cleared AND every
// outstanding Bearer token becomes invalid immediately.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.Admin.RotateSessionKey(); err != nil {
		s.Log.Error("rotate session key: %v", err)
	}
	http.SetCookie(w, &http.Cookie{Name: "gswxy_session", Value: "", MaxAge: -1, Path: "/"})
	s.Log.Audit(clientIP(r), "auth.logout", "")
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
	http.SetCookie(w, s.Auth.secureCookie(tok))
	s.Log.Audit(clientIP(r), "auth.password.set", "")
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

// handleRestart 与前端三个进程的独立重启按钮一一对应；all 为全量重启。
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target string `json:"target"`
	}
	_ = decodeJSON(r, &req)
	if err := s.App.RestartTarget(req.Target); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// ---- overview / preflight / setup ----

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	st := s.State.Get()
	out := map[string]any{
		"processes":   s.App.Sup.StatusSnapshot(),
		"readiness":   s.App.ServiceReadiness(),
		"setup":       st.Setup,
		"realm":       st.Realm,
		"locale":      st.Locale,
		"client_data": st.ClientData,
		"version":     s.App.Ver,
		"backup":      st.Backup,
		"events":      s.recentEvents(),
	}
	// 统一口径的在线统计；数据库未就绪时置空（前端显示 —，不显示假 0）。
	if pop, err := s.App.OnlinePopulation(); err == nil {
		out["population"] = pop
	} else {
		out["population_error"] = err.Error()
	}
	pub, loc := s.App.EffectiveRealmAddresses()
	out["realm_effective"] = map[string]any{
		"address": pub, "local_address": loc, "port": s.App.RealmPort(),
	}
	writeJSON(w, 200, out)
}

// recentEvents surfaces the last audit records (management actions) plus
// recent manager log errors — the overview "最近事件" card. Passwords and
// credentials are never written to these lines in the first place.
func (s *Server) recentEvents() []string {
	lines, err := s.App.TailLog("审计日志", 8, "")
	if err != nil || len(lines) == 0 {
		return nil
	}
	// newest last -> newest first
	out := make([]string, 0, len(lines))
	for i := len(lines) - 1; i >= 0; i-- {
		out = append(out, lines[i])
	}
	return out
}

func (s *Server) handlePreflight(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.App.Preflight())
}

// handleSetupStart 拒绝并发初始化：已有任务在跑时返回 409。
func (s *Server) handleSetupStart(w http.ResponseWriter, r *http.Request) {
	if s.App.SetupRunning() {
		fail(w, 409, "初始化已在进行中")
		return
	}
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
		// NAS 本地导入候选：downloads/manual 下匹配的 Data.zip
		"manual_candidates": s.App.CD.ManualCandidates(res),
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

// handleDataScan imports a Data.zip the user dropped into downloads/manual
// through the fnOS file manager (no browser upload needed for 1+ GB files).
func (s *Server) handleDataScan(w http.ResponseWriter, r *http.Request) {
	res, err := s.App.CD.LoadResource()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	path, err := s.App.CD.ScanManualDir(res)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err := s.App.CD.Download(res, path); err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1", "file": filepath.Base(path)})
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

// handleConfigSet 服务端同样校验（不依赖前端）：键必须在 schema 中，
// 值类型匹配；配置生成失败如实报错而不是提示成功。
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
	schema, err := s.App.ConfigSchemaCached(req.Conf)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	entry, ok := schema.Get(req.Key)
	if !ok {
		fail(w, 400, "未知配置项: "+req.Key)
		return
	}
	if err := confman.ValidateValue(entry, req.Value); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err := s.App.CM.SetUser(req.Conf, req.Key, req.Value); err != nil {
		fail(w, 500, err.Error())
		return
	}
	if err := s.App.RegenerateAll(); err != nil {
		fail(w, 500, "配置已保存但生成运行配置失败: "+err.Error())
		return
	}
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
	if err := s.App.RegenerateAll(); err != nil {
		fail(w, 500, "已保存但生成运行配置失败: "+err.Error())
		return
	}
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
	if err := s.App.RegenerateAll(); err != nil {
		fail(w, 500, "已还原但生成运行配置失败: "+err.Error())
		return
	}
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

// handleBotSummary 统一统计口径（机器人=随机机器人账号前缀），区分
// 在线与存量，SQL 错误如实返回而不是显示 0。
func (s *Server) handleBotSummary(w http.ResponseWriter, r *http.Request) {
	pop, err := s.App.OnlinePopulation()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	out := map[string]any{"population": pop}
	if n, err := s.App.NamePoolSize(); err == nil {
		out["name_pool"] = n
	}
	if n, err := s.App.TotalCharacters(); err == nil {
		out["total_characters"] = n
	}
	if a, h, err := s.App.FactionSplit(true); err == nil {
		out["online_alliance"], out["online_horde"] = a, h
	}
	if a, h, err := s.App.FactionSplit(false); err == nil {
		out["alliance"], out["horde"] = a, h
	}
	if m, err := s.App.KeyValueCount("level", true); err == nil {
		out["online_levels"] = m
	}
	if m, err := s.App.KeyValueCount("class", true); err == nil {
		out["online_classes"] = m
	}
	if m, err := s.App.KeyValueCount("level", false); err == nil {
		out["levels"] = m
	}
	if m, err := s.App.KeyValueCount("class", false); err == nil {
		out["classes"] = m
	}
	writeJSON(w, 200, out)
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
	if req.GMLevel < 0 || req.GMLevel > 3 {
		fail(w, 400, "GM 等级必须是 0-3")
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
		if err := mgr.SetGMLevel(req.Username, req.GMLevel); err != nil {
			fail(w, 500, "账号已创建，但设置 GM 等级失败: "+err.Error())
			return
		}
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
	if req.GMLevel < 0 || req.GMLevel > 3 {
		fail(w, 400, "GM 等级必须是 0-3")
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
	if reason := strings.TrimSpace(req.Reason); reason == "" {
		fail(w, 400, "封禁需要填写原因")
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
	if id <= 0 {
		fail(w, 400, "参数不合法")
		return
	}
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
	n := 300
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

// handleLogExport bundles recent logs + version info as a diagnostics text
// file. Secrets never appear in these logs (audit redacts passwords;
// credential files are not logs).
func (s *Server) handleLogExport(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString("GSWXY Realm 诊断信息导出\n")
	b.WriteString("生成时间: " + time.Now().Format("2006-01-02 15:04:05") + "\n\n")
	v := s.App.Ver
	fmt.Fprintf(&b, "版本: %s (%s)  Core=%s Playerbots=%s\n\n", v.Version, v.Channel,
		v.Core.Commit, v.Playerbots.Commit)
	b.WriteString("== 进程状态 ==\n")
	for k, val := range s.App.Sup.StatusSnapshot() {
		fmt.Fprintf(&b, "%s = %s\n", k, val)
	}
	st := s.State.Get()
	fmt.Fprintf(&b, "\n== 初始化 ==\ncurrent=%s initialized=%v error=%s\n\n",
		st.Setup.Current, st.Setup.Initialized, st.Setup.Error)
	for _, f := range s.App.LogFiles() {
		fmt.Fprintf(&b, "== %s (最后 120 行) ==\n", f["name"])
		lines, err := s.App.TailLog(f["name"], 120, "")
		if err != nil {
			fmt.Fprintf(&b, "读取失败: %v\n", err)
			continue
		}
		for _, l := range lines {
			b.WriteString(l + "\n")
		}
		b.WriteString("\n")
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="gswxy-diagnostics.txt"`)
	_, _ = w.Write([]byte(b.String()))
}

// ---- backups ----

func (s *Server) handleBackupList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.App.BK.List())
}

func (s *Server) handleBackupStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.App.BK.Status())
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
	_ = s.State.Update(func(d *state.Data) { d.Backup.LastCreated = time.Now().Format(time.RFC3339) })
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
			return
		}
		s.App.RecreateDB()
		_ = s.State.Update(func(d *state.Data) { d.Backup.LastRestore = time.Now().Format(time.RFC3339) })
	}()
	writeJSON(w, 200, map[string]string{"ok": "restore_started"})
}

// handleBackupAuto toggles the daily automatic backup.
func (s *Server) handleBackupAuto(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if decodeJSON(r, &req) != nil {
		fail(w, 400, "参数不合法")
		return
	}
	if err := s.App.SetAutoBackup(req.Enabled); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true, "enabled": req.Enabled})
}

// ---- version / realm / update ----

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	st := s.State.Get()
	writeJSON(w, 200, map[string]any{
		"build":       s.App.Ver,
		"client_data": st.ClientData,
		"locale":      st.Locale,
	})
}

// handleUpdateCheck 查询 GitHub Releases（只读，不自动安装）。
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	channel := "stable"
	if c := r.URL.Query().Get("channel"); c == "nightly" {
		channel = "nightly"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	res := s.Updater.Check(ctx, s.App.Ver.Version, channel)
	writeJSON(w, 200, res)
}

func (s *Server) handleRealmAddresses(w http.ResponseWriter, r *http.Request) {
	d := s.State.Get()
	pub, loc := s.App.EffectiveRealmAddresses()
	apub, aloc := s.App.AutoRealmAddresses()
	writeJSON(w, 200, map[string]any{
		"configured": map[string]string{"address": d.Realm.Address, "local_address": d.Realm.LocalAddress},
		"effective":  map[string]string{"address": pub, "local_address": loc},
		"auto":       map[string]string{"address": apub, "local_address": aloc},
		"port":       s.App.RealmPort(),
	})
}

func (s *Server) handleRealmAddressesSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Address      string `json:"address"`
		LocalAddress string `json:"local_address"`
	}
	if decodeJSON(r, &req) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	if err := s.App.SetRealmAddresses(req.Address, req.LocalAddress); err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// handleLauncher serves the generated Windows .bat. Auth accepts the
// Bearer header (iframe) or cookie (plain navigation) via the session
// middleware; the frontend downloads it as a blob with the header.
func (s *Server) handleLauncher(w http.ResponseWriter, r *http.Request) {
	bat := s.App.LauncherBAT()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="GSWXY-Realm-Launcher.bat"`)
	_, _ = w.Write(bat)
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
