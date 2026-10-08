/* 艾泽旅伴 · GSWXY Realm WebUI — 中文单页应用 */
"use strict";

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => [...document.querySelectorAll(sel)];

let TOKEN = localStorage.getItem("gsrm_token") || "";
let CSRF = localStorage.getItem("gsrm_csrf") || "";
let CURRENT = "#/overview";

// ---------- 基础 API ----------
async function api(method, path, body, isForm) {
  const headers = {};
  if (TOKEN) {
    // Bearer 令牌鉴权：不依赖 cookie（fnOS 桌面是跨站 iframe，cookie
    // 会被浏览器扣下；空值 HttpOnly cookie 也会挡掉 JS 补设）
    headers["Authorization"] = "Bearer " + TOKEN;
    if (method === "POST" && !isForm) headers["X-CSRF-Token"] = CSRF;
  }
  let opt = { method, headers };
  if (body !== undefined) {
    if (isForm) { opt.body = body; }
    else { headers["Content-Type"] = "application/json"; opt.body = JSON.stringify(body); }
  }
  const res = await fetch(path, opt);
  const text = await res.text();
  let data = {};
  try { data = text ? JSON.parse(text) : {}; } catch { data = { error: text }; }
  if (res.status === 401 && path !== "/api/login" && path !== "/api/session") {
    TOKEN = ""; CSRF = "";
    localStorage.removeItem("gsrm_token");
    localStorage.removeItem("gsrm_csrf");
    if (location.hash !== "#/login") location.hash = "#/login";
    throw new Error("登录已失效，请重新登录");
  }
  if (!res.ok) throw new Error(data.error || ("请求失败 (" + res.status + ")"));
  return data;
}

function esc(s) {
  return String(s ?? "").replace(/[&<>"]/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
}
const fmtBytes = (n) => {
  if (n === undefined || n === null) return "—";
  if (n > 1 << 30) return (n / (1 << 30)).toFixed(2) + " GB";
  if (n > 1 << 20) return (n / (1 << 20)).toFixed(1) + " MB";
  if (n > 1024) return (n / 1024).toFixed(0) + " KB";
  return n + " B";
};
function debounce(fn, ms) {
  let t;
  return (...args) => { clearTimeout(t); t = setTimeout(() => fn(...args), ms); };
}

function pill(text, cls) { return `<span class="pill ${cls}">${esc(text)}</span>`; }
function statusPill(st) {
  switch (st) {
    case "running": return pill("运行中", "ok");
    case "stopped": return pill("已停止", "idle");
    case "starting": return pill("启动中", "warn");
    case "stopping": return pill("停止中", "warn");
    case "backoff": return pill("崩溃等待重启", "warn");
    case "failed": return pill("故障（需手动启动）", "bad");
    default: return pill(st || "未知", "idle");
  }
}

// ---------- Toast ----------
function toast(msg, type = "ok", ms = 3600) {
  const el = document.createElement("div");
  el.className = "toast " + type;
  el.textContent = msg;
  $("#toasts").appendChild(el);
  setTimeout(() => { el.style.opacity = "0"; el.style.transition = "opacity .3s"; }, ms - 300);
  setTimeout(() => el.remove(), ms);
}

// ---------- 弹窗 ----------
const modal = {
  open({ title, bodyHTML, actions, onOpen }) {
    $("#modal-title").textContent = title;
    $("#modal-body").innerHTML = bodyHTML;
    const act = $("#modal-actions");
    act.innerHTML = "";
    const close = () => { $("#modal-mask").hidden = true; };
    (actions || [{ label: "关闭" }]).forEach(a => {
      const b = document.createElement("button");
      b.textContent = a.label;
      if (a.cls) b.className = a.cls;
      b.onclick = async () => {
        if (a.onClick) { const r = await a.onClick(close); if (r === false) return; }
        if (!a.keepOpen) close();
      };
      act.appendChild(b);
    });
    $("#modal-mask").hidden = false;
    if (onOpen) onOpen(close);
  },
  close() { $("#modal-mask").hidden = true; },
};
// 点遮罩关闭
$("#modal-mask").addEventListener("mousedown", (e) => {
  if (e.target.id === "modal-mask") modal.close();
});
document.addEventListener("keydown", (e) => { if (e.key === "Escape") modal.close(); });

// 确认弹窗（替代原生 confirm）
function confirmBox(title, text, okLabel = "确认", danger = true) {
  return new Promise((resolve) => {
    modal.open({
      title,
      bodyHTML: typeof text === "string" && text.startsWith("<") ? text : `<p>${esc(text)}</p>`,
      actions: [
        { label: "取消", onClick: () => resolve(false) },
        { label: okLabel, cls: danger ? "danger" : "primary", onClick: () => resolve(true) },
      ],
    });
  });
}

// ---------- 下载工具（Bearer 鉴权，兼容 iframe） ----------
async function authDownload(path, filename) {
  try {
    const res = await fetch(path, { headers: { "Authorization": "Bearer " + TOKEN } });
    if (!res.ok) throw new Error("下载失败 (" + res.status + ")，请重新登录后再试");
    const blob = await res.blob();
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(a.href), 5000);
  } catch (e) {
    toast(e.message, "err");
  }
}

// ---------- 初始化步骤 ----------
const stepNames = {
  env_check: "运行环境检查",
  db_init: "初始化内置数据库",
  db_import: "导入基础数据库与模块数据",
  playerbot_init: "Playerbot 数据校验",
  locale_import: "导入中文数据",
  client_data: "客户端数据（可稍后下载）",
  realm: "注册 Realm 与最终配置",
  done: "完成",
};

// ---------- 路由 ----------
const routes = ["overview", "server", "playerbot", "accounts", "config", "data", "logs", "backup", "version", "login"];

function render() {
  const hash = location.hash || "#/overview";
  CURRENT = hash;
  const name = hash.replace("#/", "").split("?")[0];
  $$(".page").forEach(p => p.classList.remove("active"));
  const page = $("#page-" + name) || $("#page-overview");
  page.classList.add("active");
  $$("#nav a").forEach(a => a.classList.toggle("active", a.getAttribute("href") === "#/" + name));

  const loaders = {
    overview: loadOverview, server: loadServer, playerbot: loadPlayerbot,
    accounts: loadAccounts, config: loadConfig, data: loadData,
    logs: loadLogs, backup: loadBackup, version: loadVersion, login: loadLogin,
  };
  (loaders[name] || loadOverview)();
}

window.addEventListener("hashchange", render);

// ---------- 登录 ----------
async function loadLogin() {
  const s = await api("GET", "/api/session");
  const initialized = s.initialized;
  $("#login-title").textContent = initialized ? "登录 艾泽旅伴" : "首次设置";
  $("#login-desc").textContent = initialized
    ? "请输入管理员密码进入管理界面。"
    : "首次使用请设置管理员密码（至少 8 位）。此密码用于登录本管理界面。";
  $("#login-password2-wrap").style.display = initialized ? "none" : "block";
}

async function doLogin() {
  const pw = $("#login-password").value;
  const pw2 = $("#login-password2").value;
  $("#login-error").textContent = "";
  try {
    const s = await api("GET", "/api/session");
    const firstSetup = !s.initialized;
    const ep = firstSetup ? "/api/setup/password" : "/api/login";
    if (firstSetup && pw !== pw2) throw new Error("两次输入的密码不一致");
    const r = await api("POST", ep, { password: pw });
    TOKEN = r.token; CSRF = r.csrf;
    localStorage.setItem("gsrm_token", TOKEN);
    localStorage.setItem("gsrm_csrf", CSRF);
    toast(firstSetup ? "管理员密码已设置" : "登录成功", "ok");
    location.hash = "#/overview";
  } catch (e) {
    $("#login-error").textContent = e.message;
  }
}

// ---------- 概览 ----------
function serviceCard(name, label, state, procState) {
  const ready = state === "ready";
  const procRunning = ["running", "backoff", "starting"].includes(procState);
  let pillHtml;
  if (procState === "failed") pillHtml = statusPill("failed");
  else if (!procRunning) pillHtml = statusPill("stopped");
  else if (ready) pillHtml = pill("运行中 · 端口就绪", "ok");
  else if (state === "starting") pillHtml = pill("启动中 · 端口未通", "warn");
  else pillHtml = pill("运行中 · 端口未通", "warn");
  return `<div class="stat card">
    <div class="stat-label">${esc(label)}</div>
    <div class="stat-value">${pillHtml}</div>
    <div class="stat-sub mono">${esc(name)}</div>
  </div>`;
}

async function loadOverview() {
  let ov;
  try { ov = await api("GET", "/api/overview"); }
  catch (e) { $("#ov-overall").textContent = "状态读取失败"; $("#ov-sub").textContent = e.message; return; }

  const pr = ov.processes || {};
  const rd = ov.readiness || {};
  const su = ov.setup || {};

  // 总体状态
  let overall, sub;
  if (!su.initialized) {
    overall = "待初始化";
    sub = su.in_progress ? "初始化进行中…" : "完成初始化后即可开服";
  } else {
    const states = ["mysqld", "authserver", "worldserver"].map(k => ({ k, proc: pr[k] || "stopped", ready: rd[k] }));
    const anyRun = states.some(s => s.proc === "running");
    const allReady = states.every(s => s.ready === "ready");
    const anyFailed = states.some(s => s.proc === "failed");
    const anyStarting = states.some(s => s.proc === "running" && s.ready === "starting");
    if (anyFailed) { overall = "有服务故障"; sub = "请在「服务器」页查看并重启故障进程"; }
    else if (allReady) { overall = "运行正常"; sub = "服务器已就绪，玩家可以进入游戏"; }
    else if (anyStarting) { overall = "启动中…"; sub = "进程已运行，等待端口就绪（WorldServer 首次启动需加载数据）"; }
    else if (anyRun) { overall = "部分运行"; sub = "部分进程未启动，可在「服务器」页逐个处理"; }
    else { overall = "已停止"; sub = "点击「启动全部」开服"; }
  }
  $("#ov-overall").textContent = overall;
  $("#ov-sub").textContent = sub;

  $("#ov-services").innerHTML =
    serviceCard("mysqld", "内置数据库", rd.mysqld, pr.mysqld) +
    serviceCard("authserver", "登录服务器 AuthServer", rd.authserver, pr.authserver) +
    serviceCard("worldserver", "世界服务器 WorldServer", rd.worldserver, pr.worldserver);

  // 在线人数
  const pop = ov.population;
  const popErr = ov.population_error;
  $("#ov-population").innerHTML = pop
    ? `<div class="stat card"><div class="stat-label">真实玩家在线</div><div class="stat-value">${pop.real_players}</div></div>
       <div class="stat card"><div class="stat-label">在线机器人</div><div class="stat-value">${pop.bots}</div></div>
       <div class="stat card"><div class="stat-label">在线总角色</div><div class="stat-value">${pop.total}</div></div>`
    : `<div class="stat card" style="grid-column:1/-1"><div class="stat-label">在线统计</div>
       <div class="stat-value muted">${popErr ? "暂不可用" : "—"}</div>
       <div class="stat-sub">${esc(popErr || "数据库就绪后显示")}</div></div>`;

  // 连接信息
  const eff = ov.realm_effective || {};
  const realmName = (ov.realm && ov.realm.name) || "艾泽旅伴";
  $("#ov-realm").innerHTML = `
    <b>服务器名称</b><span>${esc(realmName)}</span>
    <b>局域网连接</b><span class="mono">set realmlist ${esc(eff.local_address || "?")}</span>
    <b>公网连接</b><span class="mono">${esc(eff.address || "?")} · 端口 ${esc(eff.port)}</span>
    <b>客户端要求</b><span>魔兽世界 3.3.5a (12340)</span>`;
  $("#ov-realm-hint").textContent =
    `局域网玩家：把客户端 realmlist.wtf 改为 set realmlist ${eff.local_address || "<NAS IP>"}，或直接使用启动器。` +
    `账号在「账号」页创建。`;

  // 初始化状态
  let html = "";
  if (su.initialized) {
    html = `<div>${pill("初始化完成", "ok")} 全部步骤已就绪</div>`;
  } else {
    const order = Object.keys(stepNames);
    const current = su.current || "env_check";
    for (const st of order) {
      const done = su.completed && su.completed[st];
      const cur = st === current && su.in_progress;
      const mark = done ? pill("完成", "ok") : cur ? pill("进行中", "warn") : pill("待执行", "idle");
      html += `<div>${mark} ${stepNames[st]}</div>`;
    }
    if (su.error) html += `<div class="error">上次失败：${esc(su.error)}（已保存进度，可直接重试）</div>`;
  }
  $("#ov-setup").innerHTML = html;
  $("#ov-setup-actions").innerHTML = (!su.initialized && !su.in_progress)
    ? `<button class="primary" id="ov-setup-run">开始初始化 / 从断点继续</button>` : "";
  const runBtn = $("#ov-setup-run");
  if (runBtn) runBtn.onclick = async () => {
    runBtn.disabled = true;
    try {
      await api("POST", "/api/setup/start", {});
      toast("初始化已开始，可离开本页，进度会自动保存", "ok");
      loadOverview();
    } catch (e) { toast(e.message, "err"); runBtn.disabled = false; }
  };

  // 最近事件
  const events = ov.events || [];
  $("#ov-events").innerHTML = events.length
    ? events.map(e => `<div class="ev">${esc(e)}</div>`).join("")
    : `<div class="empty muted">暂无管理事件记录</div>`;

  // 备份/版本摘要
  const v = ov.version || {};
  const cd = ov.client_data || {};
  const bk = ov.backup || {};
  $("#ov-summary").innerHTML = `
    <b>产品版本</b><span>${esc(v.version || "—")}（${esc(v.channel || "")}）</span>
    <b>客户端数据</b><span>${cd.installed ? "已安装 " + esc(cd.version) : "未下载"}</span>
    <b>中文数据</b><span>${esc((ov.locale || {}).version || "未导入")}</span>
    <b>最近备份</b><span>${bk.last_created ? esc(bk.last_created.replace("T", " ").slice(0, 19)) : "从未备份"}</span>
    <b>自动备份</b><span>${bk.auto_enabled ? "每日开启" : "已关闭"}</span>`;

  // 进程详情（技术信息折叠在下方，不占首屏）
  const kvRows = [];
  for (const k of ["mysqld", "authserver", "worldserver"]) {
    kvRows.push(`<b>${k}</b><span>${statusPill(pr[k] || "stopped")} <span class="mono muted">${esc(pr[k + ".uptime"] || "")}</span></span>`);
  }
  $("#ov-procs").innerHTML = kvRows.join("");
}

// ---------- 服务器 ----------
const PROC_LABELS = { mysqld: "内置数据库", authserver: "登录服务器", worldserver: "世界服务器" };
const PROC_PORTS = { mysqld: "内部随机端口", authserver: "3724", worldserver: "8085" };

async function loadServer() {
  try {
    const pf = await api("GET", "/api/preflight");
    $("#pf-table tbody").innerHTML = pf.map(c => `<tr>
      <td>${esc(c.name)}</td>
      <td>${c.passed ? pill("通过", "ok") : pill("未通过", "bad")}</td>
      <td class="muted">${esc(c.detail)}</td>
      <td class="muted">${esc(c.fix_hint || "")}</td>
    </tr>`).join("");
  } catch (e) { toast(e.message, "err"); }

  const ov = await api("GET", "/api/overview");
  const pr = ov.processes || {};
  const rd = ov.readiness || {};
  const rows = [];
  for (const name of ["mysqld", "authserver", "worldserver"]) {
    const st = pr[name] || "stopped";
    const portState = rd[name] === "ready" ? pill("端口就绪", "ok")
      : (st === "running" ? pill("端口未通", "warn") : `<span class="muted">—</span>`);
    rows.push(`<tr>
      <td>${esc(PROC_LABELS[name] || name)}<div class="muted mono">${name}</div></td>
      <td>${statusPill(st)}</td>
      <td>${PROC_PORTS[name]}<div>${portState}</div></td>
      <td class="muted">${esc(pr[name + ".uptime"] || "—")}</td>
      <td><button data-proc="${name}" data-act="proc-restart">重启</button></td>
    </tr>`);
  }
  $("#proc-table tbody").innerHTML = rows.join("");
  $$("#proc-table [data-act=proc-restart]").forEach(b => b.onclick = async () => {
    const target = b.dataset.proc;
    if (!(await confirmBox("重启 " + (PROC_LABELS[target] || target),
      target === "mysqld"
        ? "重启数据库会同时停止并恢复游戏服务器，期间玩家会掉线。确认继续？"
        : "重启期间该服务短暂不可用，玩家可能掉线。确认继续？",
      "确认重启"))) return;
    b.disabled = true;
    try {
      await api("POST", "/api/restart", { target });
      toast("已重启 " + (PROC_LABELS[target] || target), "ok");
      loadServer();
    } catch (e) { toast(e.message, "err"); b.disabled = false; }
  });

  $("#realm-name").value = (ov.realm && ov.realm.name) || "";
  await loadNetAddrs();
  loadConsole();
}

async function loadNetAddrs() {
  const d = await api("GET", "/api/realm/addresses");
  $("#net-public").value = d.configured.address || "";
  $("#net-local").value = d.configured.local_address || "";
  const pub = d.effective.address, loc = d.effective.local_address;
  const src = (p, auto) => (p === auto ? "（自动检测）" : "（手动设置）");
  $("#net-effective").innerHTML =
    `当前生效 —— 公网：<b>${esc(pub)}</b>${src(pub, d.auto.address)} · 本地：<b>${esc(loc)}</b>${src(loc, d.auto.local_address)} · 端口：<b>${d.port}</b>`;
  $("#realmlist-hint").textContent = `手动设置：客户端 realmlist 填 set realmlist ${loc}`;
}

$("#pf-refresh").onclick = loadServer;
$("#realm-save").onclick = async () => {
  try {
    await api("POST", "/api/realm/name", { name: $("#realm-name").value });
    toast("服务器名称已保存", "ok");
  } catch (e) { toast(e.message, "err"); }
};
$("#net-save").onclick = async () => {
  try {
    await api("POST", "/api/realm/addresses",
      { address: $("#net-public").value.trim(), local_address: $("#net-local").value.trim() });
    await loadNetAddrs();
    toast("地址已保存并应用到服务器列表", "ok");
  } catch (e) { toast(e.message, "err"); }
};
$("#net-refresh").onclick = loadNetAddrs;

async function loadConsole() {
  try {
    const c = await api("GET", "/api/console/tail");
    const el = $("#console-view");
    el.textContent = (c.lines || []).join("\n") || "（暂无输出）";
    el.scrollTop = el.scrollHeight;
  } catch { /* worldserver not running */ }
}
$("#console-send").onclick = sendConsole;
$("#console-input").addEventListener("keydown", e => { if (e.key === "Enter") sendConsole(); });
async function sendConsole() {
  const cmd = $("#console-input").value.trim();
  if (!cmd) return;
  try {
    await api("POST", "/api/console/send", { command: cmd });
    $("#console-input").value = "";
    setTimeout(loadConsole, 800);
  } catch (e) { toast(e.message, "err"); }
}

// 下载启动器（Bearer blob，兼容 fnOS iframe）
$("#launcher-dl").onclick = () => authDownload("/api/launcher", "GSWXY-Realm-Launcher.bat");
$("#ov-launcher").onclick = () => authDownload("/api/launcher", "GSWXY-Realm-Launcher.bat");

// ---------- Playerbot ----------
const CLASS_NAMES = { 1: "战士", 2: "圣骑士", 3: "猎人", 4: "潜行者", 5: "牧师", 6: "死亡骑士", 7: "萨满祭司", 8: "法师", 9: "术士", 11: "德鲁伊" };
const PB_QUICK_KEYS = [
  { key: "AiPlayerbot.MinRandomBots", label: "在线机器人数量下限", type: "number", min: 0, max: 2000, desc: "随机机器人保持在线的最少数量。NAS 建议按 CPU 核数调整（每核 20-40 个较稳）。" },
  { key: "AiPlayerbot.MaxRandomBots", label: "在线机器人数量上限", type: "number", min: 0, max: 3000, desc: "随机机器人在线数量上限，直接决定 CPU/内存占用。" },
  { key: "AiPlayerbot.RandomBotAutologin", label: "开服即上线机器人", type: "bool", desc: "开启后启动 WorldServer 时随机机器人自动登录。" },
  { key: "AiPlayerbot.EnablePeriodicOnlineOffline", label: "模拟自然在线节奏", type: "bool", desc: "机器人周期性上下线，模仿真实服务器（在线数会波动）。" },
  { key: "AiPlayerbot.RandomBotJoinLfg", label: "机器人参加组队副本", type: "bool", desc: "机器人加入地下城查找器，和你组队下副本。" },
  { key: "AiPlayerbot.RandomBotAutoJoinBG", label: "机器人参加战场", type: "bool", desc: "机器人自动排战场，PvP 更热闹，但占用更高。" },
  { key: "AiPlayerbot.RandomBotGuildCount", label: "机器人公会数量", type: "number", min: 0, max: 500, desc: "随机机器人创建的公会数量。" },
  { key: "AiPlayerbot.RandomBotTalk", label: "机器人聊天", type: "bool", desc: "机器人会说/喊/综合频道聊天（配合中文数据即为中文发言）。" },
  { key: "AiPlayerbot.RandomBotUpdateInterval", label: "AI 更新间隔（秒）", type: "number", min: 5, max: 300, desc: "机器人管理器主循环间隔，调大可降低 CPU 占用（默认 20 秒）。" },
];

let pbQuickData = {};
let pbDistOnline = true;

async function loadPlayerbot() {
  let s;
  try { s = await api("GET", "/api/playerbot/summary"); }
  catch (e) {
    $("#pb-stats").innerHTML = `<div class="stat card" style="grid-column:1/-1">
      <div class="stat-label">统计不可用</div><div class="stat-value muted">—</div>
      <div class="stat-sub">${esc(e.message)}</div></div>`;
  }
  if (s) {
    const pop = s.population || {};
    $("#pb-stats").innerHTML = `
      <div class="stat card"><div class="stat-label">真实玩家在线</div><div class="stat-value">${pop.real_players ?? "—"}</div></div>
      <div class="stat card"><div class="stat-label">机器人在线</div><div class="stat-value">${pop.bots ?? "—"}</div></div>
      <div class="stat card"><div class="stat-label">在线总数</div><div class="stat-value">${pop.total ?? "—"}</div></div>
      <div class="stat card"><div class="stat-label">联盟 / 部落（在线）</div><div class="stat-value">${s.online_alliance ?? "—"} / ${s.online_horde ?? "—"}</div></div>
      <div class="stat card"><div class="stat-label">全部角色存量</div><div class="stat-value">${s.total_characters ?? "—"}</div></div>
      <div class="stat card"><div class="stat-label">中文名字池</div><div class="stat-value">${s.name_pool ?? "—"}</div></div>`;
    renderPbDists(s);
  }

  await loadPbQuick();
  await loadPbProfiles();
}

function renderPbDists(s) {
  const classes = pbDistOnline ? s.online_classes : s.classes;
  const levels = pbDistOnline ? s.online_levels : s.levels;
  $("#pb-dist-mode-hint").textContent = pbDistOnline ? "（在线）" : "（全部角色）";
  $("#pb-lvl-mode-hint").textContent = pbDistOnline ? "（在线）" : "（全部角色）";
  let ch = "";
  for (const [k, name] of Object.entries(CLASS_NAMES)) {
    const n = (classes && classes[k]) || 0;
    ch += `<div><b>${name}</b>：${n}</div>`;
  }
  const fa = pbDistOnline ? s.online_alliance : s.alliance;
  const fh = pbDistOnline ? s.online_horde : s.horde;
  $("#pb-dists").innerHTML = `<div class="kv"><div><b>联盟</b>：${fa ?? "—"}</div><div><b>部落</b>：${fh ?? "—"}</div>${ch}</div>`;

  // 等级分布：按 10 级分桶
  const buckets = [0, 0, 0, 0, 0, 0, 0, 0]; // 1-10, 11-20, ... 71-80
  let max = 0;
  if (levels) {
    for (const [lv, n] of Object.entries(levels)) {
      const i = Math.min(7, Math.floor((parseInt(lv, 10) - 1) / 10));
      if (i >= 0) { buckets[i] += n; if (buckets[i] > max) max = buckets[i]; }
    }
  }
  $("#pb-levels").innerHTML = buckets.map((n, i) =>
    `<div class="lvlbar"><span style="width:56px">${i * 10 + 1}-${i * 10 + 10} 级</span>
     <div class="bar"><i style="width:${max ? Math.round(n * 100 / max) : 0}%"></i></div>
     <span class="n">${n}</span></div>`).join("");
}
$("#pb-dist-online").onclick = () => { pbDistOnline = true; setDistTabs(); loadPlayerbot(); };
$("#pb-dist-all").onclick = () => { pbDistOnline = false; setDistTabs(); loadPlayerbot(); };
function setDistTabs() {
  $("#pb-dist-online").classList.toggle("active", pbDistOnline);
  $("#pb-dist-all").classList.toggle("active", !pbDistOnline);
}

async function loadPbQuick() {
  const entries = await api("GET", "/api/config/list?conf=playerbots.conf");
  const byKey = {};
  for (const e of entries) byKey[e.key] = e;
  const rows = [];
  for (const q of PB_QUICK_KEYS) {
    const e = byKey[q.key];
    if (!e) continue;
    pbQuickData[q.key] = e.overridden ? e.user : e.value;
    const val = e.overridden ? e.user : e.value;
    const isOn = val === "1" || val === "true" || val === "yes";
    const ctrl = q.type === "bool"
      ? `<label class="switch"><input type="checkbox" data-qk="${esc(q.key)}" ${isOn ? "checked" : ""}><span class="track"></span></label>`
      : `<input type="number" data-qk="${esc(q.key)}" value="${esc(val)}" ${q.min !== undefined ? `min="${q.min}"` : ""} ${q.max !== undefined ? `max="${q.max}"` : ""}>`;
    rows.push(`<div class="cfg-item">
      <div class="cfg-head"><span class="cfg-cn">${esc(q.label)}</span>
        <span class="badge">默认 ${esc(e.upstream)}</span>
        ${e.overridden ? `<span class="badge overridden">已自定义</span>` : ""}
        <span class="badge restart">需重启生效</span></div>
      <div class="muted">${esc(q.desc)}</div>
      <div class="cfg-row">${ctrl}</div>
    </div>`);
  }
  $("#pb-quick").innerHTML = rows.join("") || `<p class="muted">配置加载失败</p>`;
}

$("#pb-quick-save").onclick = async () => {
  const btn = $("#pb-quick-save");
  btn.disabled = true;
  try {
    let n = 0;
    for (const q of PB_QUICK_KEYS) {
      const el = $(`#pb-quick [data-qk="${CSS.escape(q.key)}"]`);
      if (!el) continue;
      const v = q.type === "bool" ? (el.checked ? "1" : "0") : el.value.trim();
      if (v === (pbQuickData[q.key] ?? "")) continue;
      await api("POST", "/api/config/set", { conf: "playerbots.conf", key: q.key, value: v });
      n++;
    }
    toast(n ? `已保存 ${n} 项，重启 WorldServer 后生效` : "没有需要保存的修改", n ? "ok" : "warn");
    loadPbQuick();
  } catch (e) { toast(e.message, "err"); }
  btn.disabled = false;
};

async function loadPbProfiles() {
  const ps = await api("GET", "/api/playerbot/profiles");
  const loadTag = { "natural": "低耗", "solo-companion": "低-中", "dungeon-first": "中-高", "pvp-active": "中-高", "low-perf": "最低" };
  $("#pb-profiles").innerHTML = ps.map(p => {
    const entries = Object.entries(p.changes || {})
      .filter(([k, e]) => e[2] !== e[1]);
    const diffText = entries.map(([k, e]) =>
      `${k}: ${e[1] || "（默认 " + e[0] + "）"} → ${e[2]}`).join("\n");
    return `<div class="profile-card">
      <div class="profile-head">
        <div><b>${esc(p.label)}</b> <span class="profile-tag">负载：${esc(loadTag[p.name] || "中")}</span>
          <div class="muted">${esc(p.description)}</div></div>
        <button class="primary" data-profile="${esc(p.name)}">应用模板</button>
      </div>
      <details ${entries.length ? "" : "open"} style="margin-top:8px">
        <summary>${entries.length ? `查看全部变更（${entries.length} 项）` : "与当前配置一致，无变更"}</summary>
        <div class="diff">${esc(diffText || "无")}</div>
      </details>
    </div>`;
  }).join("");
  $$("#pb-profiles [data-profile]").forEach(b => b.onclick = async () => {
    const name = b.dataset.profile;
    if (!(await confirmBox("应用配置模板",
      `将把「${name}」模板的全部参数写入你的自定义配置（覆盖同名项），重启 WorldServer 后生效。点击模板卡片里的「查看全部变更」可核对每一项。确认应用？`,
      "确认应用", false))) return;
    b.disabled = true;
    try {
      const r = await api("POST", "/api/playerbot/profile", { name });
      const n = Object.keys(r.changes || {}).length;
      toast(`模板已应用（${n} 项变更），重启 WorldServer 后生效`, "ok");
      loadPlayerbot();
    } catch (e) { toast(e.message, "err"); b.disabled = false; }
  });
}

// ---------- 账号 ----------
const GM_DESC = {
  0: "普通玩家", 1: "协理员（基础管理）", 2: "游戏管理员", 3: "开发者（全部命令）",
};

async function loadAccounts() {
  const q = $("#acc-search").value || "";
  let list;
  try { list = await api("GET", "/api/accounts?q=" + encodeURIComponent(q)); }
  catch (e) { toast(e.message, "err"); return; }
  $("#acc-table tbody").innerHTML = (list || []).map(a => `<tr>
    <td>${a.id}</td>
    <td><b>${esc(a.username)}</b></td>
    <td>${a.gmlevel ? pill("GM " + a.gmlevel, "warn") : `<span class="muted">玩家</span>`}</td>
    <td>${a.banned ? pill("已封禁", "bad") : a.online ? pill("在线", "ok") : pill("离线", "idle")}</td>
    <td class="muted">${esc(a.last_login || "从未登录")}</td>
    <td class="btns">
      <button data-u="${esc(a.username)}" data-act="acc-pass">改密</button>
      <button data-u="${esc(a.username)}" data-gm="${a.gmlevel}" data-act="acc-gm">GM 等级</button>
      <button data-u="${esc(a.username)}" data-act="acc-ban">封禁</button>
      <button data-u="${esc(a.username)}" data-b="${a.banned ? 1 : 0}" data-act="acc-unban">解封</button>
      <button data-id="${a.id}" data-u="${esc(a.username)}" data-act="acc-chars">角色</button>
    </td></tr>`).join("") || `<tr><td colspan="6" class="muted" style="padding:16px">没有匹配的账号</td></tr>`;

  $$("#acc-table [data-act]").forEach(b => b.onclick = () => accountAction(b.dataset.act, b.dataset.u, b.dataset.id, b.dataset.gm, b.dataset.b));
}
$("#acc-search-btn").onclick = loadAccounts;
$("#acc-search").addEventListener("keydown", e => { if (e.key === "Enter") loadAccounts(); });

async function accountAction(act, u, id, gm, banned) {
  if (act === "acc-pass") {
    modal.open({
      title: `修改密码 — ${u}`,
      bodyHTML: `
        <label>新密码（6-32 位）<input type="password" id="f-pw1" autocomplete="new-password"></label>
        <label>确认新密码<input type="password" id="f-pw2" autocomplete="new-password"></label>
        <div class="error" id="f-err"></div>`,
      actions: [
        { label: "取消" },
        { label: "确认修改", cls: "primary", keepOpen: true, onClick: async (close) => {
          const p1 = $("#f-pw1").value, p2 = $("#f-pw2").value;
          if (p1 !== p2) { $("#f-err").textContent = "两次输入不一致"; return false; }
          if (p1.length < 6 || p1.length > 32) { $("#f-err").textContent = "密码长度需在 6-32 位之间"; return false; }
          try { await api("POST", "/api/accounts/password", { username: u, password: p1 }); toast("密码已修改", "ok"); close(); }
          catch (e) { $("#f-err").textContent = e.message; return false; }
        } },
      ],
    });
  } else if (act === "acc-gm") {
    modal.open({
      title: `GM 等级 — ${u}`,
      bodyHTML: `
        <p class="muted">当前等级：${gm}（${GM_DESC[gm] || "普通玩家"}）</p>
        <label>新 GM 等级
          <select id="f-gm">
            ${[0, 1, 2, 3].map(l => `<option value="${l}">${l} — ${GM_DESC[l]}</option>`).join("")}
          </select></label>
        <div class="error" id="f-err"></div>`,
      actions: [
        { label: "取消" },
        { label: "保存", cls: "primary", keepOpen: true, onClick: async (close) => {
          try { await api("POST", "/api/accounts/gmlevel", { username: u, gmlevel: parseInt($("#f-gm").value, 10) }); toast("GM 等级已更新", "ok"); close(); loadAccounts(); }
          catch (e) { $("#f-err").textContent = e.message; return false; }
        } },
      ],
    });
  } else if (act === "acc-ban") {
    modal.open({
      title: `封禁账号 — ${u}`,
      bodyHTML: `
        <div class="danger-note">封禁后该账号立即无法登录游戏。操作会记录到审计日志。</div>
        <label>封禁时长
          <select id="f-dur">
            <option value="24h">1 天</option>
            <option value="168h">7 天</option>
            <option value="720h">30 天</option>
            <option value="0">永久</option>
          </select></label>
        <label>封禁原因（必填）<textarea id="f-reason" rows="2" placeholder="如：违规行为"></textarea></label>
        <div class="error" id="f-err"></div>`,
      actions: [
        { label: "取消" },
        { label: "确认封禁", cls: "danger", keepOpen: true, onClick: async (close) => {
          const reason = $("#f-reason").value.trim();
          if (!reason) { $("#f-err").textContent = "请填写封禁原因"; return false; }
          try { await api("POST", "/api/accounts/ban", { username: u, reason, duration: $("#f-dur").value }); toast("已封禁", "ok"); close(); loadAccounts(); }
          catch (e) { $("#f-err").textContent = e.message; return false; }
        } },
      ],
    });
  } else if (act === "acc-unban") {
    if (banned !== "1") { toast("该账号没有生效中的封禁", "warn"); return; }
    if (!(await confirmBox("解除封禁", `确认解除 ${u} 的封禁？`, "确认解封", false))) return;
    try { await api("POST", "/api/accounts/unban", { username: u }); toast("已解封", "ok"); loadAccounts(); }
    catch (e) { toast(e.message, "err"); }
  } else if (act === "acc-chars") {
    try {
      const chars = await api("GET", "/api/accounts/characters?id=" + id);
      modal.open({
        title: `${u} 的角色（${(chars || []).length}）`,
        bodyHTML: (chars || []).length
          ? `<table class="tbl"><thead><tr><th>名字</th><th>等级</th><th>状态</th></tr></thead><tbody>
             ${chars.map(c => `<tr><td>${esc(c.name)}</td><td>Lv ${c.level}</td>
             <td>${c.online ? pill("在线", "ok") : pill("离线", "idle")}</td></tr>`).join("")}
             </tbody></table>`
          : `<p class="muted">该账号还没有角色</p>`,
        actions: [{ label: "关闭" }],
      });
    } catch (e) { toast(e.message, "err"); }
  }
}

$("#acc-new").onclick = () => {
  modal.open({
    title: "创建账号",
    bodyHTML: `
      <label>账号名（3-16 位字母数字）<input id="f-user" autocomplete="off"></label>
      <label>密码（6-32 位）<input type="password" id="f-pw1" autocomplete="new-password"></label>
      <label>确认密码<input type="password" id="f-pw2" autocomplete="new-password"></label>
      <label>GM 等级
        <select id="f-gm">
          ${[0, 1, 2, 3].map(l => `<option value="${l}">${l} — ${GM_DESC[l]}</option>`).join("")}
        </select></label>
      <div class="error" id="f-err"></div>`,
    actions: [
      { label: "取消" },
      { label: "创建", cls: "primary", keepOpen: true, onClick: async (close) => {
        const u = $("#f-user").value.trim();
        const p1 = $("#f-pw1").value, p2 = $("#f-pw2").value;
        if (!/^[A-Za-z0-9]{3,16}$/.test(u)) { $("#f-err").textContent = "账号名需为 3-16 位字母数字"; return false; }
        if (p1 !== p2) { $("#f-err").textContent = "两次输入不一致"; return false; }
        if (p1.length < 6 || p1.length > 32) { $("#f-err").textContent = "密码长度需在 6-32 位之间"; return false; }
        try {
          await api("POST", "/api/accounts/create", { username: u, password: p1, gmlevel: parseInt($("#f-gm").value, 10) });
          toast(`账号 ${u} 已创建，可直接用客户端登录`, "ok");
          close(); loadAccounts();
        } catch (e) { $("#f-err").textContent = e.message; return false; }
      } },
    ],
  });
};

// ---------- 配置中心 ----------
let cfgConf = "worldserver.conf";
let cfgMode = "common";      // common | advanced | raw
let cfgSchema = null;        // zhCN 覆盖层
let cfgEntries = [];         // 当前 conf 的 Effective 列表
let cfgQuickData = {};

// 常用设置（服务器的玩家侧参数 + 机器人核心参数）
const COMMON_KEYS = {
  "worldserver.conf": [
    { key: "MaxPlayerLevel", label: "角色最高等级", hint: "默认 80（巫妖王之怒满级）。" },
    { key: "StartPlayerLevel", label: "新角色初始等级", hint: "想让小号直接从 55 级开始可改 55。" },
    { key: "Rate.XP.Kill", label: "击杀经验倍率", hint: "1 为原始倍率，10 即十倍经验。" },
    { key: "Rate.XP.Quest", label: "任务经验倍率", hint: "" },
    { key: "Rate.XP.Explore", label: "探索经验倍率", hint: "" },
    { key: "Rate.Drop.Item.Rare", label: "精良物品掉率倍率", hint: "蓝色装备掉落倍率。" },
    { key: "Rate.Drop.Item.Epic", label: "史诗物品掉率倍率", hint: "紫色装备掉落倍率。" },
    { key: "Rate.Drop.Money", label: "金币掉落倍率", hint: "" },
  ],
  "authserver.conf": [
    { key: "RealmServerPort", label: "登录服务器端口", hint: "默认 3724，改后需同步修改客户端与防火墙。" },
  ],
  "playerbots.conf": PB_QUICK_KEYS.map(q => ({ key: q.key, label: q.label, hint: q.desc })),
};

async function loadConfig() {
  $$("#cfg-tabs button").forEach(b => b.classList.toggle("active",
    b.dataset.conf ? (cfgMode !== "raw" && b.dataset.conf === cfgConf) : b.dataset.mode === cfgMode));
  $("#cfg-gui").style.display = cfgMode === "raw" ? "none" : "";
  $("#cfg-raw").style.display = cfgMode === "raw" ? "" : "none";
  if (!cfgSchema) {
    try { cfgSchema = await api("GET", "/api/config/schema"); } catch { cfgSchema = {}; }
  }
  if (cfgMode === "raw") return loadConfigRaw();

  // 拉取当前 conf 的全量 Effective（common/advanced 共用）
  try { cfgEntries = await api("GET", "/api/config/list?conf=" + cfgConf); }
  catch (e) { toast(e.message, "err"); return; }

  $$("#cfg-mode button").forEach(b => b.classList.toggle("active", b.dataset.mode === cfgMode));
  $("#cfg-common").style.display = cfgMode === "common" ? "" : "none";
  $("#cfg-advanced").style.display = cfgMode === "advanced" ? "" : "none";
  if (cfgMode === "common") renderCfgCommon();
  else renderCfgAdvanced();
}

$$("#cfg-tabs button").forEach(b => b.onclick = () => {
  if (b.dataset.conf) { cfgConf = b.dataset.conf; cfgMode = cfgMode === "raw" ? "advanced" : cfgMode; }
  else cfgMode = "raw";
  loadConfig();
});
$$("#cfg-mode button").forEach(b => b.onclick = () => { cfgMode = b.dataset.mode; loadConfig(); });

function zhOf(key) { return (cfgSchema && cfgSchema[key]) || {}; }

function cfgControl(e, q) {
  const val = e.overridden ? e.user : e.value;
  // 开关只用于显式声明的布尔项（q.type=bool）或上游就是 true/false 的项；
  // 默认值为 0/1 的数字项（倍率、等级）保持数字输入，不能误当开关。
  const wantBool = (q && q.type === "bool") || e.type === "bool";
  if (wantBool) {
    const on = ["1", "true", "yes"].includes(val.toLowerCase());
    return `<label class="switch"><input type="checkbox" data-ck="${esc(e.key)}" ${on ? "checked" : ""}><span class="track"></span></label>`;
  }
  if (e.type === "int" || e.type === "float") {
    return `<input type="number" step="any" data-ck="${esc(e.key)}" value="${esc(val)}">`;
  }
  return `<input type="text" data-ck="${esc(e.key)}" value="${esc(val)}">`;
}

function renderCfgCommon() {
  const byKey = {};
  for (const e of cfgEntries) byKey[e.key] = e;
  const defs = COMMON_KEYS[cfgConf] || [];
  const rows = [];
  for (const q of defs) {
    const e = byKey[q.key];
    if (!e) continue; // 上游没有该键（版本变化）时跳过
    cfgQuickData[q.key] = e.overridden ? e.user : e.value;
    const zh = zhOf(q.key);
    const desc = q.hint || zh.desc || (e.comment || "").split("\n")[0];
    rows.push(`<div class="cfg-item">
      <div class="cfg-head"><span class="cfg-cn">${esc(q.label)}</span>
        <span class="badge">默认 ${esc(e.upstream)}</span>
        ${e.recommended && e.recommended !== e.upstream ? `<span class="badge">推荐 ${esc(e.recommended)}</span>` : ""}
        ${e.overridden ? `<span class="badge overridden">已自定义</span>` : ""}
        ${e.restart ? `<span class="badge restart">需重启</span>` : ""}</div>
      ${desc ? `<div class="muted" title="${esc(desc)}">${esc(desc)}</div>` : ""}
      <div class="cfg-row">${cfgControl(e, q)}
        ${e.overridden ? `<button data-reset="${esc(q.key)}">还原默认</button>` : ""}
      </div>
    </div>`);
  }
  $("#cfg-common-list").innerHTML = rows.join("") ||
    `<p class="muted">当前配置文件没有收录的常用项。</p>`;
  bindCfgCommonActions();
}

function bindCfgCommonActions() {
  $$("#cfg-common-list [data-reset]").forEach(b => b.onclick = async () => {
    try {
      await api("POST", "/api/config/reset", { conf: cfgConf, key: b.dataset.reset });
      toast("已还原该项为默认值", "ok");
      loadConfig();
    } catch (e) { toast(e.message, "err"); }
  });
  // 保存按钮（每项一个，避免误改整批）
  $$("#cfg-common-list [data-ck]").forEach(el => {
    el.addEventListener("change", async () => {
      const key = el.dataset.ck;
      const v = el.type === "checkbox" ? (el.checked ? "1" : "0") : el.value.trim();
      if (v === (cfgQuickData[key] ?? "")) return;
      el.disabled = true;
      try {
        const r = await api("POST", "/api/config/set", { conf: cfgConf, key, value: v });
        toast("已保存。" + (r.restart_hint || ""), "ok");
        loadConfig();
      } catch (e) { toast(e.message, "err"); el.disabled = false; }
    });
  });
}

function renderCfgAdvanced() {
  const q = ($("#cfg-search").value || "").toLowerCase();
  const sec = $("#cfg-section").value;
  const sections = [...new Set(cfgEntries.map(e => e.section || "General"))];
  const sel = $("#cfg-section");
  sel.innerHTML = `<option value="">全部分组</option>` +
    sections.map(s => `<option ${s === sec ? "selected" : ""}>${esc(s)}</option>`).join("");

  const filtered = cfgEntries.filter(e => {
    if (sec && (e.section || "General") !== sec) return false;
    if (!q) return true;
    const zh = zhOf(e.key);
    return e.key.toLowerCase().includes(q) ||
      (zh.name || "").toLowerCase().includes(q) ||
      (zh.desc || "").toLowerCase().includes(q) ||
      (e.comment || "").toLowerCase().includes(q);
  });

  $("#cfg-list").innerHTML = filtered.map(e => {
    const zh = zhOf(e.key);
    const cn = zh.name ? esc(zh.name) : "";
    const desc = zh.desc ? esc(zh.desc) : esc((e.comment || "").split("\n")[0]);
    const layers = `上游默认: <b>${esc(e.upstream || "")}</b>` +
      (e.recommended ? ` · 推荐: <b>${esc(e.recommended)}</b>` : "") +
      (e.overridden ? ` · 用户值: <b>${esc(e.user)}</b>` : "") +
      ` · 生效: <b>${esc(e.value)}</b>`;
    return `<div class="cfg-item" data-key="${esc(e.key)}">
      <div class="cfg-head">
        ${cn ? `<span class="cfg-cn">${cn}</span>` : ""}
        <span class="cfg-key">${esc(e.key)}</span>
        ${e.overridden ? `<span class="badge overridden">已自定义</span>` : ""}
        ${e.restart ? `<span class="badge restart">需重启</span>` : ""}
      </div>
      ${desc ? `<div class="muted">${desc}</div>` : ""}
      <div class="cfg-layers">${layers}</div>
      <div class="cfg-row">
        <input type="text" data-set="${esc(e.key)}" value="${esc(e.overridden ? e.user : e.value)}">
        <button class="primary" data-save="${esc(e.key)}">保存</button>
        ${e.overridden ? `<button data-del="${esc(e.key)}">还原</button>` : ""}
      </div>
    </div>`;
  }).join("") || `<div class="muted" style="padding:12px 0">无匹配配置项</div>`;

  $$("#cfg-list [data-save]").forEach(b => b.onclick = async () => {
    const key = b.dataset.save;
    const input = $(`#cfg-list input[data-set="${key}"]`);
    try {
      const r = await api("POST", "/api/config/set", { conf: cfgConf, key, value: input.value });
      toast("已保存。" + (r.restart_hint || ""), "ok");
      loadConfig();
    } catch (e) { toast(e.message, "err"); }
  });
  $$("#cfg-list [data-del]").forEach(b => b.onclick = async () => {
    try {
      await api("POST", "/api/config/reset", { conf: cfgConf, key: b.dataset.del });
      toast("已还原默认", "ok");
      loadConfig();
    } catch (e) { toast(e.message, "err"); }
  });
}

$("#cfg-search").addEventListener("input", debounce(() => { if (cfgMode === "advanced") renderCfgAdvanced(); }, 300));
$("#cfg-section").addEventListener("change", () => { if (cfgMode === "advanced") renderCfgAdvanced(); });

async function loadConfigRaw() {
  const raw = await api("GET", "/api/config/raw?conf=" + cfgConf);
  $("#cfg-raw-editor").value = raw.user || "";
  $("#cfg-raw-run").textContent = raw.run || "";
}

$("#cfg-raw-save").onclick = async () => {
  try {
    await api("POST", "/api/config/raw", { conf: cfgConf, content: $("#cfg-raw-editor").value });
    toast("已保存并重新生成运行配置", "ok");
    loadConfigRaw();
  } catch (e) { toast(e.message, "err"); }
};
$("#cfg-raw-reset").onclick = async () => {
  if (!(await confirmBox("恢复默认", "将清空该文件的全部用户自定义值（数据库与账号数据不受影响）。确认继续？"))) return;
  try {
    await api("POST", "/api/config/reset", { conf: cfgConf });
    toast("已恢复默认", "ok");
    loadConfigRaw();
  } catch (e) { toast(e.message, "err"); }
};

// ---------- 数据 ----------
async function loadData() {
  const st = await api("GET", "/api/data/status");
  const ins = st.installed || {};
  $("#data-status").innerHTML = `
    <b>要求版本</b><span>${esc(st.required)} ${esc(st.label || "")}</span>
    <b>已安装版本</b><span>${esc(ins.version || "未安装")}</span>
    <b>状态</b><span>${st.missing && st.missing.length === 0 && ins.installed
      ? pill("完整可用", "ok") : pill("需要下载/补全", "warn")}</span>
    <b>缺失目录</b><span>${st.missing && st.missing.length ? esc(st.missing.join(", ")) : "无"}</span>`;
  const p = st.progress || {};
  $("#data-cancel").disabled = !p.active;
  if (p.active) {
    const pct = p.bytes_total ? Math.round(p.bytes_done * 100 / p.bytes_total) : 0;
    $("#data-progress").style.width = pct + "%";
    $("#data-progress-text").textContent =
      `${p.source}：${fmtBytes(p.bytes_done)} / ${fmtBytes(p.bytes_total)}（${pct}%，${fmtBytes(Math.round(p.speed_bps))}/s）`;
  } else if (p.error) {
    $("#data-progress").style.width = "0%";
    $("#data-progress-text").textContent = "上次失败: " + p.error;
  } else {
    $("#data-progress").style.width = "0%";
    $("#data-progress-text").textContent = "";
  }
  const cands = st.manual_candidates || [];
  $("#data-manual-candidates").innerHTML = cands.length
    ? `发现待导入文件：${cands.map(c => `<span class="mono">${esc(c.split("/").pop().split("\\").pop())}</span>`).join("、")}`
    : `（当前没有检测到 Data.zip）`;
}

$("#data-download").onclick = async () => {
  try { await api("POST", "/api/data/download", {}); toast("下载任务已开始", "ok"); pollData(); }
  catch (e) { toast(e.message, "err"); }
};
$("#data-cancel").onclick = async () => { await api("POST", "/api/data/cancel", {}); toast("已请求取消", "warn"); };
$("#data-scan").onclick = async () => {
  try {
    const r = await api("POST", "/api/data/scan", {});
    toast(`已开始校验并导入 ${r.file || "Data.zip"}`, "ok");
    pollData();
  } catch (e) { toast(e.message, "err"); }
};
$("#data-import").onclick = async () => {
  const f = $("#data-file").files[0];
  if (!f) { toast("请先选择 Data.zip 文件", "warn"); return; }
  const fd = new FormData();
  fd.append("file", f);
  try { await api("POST", "/api/data/import", fd, true); toast("上传完成，开始校验导入", "ok"); pollData(); }
  catch (e) { toast(e.message, "err"); }
};

let dataTimer = null;
function pollData() {
  clearInterval(dataTimer);
  dataTimer = setInterval(async () => {
    if (CURRENT !== "#/data") return clearInterval(dataTimer);
    loadData();
  }, 2000);
}

// ---------- 日志 ----------
let logTimer = null;

async function loadLogs() {
  const files = await api("GET", "/api/logs/list");
  const sel = $("#log-name");
  const cur = sel.value;
  sel.innerHTML = files.map(f => `<option value="${esc(f.name)}">${esc(f.name)}（${fmtBytes(parseInt(f.size))}）</option>`).join("");
  if (cur && files.some(f => f.name === cur)) sel.value = cur;
  await refreshLogLines();
  scheduleLogAuto();
}

async function refreshLogLines() {
  const name = $("#log-name").value;
  if (!name) return;
  const keyword = $("#log-filter").value.trim();
  const lines = await api("GET", "/api/logs/tail?name=" + encodeURIComponent(name)
    + "&n=400&filter=" + encodeURIComponent(keyword));
  let out = lines || [];
  const level = $("#log-level").value;
  if (level === "ERROR") out = out.filter(l => /ERROR|WARN|\[ERR|\[WARN/i.test(l));
  else if (level === "ERROR_ONLY") out = out.filter(l => /ERROR|\[ERR/i.test(l));
  const el = $("#log-view");
  el.textContent = out.join("\n") || "（没有匹配的日志行）";
  el.scrollTop = el.scrollHeight;
}

function scheduleLogAuto() {
  clearInterval(logTimer);
  if (!$("#log-auto").checked) return;
  logTimer = setInterval(() => {
    if (CURRENT !== "#/logs") return clearInterval(logTimer);
    if (!$("#log-auto").checked) return clearInterval(logTimer);
    refreshLogLines().catch(() => {});
  }, 4000);
}
$("#log-auto").addEventListener("change", scheduleLogAuto);
$("#log-refresh").onclick = refreshLogLines;
$("#log-filter").addEventListener("keydown", e => { if (e.key === "Enter") refreshLogLines(); });
$("#log-level").addEventListener("change", refreshLogLines);
$("#log-name").addEventListener("change", refreshLogLines);
$("#log-export").onclick = () => authDownload("/api/logs/export", "gswxy-diagnostics.txt");

// ---------- 备份 ----------
async function loadBackup() {
  const [list, status] = await Promise.all([
    api("GET", "/api/backup/list"),
    api("GET", "/api/backup/status"),
  ]);
  const job = status || {};
  const ov = await api("GET", "/api/overview").catch(() => null);
  const bkState = (ov && ov.backup) || {};
  $("#bk-auto").checked = !!bkState.auto_enabled;

  let jobHtml;
  if (job.active) {
    jobHtml = `<b>任务</b><span>${job.kind === "restore" ? "恢复" : "备份"}进行中 — ${esc(job.phase || "")}</span>`;
    setTimeout(() => { if (CURRENT === "#/backup") loadBackup(); }, 2500);
  } else if (job.error) {
    jobHtml = `<b>上次结果</b><span class="bad">失败：${esc(job.error)}</span>
               <b>阶段</b><span>${esc(job.phase || "")}</span>`;
  } else {
    jobHtml = `<b>上次结果</b><span>${job.phase === "完成" ? pill("成功", "ok") : `<span class="muted">空闲</span>`}</span>`;
  }
  $("#bk-status").innerHTML = jobHtml;

  $("#bk-table tbody").innerHTML = (list || []).map(row => `<tr>
    <td class="mono">${esc(row.file)}</td>
    <td class="muted">${row.at ? esc(row.at.replace("T", " ").slice(0, 19)) : "—"}</td>
    <td>${fmtBytes(row.size)}</td>
    <td><button class="danger" data-restore="${esc(row.file)}">恢复</button></td>
  </tr>`).join("") || `<tr><td colspan="4" class="muted" style="padding:16px">暂无备份</td></tr>`;

  $$("#bk-table [data-restore]").forEach(b => b.onclick = async () => {
    const file = b.dataset.restore;
    if (!(await confirmBox("恢复备份",
      `<div class="danger-note">恢复会把数据库整体回滚到该备份时间点：之后创建的账号、角色进度与机器人数据都会丢失（恢复前会自动再做一次当前状态备份，可再恢复回来）。游戏服务器会短暂停止再自动拉起。</div>
       <p>确认恢复 <b class="mono">${esc(file)}</b>？</p>`,
      "确认恢复"))) return;
    try {
      await api("POST", "/api/backup/restore", { file });
      toast("恢复任务已开始，进度见上方状态", "ok");
      setTimeout(loadBackup, 1000);
    } catch (e) { toast(e.message, "err"); }
  });
}

$("#bk-create").onclick = async () => {
  const btn = $("#bk-create");
  btn.disabled = true;
  try { await api("POST", "/api/backup/create", {}); toast("备份完成", "ok"); }
  catch (e) { toast(e.message, "err"); }
  btn.disabled = false;
  loadBackup();
};
$("#bk-auto").addEventListener("change", async () => {
  try {
    await api("POST", "/api/backup/auto", { enabled: $("#bk-auto").checked });
    toast($("#bk-auto").checked ? "每日自动备份已开启" : "自动备份已关闭", "ok");
  } catch (e) { toast(e.message, "err"); loadBackup(); }
});

// ---------- 版本与更新 ----------
async function loadVersion() {
  const v = await api("GET", "/api/version");
  const b = v.build || {};
  const cd = v.client_data || {};
  $("#ver-view").innerHTML = `
    <b>产品</b><span>艾泽旅伴 · GSWXY Realm ${esc(b.version)}（${esc(b.channel)}）</span>
    <b>AzerothCore</b><span class="mono">${esc((b.core || {}).commit || "—").slice(0, 12)} @ ${esc((b.core || {}).branch || "")}</span>
    <b>mod-playerbots</b><span class="mono">${esc((b.playerbots || {}).commit || "—").slice(0, 12)} @ ${esc((b.playerbots || {}).branch || "")}</span>
    <b>客户端数据</b><span>${esc(cd.version || "未安装")}（要求与 Core 锁定版本一致）</span>
    <b>中文数据</b><span>zhCN ${esc((b.locale || {}).version || "—")}</span>
    <b>数据库运行时</b><span>MySQL ${esc((b.database_runtime || {}).version || "")}（仅本机监听）</span>
    <b>构建信息</b><span>Run ${esc((b.build || {}).run_id || "—")} · ${esc((b.build || {}).built_at || "—")}</span>`;
}

$("#upd-check").onclick = async () => {
  const btn = $("#upd-check");
  btn.disabled = true;
  $("#upd-result").innerHTML = `<span class="muted">正在查询 GitHub Releases…</span>`;
  try {
    const r = await api("GET", "/api/update/check?channel=" + $("#upd-channel").value);
    if (r.error) {
      $("#upd-result").innerHTML = `<div class="warn">检查失败：${esc(r.error)}（不影响服务器运行，可稍后再试）</div>`;
    } else if (r.available) {
      $("#upd-result").innerHTML = `
        <div class="card" style="margin:0">
          <div><b>发现新版本：${esc(r.latest)}</b> ${pill("可更新", "ok")}</div>
          <div class="muted">发布于 ${esc((r.published_at || "").replace("T", " ").slice(0, 19))}</div>
          <div class="btns" style="margin-top:8px">
            ${r.fpk ? `<a class="btn primary" href="${esc(r.fpk.url)}" target="_blank" rel="noopener">下载 FPK（${fmtBytes(r.fpk.size)}）</a>` : ""}
            ${r.sha256 ? `<a class="btn" href="${esc(r.sha256.url)}" target="_blank" rel="noopener">SHA-256 校验文件</a>` : ""}
            <a class="btn" href="${esc(r.url)}" target="_blank" rel="noopener">查看发布说明</a>
          </div>
          ${r.latest_note ? `<details style="margin-top:8px"><summary>更新内容</summary><pre class="pre" style="max-height:240px">${esc(r.latest_note)}</pre></details>` : ""}
          <p class="muted" style="margin-top:8px">下载完成后请在 fnOS 应用中心手动安装升级；账号、角色、机器人与配置会全部保留。升级前建议先在「备份」页创建备份。</p>
        </div>`;
    } else {
      $("#upd-result").innerHTML = `<div class="ok">已是最新版本（${esc(r.latest)}）</div>`;
    }
  } catch (e) {
    $("#upd-result").innerHTML = `<div class="warn">检查失败：${esc(e.message)}</div>`;
  }
  btn.disabled = false;
};

// ---------- 生命周期按钮 ----------
async function doStart() {
  toast("正在启动（首次启动 WorldServer 需加载数据，可能需要几分钟）…", "warn", 6000);
  try { await api("POST", "/api/start", {}); toast("启动指令已执行", "ok"); }
  catch (e) { toast(e.message, "err"); }
  render();
}
async function doStop() {
  if (!(await confirmBox("安全停止", "将按依赖顺序停止 WorldServer、AuthServer 与数据库，在线玩家会掉线。确认停止？", "确认停止"))) return;
  toast("正在停止…", "warn");
  try { await api("POST", "/api/stop", {}); toast("已全部停止", "ok"); }
  catch (e) { toast(e.message, "err"); }
  render();
}
$$("[data-act=start]").forEach(b => b.onclick = doStart);
$$("[data-act=stop]").forEach(b => b.onclick = doStop);

// ---------- 主题 ----------
const themeBtn = $("#theme-toggle");
function applyTheme(t) {
  document.documentElement.classList.toggle("dark", t === "dark");
  themeBtn.textContent = t === "dark" ? "☀️ 浅色模式" : "🌙 深色模式";
  localStorage.setItem("gsrm_theme", t);
}
themeBtn.onclick = () => applyTheme(document.documentElement.classList.contains("dark") ? "light" : "dark");
applyTheme(localStorage.getItem("gsrm_theme") || "light");

// ---------- 启动 ----------
$("#login-btn").onclick = doLogin;
$("#login-password").addEventListener("keydown", e => { if (e.key === "Enter") doLogin(); });
$("#login-password2").addEventListener("keydown", e => { if (e.key === "Enter") doLogin(); });
render();
setInterval(async () => {
  if (CURRENT === "#/overview") { try { await loadOverview(); } catch { /* 保持当前显示 */ } }
}, 15000);
