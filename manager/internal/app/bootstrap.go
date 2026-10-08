package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gswxy/gswxy-realm/manager/internal/state"
)

// bootstrapFile 是 fnOS 安装向导（install_callback）写入的首装参数：
//   var/state/bootstrap.json  {"admin_password","realm_name","min_bots"}
//
// Manager 启动时一次性消费：读取 → 校验 → 应用 → 删除。
// 无论应用结果如何都删除文件，避免残留的旧文件在后续升级重启时
// 覆盖用户已修改的密码/配置；被跳过的字段会记录到日志与审计。
type bootstrapDoc struct {
	AdminPassword string `json:"admin_password"`
	RealmName     string `json:"realm_name"`
	MinBots       string `json:"min_bots"`
}

// Min/MaxRandomBots 的合法区间（安装向导与后端共用的口径）。
const (
	wizardMinBotsFloor = 10
	wizardMinBotsCeil  = 500
)

// ConsumeBootstrap applies wizard values captured at install time. It is
// safe to call on every start: the file only exists right after a fresh
// install (install_callback refuses to overwrite an existing one).
func (a *App) ConsumeBootstrap() {
	path := filepath.Join(a.Paths.State(), "bootstrap.json")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		a.Log.Warn("bootstrap: read failed: %v", err)
		return
	}
	// 一次性消费：无论内容是否合法都不再保留。
	defer func() {
		if err := os.Remove(path); err != nil {
			a.Log.Warn("bootstrap: remove failed: %v", err)
		}
	}()

	var doc bootstrapDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		a.Log.Error("bootstrap: malformed JSON, dropped: %v", err)
		a.Log.Audit("system", "bootstrap.consume", "malformed; dropped")
		return
	}

	applied := []string{}
	skipped := []string{}

	// 管理员密码：仅在尚未设置时应用（升级携带旧 bootstrap 的场景不会
	// 覆盖既有密码；正常首装时 admin.json 尚不存在）。
	if pw := strings.TrimSpace(doc.AdminPassword); pw != "" {
		if a.Admin.Initialized() {
			skipped = append(skipped, "admin_password(已有密码，跳过)")
		} else if len(pw) < 8 {
			skipped = append(skipped, "admin_password(少于8位，跳过)")
		} else if err := a.Admin.SetPassword(pw); err != nil {
			skipped = append(skipped, "admin_password("+err.Error()+")")
		} else {
			applied = append(applied, "admin_password")
		}
	}

	// 服务器名称：1-32 字符，写入 state（setup 的 registerRealm 会落库）。
	if name := strings.TrimSpace(doc.RealmName); name != "" {
		if l := len([]rune(name)); l < 1 || l > 32 {
			skipped = append(skipped, "realm_name(长度不合法，跳过)")
		} else {
			_ = a.State.Update(func(d *state.Data) { d.Realm.Name = name })
			applied = append(applied, "realm_name")
		}
	}

	// 初始机器人数量：写入 playerbots 用户层（Min=N，Max 留出余量），
	// setup 重新生成配置后生效；范围外则收敛到边界。
	if s := strings.TrimSpace(doc.MinBots); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			if n < wizardMinBotsFloor {
				n = wizardMinBotsFloor
			}
			if n > wizardMinBotsCeil {
				n = wizardMinBotsCeil
			}
			max := n + n/4
			if max < n+10 {
				max = n + 10
			}
			if err := a.CM.SetUser("playerbots.conf", "AiPlayerbot.MinRandomBots", strconv.Itoa(n)); err == nil {
				_ = a.CM.SetUser("playerbots.conf", "AiPlayerbot.MaxRandomBots", strconv.Itoa(max))
				applied = append(applied, fmt.Sprintf("min_bots=%d(max %d)", n, max))
			} else {
				skipped = append(skipped, "min_bots("+err.Error()+")")
			}
		} else {
			skipped = append(skipped, "min_bots(非数字，跳过)")
		}
	}

	a.Log.Info("bootstrap consumed: applied=%v skipped=%v", applied, skipped)
	a.Log.Audit("system", "bootstrap.consume",
		"applied="+strings.Join(applied, ",")+" skipped="+strings.Join(skipped, ","))
}
