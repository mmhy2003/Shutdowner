package action

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"shutdowner/internal/power"
)

func TestFileStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule.json")
	s := NewFileStore(path)

	firesAt := time.Date(2026, 8, 3, 3, 0, 0, 0, time.FixedZone("test", 3*60*60))
	want := Persisted{Pending: &PersistedPending{
		ID: "id-a", Action: power.ActionShutdown, Force: true, FiresAt: firesAt,
	}}
	if err := s.Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Pending == nil {
		t.Fatal("Load() returned no pending action")
	}
	if got.Pending.ID != "id-a" || got.Pending.Action != power.ActionShutdown || !got.Pending.Force {
		t.Errorf("Load() = %+v, want the saved action", got.Pending)
	}
	// The offset must survive, so a clock that shifts between writing and
	// reading does not move the deadline.
	if !got.Pending.FiresAt.Equal(firesAt) {
		t.Errorf("FiresAt = %s, want %s", got.Pending.FiresAt, firesAt)
	}
}

func TestFileStoreLoadWithNoFile(t *testing.T) {
	s := NewFileStore(filepath.Join(t.TempDir(), "absent.json"))
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil for an absent file", err)
	}
	if got.Pending != nil || got.Missed != nil {
		t.Errorf("Load() = %+v, want an empty state", got)
	}
}

func TestFileStoreLoadRejectsCorruptContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("writing the corrupt file: %v", err)
	}
	if _, err := NewFileStore(path).Load(); err == nil {
		t.Error("Load() error = nil, want a decode error the caller can log")
	}
}

func TestFileStoreClearRemovesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule.json")
	s := NewFileStore(path)
	if err := s.Save(Persisted{Missed: &Missed{Action: power.ActionSleep, WasDueAt: "x"}}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := s.Clear(); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the file still exists after Clear(): %v", err)
	}
	// Clearing an already-absent file is not an error.
	if err := s.Clear(); err != nil {
		t.Errorf("second Clear() error = %v, want nil", err)
	}
}

func TestFileStoreSaveLeavesNoTempFileBehind(t *testing.T) {
	dir := t.TempDir()
	s := NewFileStore(filepath.Join(dir, "schedule.json"))
	if err := s.Save(Persisted{Missed: &Missed{Action: power.ActionSleep, WasDueAt: "x"}}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "schedule.json" {
		t.Errorf("directory holds %d entries, want only schedule.json", len(entries))
	}
}
