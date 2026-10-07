/* GSWXY Realm WebUI — 中文单页应用 */
"use strict";

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => [...document.querySelectorAll(sel)];

let TOKEN = localStorage.getItem("gsrm_token") || "";
let CSRF = localStorage.getItem("gsrm_csrf") || "";
let CURRENT = "#/overview";

// ---------- API ----------
async function api(method, path, body, isForm) {
  const headers = {};
  if (TOKEN) {
    // Bearer 令牌鉴权：不依赖 cookie（fnOS 桌面是跨站 iframe，cookie
    // 会被浏览器扣下；空值 HttpOnly cookie 也会挡掉 JS 补设）
    headers["Authorization"] = "Bearer " + TOKEN;
    headers["X-CSRF-Token"] = CSRF;
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
    location.hash = "#/login";
  }
  if (!res.ok) throw new Error(data.error || res.status);
  return data;
}

const fmtBytes = (n) => {
  if (n === undefined || n === null) return "—";
  if (n > 1 << 30) return (n / (1 << 30)).toFixed(2) + " GB";
  if (n > 1 << 20) return (n / (1 << 20)).toFixed(1) + " MB";
  if (n > 1024) return (n / 1024).toFixed(0) + " KB";
  return n + " B";
};

function pill(text, cls) { return `<span class="pill ${cls}">${text}</span>`; }

function statusPill(st) {
  switch (st) {
    case "running": return pill("运行中", "ok");
    case "stopped": return pill("已停止", "idle");
    case "starting": return pill("启动中", "warn");
    case "stopping": return pill("停止中", "warn");
    case "backoff": return pill("等待重启", "warn");
    case "failed": return pill("故障（需手动启动）", "bad");
    default: return pill(st || "未知", "idle");
  }
}

const stepNames = {
  env_check: "运行环境检查",
  db_init: "数据库初始化",
  db_import: "数据库导入（基础 + 更新 + 模块）",
  playerbot_init: "Playerbot 数据初始化",
  locale_import: "GSWXY 中文数据导入",
  client_data: "客户端数据",
  realm: "Realm 与最终配置",
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
  $("#login-title").textContent = initialized ? "登录管理" : "首次设置";
  $("#login-desc").textContent = initialized
    ? "请输入管理员密码进入 GSWXY Realm 管理界面。"
    : "首次使用请设置管理员密码（至少 8 位）。此密码用于登录本管理界面。";
  $("#login-password2-wrap").style.display = initialized ? "none" : "block";
}

async function doLogin() {
  const pw = $("#login-password").value;
  const pw2 = $("#login-password2").value;
  $("#login-error").textContent = "";
  try {
    const s = await api("GET", "/api/session");
    const ep = s.initialized ? "/api/login" : "/api/setup/password";
    if (!s.initialized && pw !== pw2) throw new Error("两次输入的密码不一致");
    const r = await api("POST", ep, { password: pw });
    TOKEN = r.token; CSRF = r.csrf;
    localStorage.setItem("gsrm_token", TOKEN);
    localStorage.setItem("gsrm_csrf", CSRF);
    location.hash = "#/overview";
  } catch (e) {
    $("#login-error").textContent = e.message;
  }
}

// ---------- 概览 ----------
async function loadOverview() {
  const ov = await api("GET", "/api/overview");
  const pr = ov.processes || {};
  $("#ov-world").innerHTML = statusPill(pr["worldserver"]);
  $("#ov-auth").innerHTML = statusPill(pr["authserver"]);
  const dbOk = pr["mysqld"] === "running";
  $("#ov-db").innerHTML = dbOk ? pill("正常", "ok") : statusPill(pr["mysqld"]);

  const pop = ov.population;
  if (pop) {
    $("#ov-real").textContent = pop.real_players;
    $("#ov-bots").textContent = pop.bots;
    $("#ov-total").textContent = pop.total;
  } else {
    $("#ov-real").textContent = $("#ov-bots").textContent = $("#ov-total").textContent = "—";
  }

  // setup
  const su = ov.setup || {};
  let html = "";
  if (su.initialized) {
    html = pill("初始化完成", "ok");
  } else {
    const order = Object.keys(stepNames);
    const current = su.current || "env_check";
    for (const st of order) {
      const done = su.completed && su.completed[st];
      const cur = st === current && su.in_progress;
      const mark = done ? pill("完成", "ok") : cur ? pill("进行中", "warn") : pill("待执行", "idle");
      html += `<div>${mark} ${stepNames[st]}</div>`;
    }
    if (su.error) html += `<div class="error">${esc(su.error)}</div>`;
  }
  $("#ov-setup").innerHTML = html;
  $("#ov-setup-actions").innerHTML = (!su.initialized && !su.in_progress)
    ? `<button class="primary" id="ov-setup-run">开始初始化</button>` : "";
  const runBtn = $("#ov-setup-run");
  if (runBtn) runBtn.onclick = async () => {
    await api("POST", "/api/setup/start", {});
    loadOverview();
  };

  const v = ov.version || {};
  $("#ov-versions").innerHTML = `
    <b>产品</b><span>${esc(v.product || "GSWXY Realm")} ${esc(v.version || "")}</span>
    <b>Core</b><span class="mono">${esc((v.core || {}).commit || "—").slice(0, 12)}</span>
    <b>Playerbots</b><span class="mono">${esc((v.playerbots || {}).commit || "—").slice(0, 12)}</span>
    <b>客户端数据</b><span>${esc(ov.client_data.version || "未下载")}</span>
    <b>中文数据</b><span>${esc(ov.locale.version || "未导入")}</span>`;
}

function esc(s) {
  return String(s ?? "").replace(/[&<>"]/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
}

// ---------- 服务器 ----------
async function loadServer() {
  const pf = await api("GET", "/api/preflight");
  const tb = $("#pf-table tbody");
  tb.innerHTML = pf.map(c => `<tr>
    <td>${esc(c.name)}</td>
    <td>${c.passed ? pill("通过", "ok") : pill("未通过", "bad")}</td>
    <td class="muted">${esc(c.detail)}</td>
    <td class="muted">${esc(c.fix_hint || "")}</td>
  </tr>`).join("");

  const ov = await api("GET", "/api/overview");
  const pr = ov.processes || {};
  const rows = [];
  for (const name of ["mysqld", "authserver", "worldserver"]) {
    const st = pr[name] || "stopped";
    rows.push(`<tr><td>${name}</td><td>${statusPill(st)}</td>
      <td>${esc(pr[name + ".pid"] || "—")}</td>
      <td><button data-proc="${name}" data-act="proc-restart">重启</button></td></tr>`);
  }
  $("#proc-table tbody").innerHTML = rows.join("");
  $$("#proc-table [data-act=proc-restart]").forEach(b => b.onclick = async () => {
    await api("POST", "/api/restart", { target: b.dataset.proc });
    loadServer();
  });

  const st = await api("GET", "/api/version");
  $("#realm-name").value = (st.build && "") || "";
}

$("#realm-save").onclick = async () => {
  await api("POST", "/api/realm/name", { name: $("#realm-name").value });
  alert("已保存");
};

// ---------- Playerbot ----------
async function loadPlayerbot() {
  const s = await api("GET", "/api/playerbot/summary");
  $("#pb-stats").innerHTML = `
    <div class="stat card"><div class="stat-label">真实玩家在线</div><div class="stat-value">${s.real_players}</div></div>
    <div class="stat card"><div class="stat-label">机器人在线</div><div class="stat-value">${s.bots}</div></div>
    <div class="stat card"><div class="stat-label">总在线</div><div class="stat-value">${s.online_total}</div></div>
    <div class="stat card"><div class="stat-label">联盟</div><div class="stat-value">${s.alliance}</div></div>
    <div class="stat card"><div class="stat-label">部落</div><div class="stat-value">${s.horde}</div></div>
    <div class="stat card"><div class="stat-label">名字池</div><div class="stat-value">${s.name_pool}</div></div>`;

  const classes = { "1": "战士", "2": "圣骑士", "3": "猎人", "4": "潜行者", "5": "牧师", "6": "死亡骑士", "7": "萨满祭司", "8": "法师", "9": "术士", "11": "德鲁伊" };
  let ch = "";
  for (const [k, v] of Object.entries(classes)) {
    const n = s.classes[k] || 0;
    if (n) ch += `<div><b>${v}</b>：${n}</div>`;
  }
  $("#pb-dists").innerHTML = `<div class="kv">${ch}</div>`;

  const ps = await api("GET", "/api/playerbot/profiles");
  $("#pb-profiles").innerHTML = ps.map((p, i) => {
    const diff = Object.entries(p.changes || {})
      .filter(([k, e]) => e[2] !== e[1])
      .map(([k, e]) => `${k}: ${e[1] || "(默认 " + e[0] + ")"} → ${e[2]}`)
      .slice(0, 8).join("\n");
    return `<div class="profile-card">
      <div class="profile-head">
        <div><b>${esc(p.label)}</b> <span class="muted">${esc(p.description)}</span></div>
        <button class="primary" data-profile="${esc(p.name)}" data-pi="${i}">应用</button>
      </div>
      <div class="diff">${esc(diff || "无变更（与当前用户配置一致）")}</div>
    </div>`;
  }).join("");
  $$("#pb-profiles [data-profile]").forEach(b => b.onclick = async () => {
    if (!confirm("确认应用该配置模板？")) return;
    await api("POST", "/api/playerbot/profile", { name: b.dataset.profile });
    alert("已应用，重启 WorldServer 后生效");
    loadPlayerbot();
  });
}

// ---------- 账号 ----------
async function loadAccounts() {
  const q = $("#acc-search").value || "";
  const list = await api("GET", "/api/accounts?q=" + encodeURIComponent(q));
  $("#acc-table tbody").innerHTML = (list || []).map(a => `<tr>
    <td>${a.id}</td><td>${esc(a.username)}</td><td>${a.gmlevel}</td>
    <td>${a.banned ? pill("已封禁", "bad") : a.online ? pill("在线", "ok") : pill("离线", "idle")}</td>
    <td class="muted">${esc(a.last_login || "—")}</td>
    <td class="btns">
      <button data-id="${a.id}" data-u="${esc(a.username)}" data-act="acc-pass">改密</button>
      <button data-u="${esc(a.username)}" data-act="acc-ban">封禁</button>
      <button data-u="${esc(a.username)}" data-act="acc-unban">解封</button>
      <button data-id="${a.id}" data-act="acc-chars">角色</button>
    </td></tr>`).join("");

  $$("#acc-table [data-act]").forEach(b => b.onclick = async () => {
    const u = b.dataset.u, id = b.dataset.id;
    try {
      if (b.dataset.act === "acc-pass") {
        const pw = prompt(`为 ${u} 设置新密码（6-32 位）`);
        if (pw) await api("POST", "/api/accounts/password", { username: u, password: pw });
      } else if (b.dataset.act === "acc-ban") {
        const reason = prompt("封禁原因", "违规行为");
        const dur = prompt("时长（如 24h、30d，0=永久）", "24h");
        if (reason) await api("POST", "/api/accounts/ban", { username: u, reason, duration: dur || "0" });
      } else if (b.dataset.act === "acc-unban") {
        await api("POST", "/api/accounts/unban", { username: u });
      } else if (b.dataset.act === "acc-chars") {
        const chars = await api("GET", "/api/accounts/characters?id=" + id);
        alert((chars || []).map(c => `${c.name} Lv${c.level}${c.online ? "（在线）" : ""}`).join("\n") || "无角色");
        return;
      }
      loadAccounts();
    } catch (e) { alert(e.message); }
  });
}

$("#acc-new").onclick = async () => {
  const u = prompt("账号名（3-16 位字母数字）");
  if (!u) return;
  const pw = prompt("密码（6-32 位）");
  if (!pw) return;
  const gm = parseInt(prompt("GM 等级（0-3，0=普通玩家）", "0") || "0", 10);
  try {
    await api("POST", "/api/accounts/create", { username: u, password: pw, gmlevel: gm });
    loadAccounts();
  } catch (e) { alert(e.message); }
};
$("#acc-search-btn").onclick = loadAccounts;
$("#acc-search").addEventListener("keydown", e => { if (e.key === "Enter") loadAccounts(); });

// ---------- 配置 ----------
let cfgConf = "worldserver.conf";
let cfgMode = "gui";
let cfgSchema = null;   // generated config-schema.json (zhCN overlay)

async function loadConfig() {
  $$("#cfg-tabs button").forEach(b => b.classList.toggle("active",
    cfgMode === "gui" ? b.dataset.conf === cfgConf : b.dataset.mode === "raw"));
  $("#cfg-list").style.display = cfgMode === "gui" ? "" : "none";
  $("#cfg-raw").style.display = cfgMode === "gui" ? "none" : "";
  if (!cfgSchema) {
    try { cfgSchema = await api("GET", "/api/config/schema"); } catch { cfgSchema = {}; }
  }
  if (cfgMode === "gui") return loadConfigList();
  return loadConfigRaw();
}

$$("#cfg-tabs button").forEach(b => b.onclick = () => {
  if (b.dataset.conf) { cfgConf = b.dataset.conf; cfgMode = "gui"; }
  else cfgMode = "raw";
  loadConfig();
});

function translateKey(key, fallback) {
  const zh = cfgSchema && cfgSchema[key] || null;
  if (zh && zh.name) return zh.name;
  if (zh && zh.pending) return "";
  return fallback;
}

async function loadConfigList() {
  const entries = await api("GET", "/api/config/list?conf=" + cfgConf);
  const q = ($("#cfg-search").value || "").toLowerCase();
  const sec = $("#cfg-section").value;
  const sections = [...new Set(entries.map(e => e.section))];
  const sel = $("#cfg-section");
  sel.innerHTML = `<option value="">全部分组</option>` +
    sections.map(s => `<option ${s === sec ? "selected" : ""}>${esc(s)}</option>`).join("");

  const filtered = entries.filter(e => {
    if (sec && e.section !== sec) return false;
    if (!q) return true;
    const zh = (cfgSchema && cfgSchema[e.key]) || {};
    return e.key.toLowerCase().includes(q) ||
      (zh.name || "").toLowerCase().includes(q) ||
      (zh.desc || "").toLowerCase().includes(q) ||
      (e.comment || "").toLowerCase().includes(q);
  });

  $("#cfg-list").innerHTML = filtered.map(e => {
    const zh = (cfgSchema && cfgSchema[e.key]) || {};
    const cn = zh.name ? esc(zh.name) : "";
    const pending = zh.pending ? `<span class="muted">（待翻译）</span>` : "";
    const desc = zh.desc ? esc(zh.desc) : esc((e.comment || e.description || "").split("\n")[0]);
    const layers = `上游默认: <b>${esc(e.upstream || "")}</b>` +
      (e.recommended ? ` · 推荐: <b>${esc(e.recommended)}</b>` : "") +
      (e.overridden ? ` · 当前为用户值: <b>${esc(e.user)}</b>` : "");
    const val = e.overridden ? e.user : (e.recommended || e.upstream);
    return `<div class="cfg-item" data-key="${esc(e.key)}">
      <div class="cfg-key">${esc(e.key)}</div>
      ${cn ? `<div class="cfg-cn">${cn}</div>` : ""}
      ${desc ? `<div class="muted">${desc}</div>` : ""}
      <div class="cfg-layers">${layers}</div>
      <div class="cfg-row">
        <input value="${esc(val)}" data-key="${esc(e.key)}">
        <button class="primary" data-save="${esc(e.key)}">保存</button>
        ${e.overridden ? `<button data-del="${esc(e.key)}">还原</button>` : ""}
      </div>
    </div>`;
  }).join("") || `<div class="muted">无匹配配置项</div>`;

  $$("#cfg-list [data-save]").forEach(b => b.onclick = async () => {
    const key = b.dataset.save;
    const input = $(`#cfg-list input[data-key="${key}"]`);
    try {
      await api("POST", "/api/config/set", { conf: cfgConf, key, value: input.value });
      loadConfigList();
    } catch (e) { alert(e.message); }
  });
  $$("#cfg-list [data-del]").forEach(b => b.onclick = async () => {
    await api("POST", "/api/config/reset", { conf: cfgConf, key: b.dataset.del });
    loadConfigList();
  });
}

$("#cfg-search").addEventListener("input", debounce(loadConfigList, 300));
$("#cfg-section").addEventListener("change", loadConfigList);

async function loadConfigRaw() {
  const raw = await api("GET", "/api/config/raw?conf=" + cfgConf);
  $("#cfg-raw-editor").value = raw.user || "";
  $("#cfg-raw-run").textContent = raw.run || "";
}

$("#cfg-raw-save").onclick = async () => {
  await api("POST", "/api/config/raw", { conf: cfgConf, content: $("#cfg-raw-editor").value });
  loadConfigRaw();
};
$("#cfg-raw-reset").onclick = async () => {
  if (!confirm("清空用户配置并恢复默认？")) return;
  await api("POST", "/api/config/reset", { conf: cfgConf });
  loadConfigRaw();
};

// ---------- 数据 ----------
async function loadData() {
  const st = await api("GET", "/api/data/status");
  const ins = st.installed || {};
  $("#data-status").innerHTML = `
    <b>要求版本</b><span>${esc(st.required)} ${esc(st.label || "")}</span>
    <b>已安装版本</b><span>${esc(ins.version || "未安装")}</span>
    <b>状态</b><span>${st.missing && st.missing.length === 0 && ins.installed
      ? pill("正常", "ok") : pill("需要下载", "warn")}</span>
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
    $("#data-progress-text").textContent = "下载失败: " + p.error;
  } else {
    $("#data-progress").style.width = "0%";
    $("#data-progress-text").textContent = "";
  }
}

$("#data-download").onclick = async () => {
  try { await api("POST", "/api/data/download", {}); } catch (e) { alert(e.message); }
  pollData();
};
$("#data-cancel").onclick = async () => { await api("POST", "/api/data/cancel", {}); };
$("#data-import").onclick = async () => {
  const f = $("#data-file").files[0];
  if (!f) return alert("请先选择 Data.zip");
  const fd = new FormData();
  fd.append("file", f);
  try { await api("POST", "/api/data/import", fd, true); }
  catch (e) { return alert(e.message); }
  pollData();
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
async function loadLogs() {
  const files = await api("GET", "/api/logs/list");
  const sel = $("#log-name");
  const cur = sel.value;
  sel.innerHTML = files.map(f => `<option value="${esc(f.name)}">${esc(f.name)}</option>`).join("");
  if (cur && files.some(f => f.name === cur)) sel.value = cur;
  const filter = $("#log-filter").value;
  const lines = await api("GET", "/api/logs/tail?name=" + encodeURIComponent(sel.value)
    + "&n=300&filter=" + encodeURIComponent(filter));
  $("#log-view").textContent = (lines || []).join("\n") || "（无日志）";
  $("#log-view").scrollTop = $("#log-view").scrollHeight;
}
$("#log-refresh").onclick = loadLogs;
$("#log-filter").addEventListener("keydown", e => { if (e.key === "Enter") loadLogs(); });

// ---------- 备份 ----------
async function loadBackup() {
  const list = await api("GET", "/api/backup/list");
  $("#bk-table tbody").innerHTML = (list || []).map(row => {
    const [name, size] = row.split("|");
    return `<tr><td>${esc(name)}</td><td>${fmtBytes(parseInt(size))}</td>
      <td><button class="danger" data-restore="${esc(name)}">恢复</button></td></tr>`;
  }).join("") || `<tr><td colspan="3" class="muted">暂无备份</td></tr>`;
  $$("#bk-table [data-restore]").forEach(b => b.onclick = async () => {
    if (!confirm("恢复该备份？当前状态会先自动备份。")) return;
    await api("POST", "/api/backup/restore", { file: b.dataset.restore });
    alert("恢复已开始，请稍后在日志中确认结果");
  });
}
$("#bk-create").onclick = async () => {
  try { await api("POST", "/api/backup/create", {}); loadBackup(); }
  catch (e) { alert(e.message); }
};

// ---------- 版本 ----------
async function loadVersion() {
  const v = await api("GET", "/api/version");
  const b = v.build || {};
  $("#ver-view").innerHTML = `
    <b>产品</b><span>${esc(b.product)} ${esc(b.version)}（${esc(b.channel)}）</span>
    <b>Core</b><span>${esc((b.core || {}).repository || "")}<br>
      ${esc((b.core || {}).branch || "")} @ ${esc((b.core || {}).commit || "")}</span>
    <b>mod-playerbots</b><span>${esc((b.playerbots || {}).repository || "")}<br>
      ${esc((b.playerbots || {}).branch || "")} @ ${esc((b.playerbots || {}).commit || "")}</span>
    <b>AC Data</b><span>${esc((v.client_data || {}).version || "未安装")}</span>
    <b>GSWXY Locale</b><span>${esc((b.locale || {}).language || "zhCN")} ${esc((b.locale || {}).version || "—")}</span>
    <b>数据库运行时</b><span>${esc((b.database_runtime || {}).flavor || "")} ${esc((b.database_runtime || {}).version || "")}</span>
    <b>构建</b><span>Run ${esc((b.build || {}).run_id || "—")} · ${esc((b.build || {}).built_at || "—")}</span>`;
}

// ---------- 生命周期按钮 ----------
$$("[data-act=start]").forEach(b => b.onclick = async () => {
  try { await api("POST", "/api/start", {}); loadOverview(); }
  catch (e) { alert(e.message); }
});
$$("[data-act=stop]").forEach(b => b.onclick = async () => {
  if (!confirm("确认安全停止全部服务器？")) return;
  await api("POST", "/api/stop", {});
  loadOverview();
});

// ---------- utils ----------
function debounce(fn, ms) {
  let t;
  return (...args) => { clearTimeout(t); t = setTimeout(() => fn(...args), ms); };
}

// ---------- 启动 ----------
$("#login-btn").onclick = doLogin;
$("#login-password").addEventListener("keydown", e => { if (e.key === "Enter") doLogin(); });
render();
setInterval(async () => {
  if (CURRENT === "#/overview") { try { await loadOverview(); } catch {} }
}, 15000);
