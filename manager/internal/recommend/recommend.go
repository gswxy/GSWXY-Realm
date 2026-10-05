// Package recommend embeds the GSWXY recommended configuration values.
package recommend

import (
	_ "embed"
	"encoding/json"
	"sync"
)

//go:embed recommended.json
var raw []byte

// Store is the lazy-parsed recommendation table.
type Store struct {
	once sync.Once
	m    map[string]map[string]string // conf -> key -> value
}

// New returns the recommendation store.
func New() *Store { return &Store{} }

func (s *Store) load() {
	s.once.Do(func() {
		s.m = map[string]map[string]string{}
		_ = json.Unmarshal(raw, &s.m)
	})
}

// Recommended implements confman.RecSource.
func (s *Store) Recommended(conf, key string) (string, bool) {
	s.load()
	if m, ok := s.m[conf]; ok {
		v, ok2 := m[key]
		return v, ok2
	}
	return "", false
}
