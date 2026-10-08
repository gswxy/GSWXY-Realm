// Package state persists installation status and versioning on disk.
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Setup steps, in execution order.
const (
	StepEnvCheck       = "env_check"
	StepDBInit         = "db_init"
	StepDBImport       = "db_import"
	StepPlayerbotInit  = "playerbot_init"
	StepLocaleImport   = "locale_import"
	StepClientData     = "client_data"
	StepRealm          = "realm"
	StepDone           = "done"
)

var stepOrder = []string{
	StepEnvCheck, StepDBInit, StepDBImport, StepPlayerbotInit,
	StepLocaleImport, StepClientData, StepRealm, StepDone,
}

// Data is the on-disk state document (var/state/state.json).
type Data struct {
	Setup struct {
		Current     string            `json:"current"`
		Completed   map[string]string `json:"completed"` // step -> finishedAt RFC3339
		Error       string            `json:"error,omitempty"`
		InProgress  bool              `json:"in_progress"`
		Initialized bool              `json:"initialized"` // first-run setup fully done
	} `json:"setup"`

	// Database connection facts discovered at init time.
	Database struct {
		Port   int    `json:"port"`
		Socket string `json:"socket,omitempty"`
	} `json:"database"`

	// ClientData records the installed AC client data version.
	ClientData struct {
		Version    string `json:"version"`
		Installed  bool   `json:"installed"`
		VerifiedAt string `json:"verified_at,omitempty"`
		Source     string `json:"source,omitempty"`
	} `json:"client_data"`

	// Locale tracks the imported GSWXY zhCN data version.
	Locale struct {
		Version    string `json:"version"`
		ImportedAt string `json:"imported_at,omitempty"`
	} `json:"locale"`

	// Schema patches applied from upstream/module updates.
	Patches map[string]PatchRecord `json:"patches"`

	// Realm configuration captured at first run.
	Realm struct {
		Name         string `json:"name"`
		Address      string `json:"address"`       // 公网地址；空=自动检测
		LocalAddress string `json:"local_address"` // 本地/LAN 地址；空=自动检测
	} `json:"realm"`

	// Download job progress (transient mirror of the downloader).
	Download struct {
		Active    bool    `json:"active"`
		Source    string  `json:"source,omitempty"`
		URL       string  `json:"url,omitempty"`
		BytesDone int64   `json:"bytes_done"`
		BytesTotal int64  `json:"bytes_total"`
		SpeedBps  float64 `json:"speed_bps"`
		Error     string  `json:"error,omitempty"`
	} `json:"download"`

	// Backup bookkeeping (job status lives in the backup manager).
	Backup struct {
		AutoEnabled bool   `json:"auto_enabled"`
		LastAutoAt  string `json:"last_auto_at,omitempty"`
		LastCreated string `json:"last_created,omitempty"`
		LastRestore string `json:"last_restore,omitempty"`
	} `json:"backup"`
}

// PatchRecord remembers one applied SQL patch.
type PatchRecord struct {
	AppliedAt string `json:"applied_at"`
	Checksum  string `json:"checksum"`
}

// Store is a mutex-guarded JSON file store.
type Store struct {
	mu   sync.Mutex
	path string
	data Data
}

func Open(path string) (*Store, error) {
	s := &Store{path: path, data: Data{}}
	s.data.Patches = map[string]PatchRecord{}
	s.data.Setup.Completed = map[string]string{}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &s.data)
		if s.data.Patches == nil {
			s.data.Patches = map[string]PatchRecord{}
		}
		if s.data.Setup.Completed == nil {
			s.data.Setup.Completed = map[string]string{}
		}
	}
	return s, nil
}

func (s *Store) Get() Data {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data
}

func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Update applies a mutation under lock and persists.
func (s *Store) Update(fn func(d *Data)) error {
	s.mu.Lock()
	fn(&s.data)
	s.mu.Unlock()
	return s.Save()
}

// MarkStepDone records a finished setup step and advances the cursor
// from the step that was completed.
func (s *Store) MarkStepDone(step string) error {
	return s.Update(func(d *Data) {
		d.Setup.Completed[step] = time.Now().Format(time.RFC3339)
		d.Setup.Current = NextStep(step)
		if d.Setup.Current == StepDone {
			d.Setup.InProgress = false
			d.Setup.Initialized = true
			d.Setup.Error = ""
		}
	})
}

// PendingStep returns the first setup step without a completed record.
func (d Data) PendingStep() string {
	for _, st := range stepOrder {
		if _, ok := d.Setup.Completed[st]; !ok {
			return st
		}
	}
	return StepDone
}

// NextStep returns the step after cur.
func NextStep(cur string) string {
	for i, st := range stepOrder {
		if st == cur && i+1 < len(stepOrder) {
			return stepOrder[i+1]
		}
	}
	return StepDone
}
