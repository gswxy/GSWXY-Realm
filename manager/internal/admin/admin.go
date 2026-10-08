// Package admin stores the Manager admin credential (bcrypt) and session
// secrets. No default weak password: first-run requires the operator to
// set one (via fnOS install wizard or the setup UI).
package admin

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

// Store persists the admin credential hash (var/state/admin.json, 0600).
type Store struct {
	mu   sync.Mutex
	path string
	data file
}

type file struct {
	Hash       string `json:"hash"`
	SessionKey string `json:"session_key"`
}

func Open(stateDir string) (*Store, error) {
	s := &Store{path: filepath.Join(stateDir, "admin.json")}
	if raw, err := os.ReadFile(s.path); err == nil {
		_ = jsonUnmarshal(raw, &s.data)
	}
	return s, nil
}

// SetPassword creates/replaces the admin password (min 8 chars).
func (s *Store) SetPassword(pw string) error {
	if len(pw) < 8 {
		return errShort
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.data.Hash = string(h)
	if s.data.SessionKey == "" {
		s.data.SessionKey = randomKey(32)
	}
	s.mu.Unlock()
	return s.save()
}

// Verify checks a password.
func (s *Store) Verify(pw string) bool {
	s.mu.Lock()
	h := s.data.Hash
	s.mu.Unlock()
	if h == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(h), []byte(pw)) == nil
}

// Initialized reports whether a password has been set.
func (s *Store) Initialized() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Hash != ""
}

// SessionKey returns the persistent HMAC key for session tokens.
func (s *Store) SessionKey() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.SessionKey == "" {
		s.data.SessionKey = randomKey(32)
		_ = s.save()
	}
	return s.data.SessionKey
}

// RotateSessionKey replaces the signing key, invalidating every issued
// token immediately (logout on a single-admin app).
func (s *Store) RotateSessionKey() error {
	s.mu.Lock()
	s.data.SessionKey = randomKey(32)
	s.mu.Unlock()
	return s.save()
}

func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	raw := jsonMarshal(s.data)
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func randomKey(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type shortErr struct{}

func (shortErr) Error() string { return "密码至少 8 位" }

var errShort = shortErr{}
