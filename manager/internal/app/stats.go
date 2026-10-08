package app

import (
	"fmt"
	"strings"
)

// Population 统一口径：机器人 = 账号名带随机机器人前缀（上游
// RandomPlayerbotFactory 以 <prefix><N> 批量建号，默认 rndbot，落库时
// 大写）。此前 overview / playerbot 两页各用一张并不存在的表互相矛盾，
// 现统一为一处实现，SQL 错误如实上抛而不是吞掉后显示 0。
type Population struct {
	RealPlayers int `json:"real_players"`
	Bots        int `json:"bots"`
	Total       int `json:"total"`
}

// BotAccountPrefix 返回生效的随机机器人账号前缀（大写，来自三层配置）。
func (a *App) BotAccountPrefix() string {
	prefix := "rndbot"
	if s, err := a.ConfigSchemaCached("playerbots.conf"); err == nil {
		if eff, err := a.CM.Resolve("playerbots.conf", s); err == nil {
			for _, e := range eff {
				if e.Key == "AiPlayerbot.RandomBotAccountPrefix" && strings.TrimSpace(e.Value) != "" {
					prefix = strings.TrimSpace(e.Value)
				}
			}
		}
	}
	return strings.ToUpper(prefix)
}

// OnlinePopulation 一条 SQL 统计在线真人 / 机器人 / 总数。
func (a *App) OnlinePopulation() (Population, error) {
	var p Population
	db, err := a.DB()
	if err != nil {
		return p, err
	}
	like := a.BotAccountPrefix() + "%"
	err = db.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(a.username LIKE ?), 0),
		       COALESCE(SUM(a.username NOT LIKE ?), 0)
		FROM acore_characters.characters c
		JOIN acore_auth.account a ON a.id = c.account
		WHERE c.online = 1`, like, like).
		Scan(&p.Total, &p.Bots, &p.RealPlayers)
	if err != nil {
		return p, fmt.Errorf("统计在线人数失败: %w", err)
	}
	return p, nil
}

// NamePoolSize 中文名字池大小（characters 库的 playerbots_names 表）。
func (a *App) NamePoolSize() (int, error) {
	db, err := a.DB()
	if err != nil {
		return 0, err
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM acore_characters.playerbots_names`).Scan(&n); err != nil {
		return 0, fmt.Errorf("统计名字池失败: %w", err)
	}
	return n, nil
}

// KeyValueCount 分组计数（levels/classes/races），只对传入的列名白名单。
func (a *App) KeyValueCount(column string, onlineOnly bool) (map[string]int, error) {
	switch column {
	case "level", "class", "race":
	default:
		return nil, fmt.Errorf("非法分组列: %s", column)
	}
	db, err := a.DB()
	if err != nil {
		return nil, err
	}
	q := "SELECT " + column + ", COUNT(*) FROM acore_characters.characters"
	if onlineOnly {
		q += " WHERE online = 1"
	}
	q += " GROUP BY " + column
	out := map[string]int{}
	rows, err := db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("统计%s分布失败: %w", column, err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[fmt.Sprintf("%d", k)] = n
	}
	return out, rows.Err()
}

// TotalCharacters 全部角色存量（含机器人，区分在线/存量两套口径）。
func (a *App) TotalCharacters() (int, error) {
	db, err := a.DB()
	if err != nil {
		return 0, err
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM acore_characters.characters`).Scan(&n); err != nil {
		return 0, fmt.Errorf("统计角色存量失败: %w", err)
	}
	return n, nil
}

// FactionSplit 阵营人数（online=true 在线，false 全量）。
func (a *App) FactionSplit(online bool) (alliance, horde int, err error) {
	db, err := a.DB()
	if err != nil {
		return 0, 0, err
	}
	cond := ""
	if online {
		cond = " AND online = 1"
	}
	q := `SELECT COALESCE(SUM(race IN (1,3,4,7,11)),0), COALESCE(SUM(race IN (2,5,6,8,10)),0)
		FROM acore_characters.characters WHERE 1=1` + cond
	if err := db.QueryRow(q).Scan(&alliance, &horde); err != nil {
		return 0, 0, fmt.Errorf("统计阵营失败: %w", err)
	}
	return alliance, horde, nil
}
