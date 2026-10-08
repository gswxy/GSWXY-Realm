package app

import (
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gswxy/gswxy-realm/manager/internal/state"
)

// registerRealm upserts the realmlist row so a fresh install is playable
// immediately. Address columns come from the effective (configured or
// auto-detected) values.
func (a *App) registerRealm() error {
	d := a.State.Get()
	name := d.Realm.Name
	if name == "" {
		name = "GSWXY Realm"
	}
	pub, loc := a.EffectiveRealmAddresses()
	port := a.RealmPort()

	db, err := a.DB()
	if err != nil {
		return err
	}
	var id int64
	err = db.QueryRow(`SELECT id FROM acore_auth.realmlist LIMIT 1`).Scan(&id)
	switch {
	case err == sql.ErrNoRows:
		_, err = db.Exec(
			`INSERT INTO acore_auth.realmlist (name, address, localAddress, localSubnetMask, port, icon, flag, timezone, allowedSecurityLevel, population, gamebuild)
			 VALUES (?, ?, ?, '255.255.255.0', ?, 0, 2, 1, 0, 0, 12340)`, name, pub, loc, port)
		if err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		_, err = db.Exec(
			`UPDATE acore_auth.realmlist SET name = ?, address = ?, localAddress = ?, port = ? WHERE id = ?`,
			name, pub, loc, port, id)
		if err != nil {
			return err
		}
	}
	_ = a.State.Update(func(dd *state.Data) { dd.Realm.Name = name })
	return nil
}

// SetRealmName updates the realm name (Overview page common settings).
func (a *App) SetRealmName(name string) error {
	if name == "" {
		return fmt.Errorf("名称不能为空")
	}
	_ = a.State.Update(func(d *state.Data) { d.Realm.Name = name })
	return a.registerRealm()
}

// SetRealmAddresses persists the configured addresses (empty = auto) and
// applies them immediately when the database is reachable.
func (a *App) SetRealmAddresses(address, localAddress string) error {
	for _, v := range []string{address, localAddress} {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if ip := net.ParseIP(v); ip == nil && !validHostname(v) {
			return fmt.Errorf("地址不合法: %s", v)
		}
	}
	_ = a.State.Update(func(d *state.Data) {
		d.Realm.Address = strings.TrimSpace(address)
		d.Realm.LocalAddress = strings.TrimSpace(localAddress)
	})
	publicMu.Lock()
	publicCache.at = time.Time{} // 保存后强制重新检测
	publicMu.Unlock()
	// 数据库尚未就绪（如初始化前）时先落盘，setup/启动时会经 registerRealm 应用。
	_ = a.registerRealm()
	return nil
}

func validHostname(h string) bool {
	if len(h) > 253 {
		return false
	}
	return regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)+$`).
		MatchString(h)
}

// EffectiveRealmAddresses returns (public, local) addresses in effect:
// configured value when set, otherwise the auto-detected one. Public
// detection failing falls back to the local address (LAN play keeps working).
func (a *App) EffectiveRealmAddresses() (pub, loc string) {
	d := a.State.Get()
	loc = strings.TrimSpace(d.Realm.LocalAddress)
	if loc == "" {
		loc = detectLocalIP()
	}
	pub = strings.TrimSpace(d.Realm.Address)
	if pub == "" {
		pub = a.detectPublicIP()
	}
	if pub == "" {
		pub = loc
	}
	return pub, loc
}

// AutoRealmAddresses reports the detected values (for UI display of what
// "auto" currently resolves to).
func (a *App) AutoRealmAddresses() (pub, loc string) {
	return a.detectPublicIP(), detectLocalIP()
}

// realmPort reads WorldServerPort from the generated run conf.
func (a *App) RealmPort() int {
	raw, err := os.ReadFile(a.CM.RunPath("worldserver.conf"))
	if err == nil {
		if m := regexp.MustCompile(`(?m)^WorldServerPort\s*=\s*(\d+)`).FindSubmatch(raw); m != nil {
			if p, err := strconv.Atoi(string(m[1])); err == nil && p > 0 {
				return p
			}
		}
	}
	return 8085
}

// detectLocalIP picks the NAS's primary LAN IPv4: physical NICs first,
// skipping loopback and virtual bridges/docker/zerotier interfaces.
func detectLocalIP() string {
	var preferred, fallback []net.IP
	ifaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		name := ifc.Name
		virtual := strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "veth") ||
			strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "zt") ||
			strings.HasPrefix(name, "lo") || strings.HasPrefix(name, "tun") ||
			strings.HasPrefix(name, "tap") || strings.HasPrefix(name, "wg")
		addrs, _ := ifc.Addrs()
		for _, addr := range addrs {
			ip4, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ip4.IP.To4()
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if virtual {
				fallback = append(fallback, ip)
			} else {
				preferred = append(preferred, ip)
			}
		}
	}
	if len(preferred) > 0 {
		return preferred[0].String()
	}
	if len(fallback) > 0 {
		return fallback[0].String()
	}
	return "127.0.0.1"
}

var (
	publicMu    sync.Mutex
	publicCache struct {
		val string
		at  time.Time
	}
)

// detectPublicIP best-effort queries a public echo service (60s cache);
// returns "" when offline (caller falls back to the local address).
func (a *App) detectPublicIP() string {
	publicMu.Lock()
	defer publicMu.Unlock()
	if time.Since(publicCache.at) < time.Minute {
		return publicCache.val
	}
	client := &http.Client{Timeout: 4 * time.Second}
	for _, url := range []string{"https://api.ipify.org", "http://ip.3322.net/"} {
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		ip := strings.TrimSpace(string(b))
		if net.ParseIP(ip) != nil {
			publicCache.val, publicCache.at = ip, time.Now()
			return ip
		}
	}
	publicCache.at = time.Now()
	return publicCache.val
}

// LauncherBAT renders the Windows launcher for the effective address.
// Pure ASCII + CRLF so cmd.exe handles it on any codepage.
func (a *App) LauncherBAT() []byte {
	_, loc := a.EffectiveRealmAddresses()
	port := a.RealmPort()
	realm := fmt.Sprintf("%s:%d", loc, port)
	crlf := "\r\n"
	var b strings.Builder
	lines := []string{
		"@echo off",
		"REM ============================================",
		"REM  GSWXY Realm Launcher",
		"REM  Put this file into your WoW 3.3.5a client",
		"REM  folder and double-click to play.",
		"REM  Server: " + realm,
		"REM ============================================",
		"setlocal",
		"set \"WOWDIR=%~dp0\"",
		"set \"REALM=set realmlist " + loc + "\"",
		"",
		"if not exist \"%WOWDIR%Wow.exe\" (",
		"  echo [GSWXY] Wow.exe not found in this folder.",
		"  echo [GSWXY] Please put this .bat into your WoW 3.3.5a client folder.",
		"  pause",
		"  exit /b 1",
		")",
		"",
		"set WRITTEN=0",
		"for %%L in (enUS enGB zhCN zhTW ruRU koKR frFR deDE esES esMX) do (",
		"  if exist \"%WOWDIR%Data\\%%L\" (",
		"    >\"%WOWDIR%Data\\%%L\\realmlist.wtf\" echo %REALM%",
		"    echo [GSWXY] realmlist set: Data\\%%L",
		"    set WRITTEN=1",
		"  )",
		")",
		"if exist \"%WOWDIR%Data\\realmlist.wtf\" (",
		"  >\"%WOWDIR%Data\\realmlist.wtf\" echo %REALM%",
		"  set WRITTEN=1",
		")",
		"if \"%WRITTEN%\"==\"0\" echo [GSWXY] No locale folder found, realmlist unchanged.",
		"",
		"echo [GSWXY] Connecting to " + realm + " ...",
		"start \"\" \"%WOWDIR%Wow.exe\"",
		"endlocal",
		"",
	}
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString(crlf)
	}
	return []byte(b.String())
}
