package app

import (
	"database/sql"
	"fmt"

	"github.com/gswxy/gswxy-realm/manager/internal/state"
)

// registerRealm upserts the realmlist row so a fresh install is playable
// immediately (realm name from state; address = local host, LAN-visible).
func (a *App) registerRealm() error {
	d := a.State.Get()
	name := d.Realm.Name
	if name == "" {
		name = "GSWXY Realm"
	}
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
			 VALUES (?, '127.0.0.1', '127.0.0.1', '255.255.255.0', 8085, 0, 2, 1, 0, 0, 12340)`, name)
		if err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		_, err = db.Exec(
			`UPDATE acore_auth.realmlist SET name = ? WHERE id = ?`, name, id)
		if err != nil {
			return err
		}
	}
	_ = a.State.Update(func(dd *state.Data) { dd.Realm.Name = name })
	return nil
}

// SetRealmName updates the realm name (概览页常用设置).
func (a *App) SetRealmName(name string) error {
	if name == "" {
		return fmt.Errorf("名称不能为空")
	}
	_ = a.State.Update(func(d *state.Data) { d.Realm.Name = name })
	return a.registerRealm()
}
