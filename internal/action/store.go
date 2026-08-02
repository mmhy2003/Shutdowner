package action

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"shutdowner/internal/power"
)

// PersistedPending is a scheduled action as it survives a restart.
//
// FiresAt keeps its offset, unlike the naive wall clock the operator typed. The
// intent was captured when the action was scheduled, so that is the instant to
// replay; storing it naively would let a DST change move the deadline.
type PersistedPending struct {
	ID      string       `json:"id"`
	Action  power.Action `json:"action"`
	Force   bool         `json:"force"`
	FiresAt time.Time    `json:"firesAt"`
}

// Persisted is the whole of what the manager keeps across restarts.
type Persisted struct {
	Pending *PersistedPending `json:"pending,omitempty"`
	Missed  *Missed           `json:"missed,omitempty"`
}

// Store keeps the schedule somewhere it survives the process.
type Store interface {
	Load() (Persisted, error)
	Save(Persisted) error
	Clear() error
}

// NopStore discards everything. It is the default, so a Manager built without a
// store behaves exactly as it did before there was one.
type NopStore struct{}

func (NopStore) Load() (Persisted, error) { return Persisted{}, nil }
func (NopStore) Save(Persisted) error     { return nil }
func (NopStore) Clear() error             { return nil }

// FileStore keeps the schedule in a JSON file.
type FileStore struct{ path string }

func NewFileStore(path string) *FileStore { return &FileStore{path: path} }

// Load reports an absent file as an empty state rather than an error: a machine
// with nothing scheduled is the ordinary case, not a fault.
func (s *FileStore) Load() (Persisted, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return Persisted{}, nil
	}
	if err != nil {
		return Persisted{}, fmt.Errorf("reading %s: %w", s.path, err)
	}
	var p Persisted
	if err := json.Unmarshal(data, &p); err != nil {
		return Persisted{}, fmt.Errorf("parsing %s: %w", s.path, err)
	}
	return p, nil
}

// Save writes through a temporary file and a rename, so a crash mid-write
// cannot leave a half-written schedule that fails to parse at startup.
func (s *FileStore) Save(p Persisted) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the schedule: %w", err)
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".schedule-*.tmp")
	if err != nil {
		return fmt.Errorf("creating a temporary file in %s: %w", dir, err)
	}
	name := tmp.Name()
	// Any failure from here on leaves the temp file behind unless it is removed,
	// and a directory slowly filling with .schedule-*.tmp is its own bug report.
	defer os.Remove(name)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", name, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("setting the mode of %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", name, err)
	}
	if err := os.Rename(name, s.path); err != nil {
		return fmt.Errorf("replacing %s: %w", s.path, err)
	}
	return nil
}

func (s *FileStore) Clear() error {
	if err := os.Remove(s.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", s.path, err)
	}
	return nil
}
