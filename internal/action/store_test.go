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
		t.Errorf("FiresAt = %s, want the same instant as %s", got.Pending.FiresAt, firesAt)
	}
	// Equal compares instants, so it passes even if the offset was normalised
	// away to UTC. The offset is the point: it is what keeps a DST change from
	// moving the deadline.
	_, wantOffset := firesAt.Zone()
	if _, gotOffset := got.Pending.FiresAt.Zone(); gotOffset != wantOffset {
		t.Errorf("FiresAt zone offset = %d, want %d — the stored offset was lost", gotOffset, wantOffset)
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

func TestFileStoreSaveFailureRemovesTheTemp(t *testing.T) {
	// Force Save to fail at the rename step by pointing the FileStore at a
	// path that is itself a directory.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "schedule.json"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	s := NewFileStore(filepath.Join(dir, "schedule.json"))
	if err := s.Save(Persisted{Pending: &PersistedPending{ID: "id", Action: power.ActionShutdown, FiresAt: time.Now()}}); err == nil {
		t.Fatal("Save() error = nil, want an error from the rename failure")
	}
	// The defer os.Remove in Save should have cleaned up the temp file.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	// Only the schedule.json directory exists; no temp files.
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want only 1 (the directory)", len(entries))
	}
	for _, e := range entries {
		if e.Name() != "schedule.json" {
			t.Errorf("unexpected entry: %s", e.Name())
		}
	}
}

func TestFileStoreSaveTwiceReplacesTheFile(t *testing.T) {
	// Verify os.Rename replaces an existing file on Windows as well as Unix.
	path := filepath.Join(t.TempDir(), "schedule.json")
	s := NewFileStore(path)

	// First save.
	first := Persisted{Pending: &PersistedPending{
		ID: "id-1", Action: power.ActionShutdown, Force: false, FiresAt: time.Now(),
	}}
	if err := s.Save(first); err != nil {
		t.Fatalf("first Save() error = %v", err)
	}

	// Second save with different contents.
	second := Persisted{Missed: &Missed{Action: power.ActionSleep, WasDueAt: "2026-08-03T03:00:00Z"}}
	if err := s.Save(second); err != nil {
		t.Fatalf("second Save() error = %v", err)
	}

	// Load and verify the second value is what persists.
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Pending != nil {
		t.Errorf("Load() returned Pending = %+v, want nil (second save cleared it)", got.Pending)
	}
	if got.Missed == nil || got.Missed.Action != power.ActionSleep {
		t.Errorf("Load() returned Missed = %+v, want the second save's value", got.Missed)
	}
}
