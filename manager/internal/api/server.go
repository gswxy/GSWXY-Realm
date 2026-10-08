// HTTP API of GSWXY Manager. All mutating endpoints are POST, session
// protected, CSRF-checked and audited. Static WebUI is served from the
// embedded bundle. Sensitive values (passwords) are redacted in audit.
package api

import (
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gswxy/gswxy-realm/manager/internal/admin"
	"github.com/gswxy/gswxy-realm/manager/internal/app"
	"github.com/gswxy/gswxy-realm/manager/internal/logging"
	"github.com/gswxy/gswxy-realm/manager/internal/state"
)

//go:embed all:static
var staticFS embed.FS

// staticRoot serves the embedded UI bundle at / (strips the static/ prefix).
var staticRoot, _ = fs.Sub(staticFS, "static")

// Server wires App + Auth into an http.Handler.
type Server struct {
	App   *app.App
	Log   *logging.Logger
	Auth  *Auth
	Admin *admin.Store
	State *state.Store
}

// Handler builds the route table (Go 1.22+ method routing).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && !strings.Contains(r.URL.Path, ".") {
			// SPA fallback
			b, err := fs.ReadFile(staticRoot, "index.html")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(b)
			return
		}
		http.FileServer(http.FS(staticRoot)).ServeHTTP(w, r)
	})

	// auth
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.session(s.handleLogout))
	mux.HandleFunc("GET /api/session", s.handleSession)

	// overview + preflight
	mux.HandleFunc("GET /api/overview", s.session(s.handleOverview))
	mux.HandleFunc("GET /api/preflight", s.session(s.handlePreflight))

	// lifecycle
	mux.HandleFunc("POST /api/start", s.session(s.admin(s.handleStart)))
	mux.HandleFunc("POST /api/stop", s.session(s.admin(s.handleStop)))
	mux.HandleFunc("POST /api/restart", s.session(s.admin(s.handleRestart)))

	// setup
	mux.HandleFunc("POST /api/setup/start", s.session(s.admin(s.handleSetupStart)))
	mux.HandleFunc("GET /api/setup/status", s.session(s.handleSetupStatus))
	mux.HandleFunc("POST /api/setup/password", s.handleSetupPassword)

	// client data
	mux.HandleFunc("GET /api/data/status", s.session(s.handleDataStatus))
	mux.HandleFunc("POST /api/data/download", s.session(s.admin(s.handleDataDownload)))
	mux.HandleFunc("POST /api/data/cancel", s.session(s.admin(s.handleDataCancel)))
	mux.HandleFunc("POST /api/data/import", s.session(s.admin(s.handleDataImport)))

	// config
	mux.HandleFunc("GET /api/config/list", s.session(s.handleConfigList))
	mux.HandleFunc("GET /api/config/schema", s.session(s.handleConfigSchema))
	mux.HandleFunc("POST /api/config/set", s.session(s.admin(s.handleConfigSet)))
	mux.HandleFunc("GET /api/config/raw", s.session(s.handleConfigRaw))
	mux.HandleFunc("POST /api/config/raw", s.session(s.admin(s.handleConfigRawSave)))
	mux.HandleFunc("POST /api/config/reset", s.session(s.admin(s.handleConfigReset)))
	mux.HandleFunc("POST /api/config/regenerate", s.session(s.admin(s.handleConfigRegen)))

	// playerbot
	mux.HandleFunc("GET /api/playerbot/summary", s.session(s.handleBotSummary))
	mux.HandleFunc("GET /api/playerbot/profiles", s.session(s.handleBotProfiles))
	mux.HandleFunc("POST /api/playerbot/profile", s.session(s.admin(s.handleBotApplyProfile)))

	// accounts
	mux.HandleFunc("GET /api/accounts", s.session(s.handleAccounts))
	mux.HandleFunc("POST /api/accounts/create", s.session(s.admin(s.handleAccountCreate)))
	mux.HandleFunc("POST /api/accounts/password", s.session(s.admin(s.handleAccountPassword)))
	mux.HandleFunc("POST /api/accounts/gmlevel", s.session(s.admin(s.handleAccountGM)))
	mux.HandleFunc("POST /api/accounts/ban", s.session(s.admin(s.handleAccountBan)))
	mux.HandleFunc("POST /api/accounts/unban", s.session(s.admin(s.handleAccountUnban)))
	mux.HandleFunc("GET /api/accounts/characters", s.session(s.handleCharacters))

	// console
	mux.HandleFunc("GET /api/console/tail", s.session(s.handleConsoleTail))
	mux.HandleFunc("POST /api/console/send", s.session(s.admin(s.handleConsoleSend)))

	// logs
	mux.HandleFunc("GET /api/logs/list", s.session(s.handleLogList))
	mux.HandleFunc("GET /api/logs/tail", s.session(s.handleLogTail))

	// backup
	mux.HandleFunc("GET /api/backup/list", s.session(s.handleBackupList))
	mux.HandleFunc("POST /api/backup/create", s.session(s.admin(s.handleBackupCreate)))
	mux.HandleFunc("POST /api/backup/restore", s.session(s.admin(s.handleBackupRestore)))

	// version / realm
	mux.HandleFunc("GET /api/version", s.session(s.handleVersion))
	mux.HandleFunc("POST /api/realm/name", s.session(s.admin(s.handleRealmName)))
	mux.HandleFunc("GET /api/realm/addresses", s.session(s.handleRealmAddresses))
	mux.HandleFunc("POST /api/realm/addresses", s.session(s.admin(s.handleRealmAddressesSave)))
	mux.HandleFunc("GET /api/launcher", s.session(s.handleLauncher))

	return s.logMiddleware(mux)
}

// ---- middleware ----

func (s *Server) session(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.skipAuth() {
			next(w, r)
			return
		}
		tok := tokenFrom(r)
		if tok == "" || !s.Auth.Verify(tok) {
			fail(w, http.StatusUnauthorized, "需要登录")
			return
		}
		if r.Method == http.MethodPost {
			csrf := r.Header.Get("X-CSRF-Token")
			if csrf == "" || len(tok) < 16 || csrf != tok[:16] {
				fail(w, http.StatusForbidden, "CSRF 校验失败")
				return
			}
		}
		next(w, r)
	}
}

// admin wraps an authenticated handler with audit logging.
func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body := s.safeBody(r)
		next(w, r)
		s.Log.Audit(clientIP(r), r.URL.Path, body)
	}
}

func (s *Server) safeBody(r *http.Request) string {
	if r.Body == nil {
		return ""
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	r.Body = io.NopCloser(strings.NewReader(string(raw)))
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for _, k := range []string{"password", "new_password", "content"} {
		if _, ok := m[k]; ok {
			m[k] = "***"
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func (s *Server) skipAuth() bool {
	return os.Getenv("GSRM_DEV_NOAUTH") == "1"
}

func csrfOf(token string) string { return token[:16] }

func (s *Server) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			start := time.Now()
			next.ServeHTTP(w, r)
			s.Log.Info("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// decodeJSON reads the request body into v (call after safeBody).
func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}
