// Package api implements the Manager HTTP API + embedded WebUI.
package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gswxy/gswxy-realm/manager/internal/logging"
)

// sessionTTL and login rate limit.
const (
	sessionTTL       = 12 * time.Hour
	loginMaxAttempts = 8
	loginWindow      = 10 * time.Minute
)

// Session is a signed token: hex(payload) + "." + hex(hmac).
type session struct {
	User      string    `json:"u"`
	ExpiresAt time.Time `json:"e"`
}

// Auth guards the API with sessions, CSRF and rate limiting. The signing
// key is fetched through keyFn on every operation so rotating it (logout)
// invalidates outstanding tokens immediately.
type Auth struct {
	keyFn func() []byte
	log   *logging.Logger
	mu    sync.Mutex
	attempt map[string][]time.Time // ip -> timestamps
}

func NewAuth(keyFn func() []byte, log *logging.Logger) *Auth {
	return &Auth{keyFn: keyFn, log: log, attempt: map[string][]time.Time{}}
}

func (a *Auth) key() []byte {
	if a.keyFn != nil {
		return a.keyFn()
	}
	return nil
}

// Issue mints a token for user.
func (a *Auth) Issue(user string) string {
	s := session{User: user, ExpiresAt: time.Now().Add(sessionTTL)}
	raw, _ := json.Marshal(s)
	mac := hmac.New(sha256.New, a.key())
	mac.Write(raw)
	return hex.EncodeToString(raw) + "." + hex.EncodeToString(mac.Sum(nil))
}

// Verify validates a token.
func (a *Auth) Verify(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return false
	}
	raw, err := hex.DecodeString(parts[0])
	if err != nil {
		return false
	}
	mac, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}
	mm := hmac.New(sha256.New, a.key())
	mm.Write(raw)
	if !hmac.Equal(mac, mm.Sum(nil)) {
		return false
	}
	var s session
	if err := json.Unmarshal(raw, &s); err != nil {
		return false
	}
	return time.Now().Before(s.ExpiresAt)
}

// AllowLogin rate-limits per IP.
func (a *Auth) AllowLogin(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	recent := a.attempt[ip][:0]
	for _, t := range a.attempt[ip] {
		if now.Sub(t) < loginWindow {
			recent = append(recent, t)
		}
	}
	a.attempt[ip] = recent
	if len(recent) >= loginMaxAttempts {
		return false
	}
	a.attempt[ip] = append(recent, now)
	return true
}

// middleware helpers ----

// SameSite=None（iframe 可用）需要 Secure+HTTPS；Manager 走 HTTP，
// 因此 cookie 只作为同源顶级页面的便捷载体，跨站 iframe 场景由
// Authorization: Bearer 头承担（见 session 中间件）。
func (a *Auth) secureCookie(token string) *http.Cookie {
	return &http.Cookie{
		Name:     "gswxy_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	}
}

// tokenFrom extracts the session token: Authorization header first
// (works inside cross-site iframes where cookies are withheld), then
// the cookie (plain browser navigation).
func tokenFrom(r *http.Request) string {
	if ah := r.Header.Get("Authorization"); strings.HasPrefix(ah, "Bearer ") {
		return strings.TrimPrefix(ah, "Bearer ")
	}
	if c, err := r.Cookie("gswxy_session"); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// writeJSON is the standard JSON responder.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
