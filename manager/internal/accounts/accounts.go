// Package accounts manages game accounts through safe, audited database
// operations. Password verifiers are computed with AzerothCore's SRP6
// scheme (little-endian big integers, see
// src/common/Cryptography/Authentication/SRP6.cpp upstream).
package accounts

import (
	"crypto/rand"
	"crypto/sha1"
	"database/sql"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// AC SRP6 constants (WoW 3.3.5a / AzerothCore).
var (
	srpN, _ = new(big.Int).SetString(
		"894B645E89E1535BBDAD5B8B290650530801B18EBFBF5E8FAB3C82872A3E9BB7", 16)
	srpG = big.NewInt(7)
)

// Manager performs account operations against acore_auth.
type Manager struct {
	DB *sql.DB // connected as the app user
}

// srp6Verifier computes (salt, verifier) exactly like upstream SRP6.cpp:
//
//	H1 = H(USERNAME || ':' || PASSWORD)          (uppercased username)
//	H2 = H(salt || H1)
//	x  = H2 interpreted little-endian
//	v  = g^x mod N
//
// All big integers serialize little-endian (BigNumber default).
func srp6Verifier(username, password string) (salt, verifier []byte, err error) {
	salt = make([]byte, 32)
	if _, err = rand.Read(salt); err != nil {
		return nil, nil, err
	}
	h1 := sha1.Sum([]byte(strings.ToUpper(username) + ":" + password))
	h2in := append(append([]byte{}, salt...), h1[:]...)
	h2 := sha1.Sum(h2in)

	x := new(big.Int).SetBytes(reverseBytes(h2[:])) // little-endian
	v := new(big.Int).Exp(srpG, x, srpN)
	vb := v.Bytes()
	return salt, reverseBytes(vb), nil
}

func reverseBytes(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		out[len(b)-1-i] = c
	}
	return out
}

// Create inserts a new account (bcrypt-free path: SRP6 only, as upstream).
func (m *Manager) Create(username, password string) (int64, error) {
	username = strings.TrimSpace(username)
	if !validName(username) {
		return 0, fmt.Errorf("账号名不合法（3-16 位字母数字）")
	}
	if len(password) < 6 || len(password) > 32 {
		return 0, fmt.Errorf("密码长度需在 6-32 位之间")
	}
	var exists int
	if err := m.DB.QueryRow(
		`SELECT COUNT(*) FROM acore_auth.account WHERE username = ?`,
		strings.ToUpper(username)).Scan(&exists); err != nil {
		return 0, err
	}
	if exists > 0 {
		return 0, fmt.Errorf("账号已存在")
	}
	salt, verifier, err := srp6Verifier(username, password)
	if err != nil {
		return 0, err
	}
	res, err := m.DB.Exec(
		`INSERT INTO acore_auth.account
		   (username, salt, verifier, email, reg_mail, joindate, last_login, locale)
		 VALUES (?, ?, ?, '', '', ?, NOW(), 4)`,
		strings.ToUpper(username), salt, verifier, time.Now().Format("2006-01-02 15:04:05"))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ChangePassword rewrites the SRP6 verifier for an account.
func (m *Manager) ChangePassword(username, password string) error {
	if len(password) < 6 || len(password) > 32 {
		return fmt.Errorf("密码长度需在 6-32 位之间")
	}
	salt, verifier, err := srp6Verifier(username, password)
	if err != nil {
		return err
	}
	res, err := m.DB.Exec(
		`UPDATE acore_auth.account SET salt=?, verifier=? WHERE username = ?`,
		salt, verifier, strings.ToUpper(username))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("账号不存在")
	}
	return nil
}

// SetGMLevel updates the GM level (RealmID -1 = all realms).
func (m *Manager) SetGMLevel(username string, level int) error {
	if level < 0 || level > 3 {
		return fmt.Errorf("GM 等级必须是 0-3")
	}
	u := strings.ToUpper(username)
	var id int64
	if err := m.DB.QueryRow(
		`SELECT id FROM acore_auth.account WHERE username = ?`, u).Scan(&id); err != nil {
		return fmt.Errorf("账号不存在")
	}
	_, err := m.DB.Exec(
		`INSERT INTO acore_auth.account_access (id, gmlevel, RealmID)
		 VALUES (?, ?, -1)
		 ON DUPLICATE KEY UPDATE gmlevel = VALUES(gmlevel)`, id, level)
	return err
}

// Ban bans an account; duration is like "30d", "12h", "0" = permanent.
func (m *Manager) Ban(username, reason, duration string) error {
	var id int64
	if err := m.DB.QueryRow(
		`SELECT id FROM acore_auth.account WHERE username = ?`,
		strings.ToUpper(username)).Scan(&id); err != nil {
		return fmt.Errorf("账号不存在")
	}
	secs := parseDurationInt(duration)
	var unbandate int64
	if secs == 0 {
		unbandate = 32503680000 // far future ≈ year 3000 (permanent)
	} else {
		unbandate = time.Now().Unix() + secs
	}
	_, err := m.DB.Exec(
		`INSERT INTO acore_auth.account_banned
		       (id, bandate, unbandate, bannedby, banreason, active)
		 VALUES (?, UNIX_TIMESTAMP(NOW()), ?, 'GSWXY Manager', ?, 1)`,
		id, unbandate, reason)
	return err
}

// Unban lifts every active ban of the account.
func (m *Manager) Unban(username string) error {
	var id int64
	if err := m.DB.QueryRow(
		`SELECT id FROM acore_auth.account WHERE username = ?`,
		strings.ToUpper(username)).Scan(&id); err != nil {
		return fmt.Errorf("账号不存在")
	}
	_, err := m.DB.Exec(
		`UPDATE acore_auth.account_banned
		 SET active = 0, unbandate = UNIX_TIMESTAMP(NOW())
		 WHERE id = ? AND active = 1`, id)
	return err
}

func parseDurationInt(d string) int64 {
	secs := int64(0)
	switch {
	case strings.HasSuffix(d, "d"):
		fmt.Sscanf(d, "%dd", &secs)
		secs *= 86400
	case strings.HasSuffix(d, "h"):
		fmt.Sscanf(d, "%dh", &secs)
		secs *= 3600
	default:
		fmt.Sscanf(d, "%d", &secs)
	}
	return secs
}

// Summary is one account row for the UI.
type Summary struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	GMLevel  int    `json:"gmlevel"`
	Banned   bool   `json:"banned"`
	LastIP   string `json:"last_ip,omitempty"`
	LastLogin string `json:"last_login,omitempty"`
	Online   bool   `json:"online"`
	Locale   int    `json:"locale"`
}

// List returns accounts with ban/online status.
func (m *Manager) List(search string, limit int) ([]Summary, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	base := `SELECT a.id, a.username,
	               COALESCE(aa.gmlevel, 0),
	               EXISTS(SELECT 1 FROM acore_auth.account_banned b
	                      WHERE b.id = a.id AND b.active = 1),
	               COALESCE(a.last_ip, ''), COALESCE(a.last_login, ''),
	               COALESCE(a.online, 0), COALESCE(a.locale, 0)
	        FROM acore_auth.account a
	        LEFT JOIN acore_auth.account_access aa
	               ON aa.id = a.id AND aa.RealmID = -1`
	var args []any
	where := ""
	if search != "" {
		where = ` WHERE a.username LIKE ?`
		args = append(args, "%"+strings.ToUpper(search)+"%")
	}
	q := base + where + ` ORDER BY a.id LIMIT ?`
	args = append(args, limit)

	rows, err := m.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Summary
	for rows.Next() {
		var s Summary
		var online int
		if err := rows.Scan(&s.ID, &s.Username, &s.GMLevel, &s.Banned,
			&s.LastIP, &s.LastLogin, &online, &s.Locale); err != nil {
			return nil, err
		}
		s.Online = online != 0
		out = append(out, s)
	}
	return out, rows.Err()
}

// Characters lists characters of one account.
type Character struct {
	GUID   int64  `json:"guid"`
	Name   string `json:"name"`
	Level  int    `json:"level"`
	Race   int    `json:"race"`
	Class  int    `json:"class"`
	Online bool   `json:"online"`
}

func (m *Manager) Characters(accountID int64) ([]Character, error) {
	rows, err := m.DB.Query(
		`SELECT guid, name, level, race, class, online
		 FROM acore_characters.characters WHERE account = ? ORDER BY level DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Character
	for rows.Next() {
		var c Character
		var on int
		if err := rows.Scan(&c.GUID, &c.Name, &c.Level, &c.Race, &c.Class, &on); err != nil {
			return nil, err
		}
		c.Online = on != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

func validName(s string) bool {
	if len(s) < 3 || len(s) > 16 {
		return false
	}
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if !ok {
			return false
		}
	}
	return true
}
