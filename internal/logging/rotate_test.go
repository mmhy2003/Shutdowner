package logging

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriterCreatesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	w, err := NewRotatingWriter(path, 1024, 2)
	if err != nil {
		t.Fatalf("NewRotatingWriter() error = %v", err)
	}
	defer w.Close()

	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(b) != "hello\n" {
		t.Errorf("file contents = %q, want %q", b, "hello\n")
	}
}

func TestWriterAppendsToAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	if err := os.WriteFile(path, []byte("earlier\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := NewRotatingWriter(path, 1024, 2)
	if err != nil {
		t.Fatalf("NewRotatingWriter() error = %v", err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("later\n")); err != nil {
		t.Fatal(err)
	}

	b, _ := os.ReadFile(path)
	if string(b) != "earlier\nlater\n" {
		t.Errorf("file contents = %q, want the earlier content preserved", b)
	}
}

func TestWriterRotatesWhenFull(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := NewRotatingWriter(path, 20, 2)
	if err != nil {
		t.Fatalf("NewRotatingWriter() error = %v", err)
	}
	defer w.Close()

	// Each line is 10 bytes, so the third crosses the 20 byte limit.
	for i := 0; i < 3; i++ {
		if _, err := w.Write([]byte(fmt.Sprintf("line-%04d\n", i))); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected %s.1 to exist after rotation: %v", path, err)
	}
	current, _ := os.ReadFile(path)
	if !strings.Contains(string(current), "line-0002") {
		t.Errorf("current log = %q, want it to hold the newest line", current)
	}
	rotated, _ := os.ReadFile(path + ".1")
	if !strings.Contains(string(rotated), "line-0000") {
		t.Errorf("rotated log = %q, want it to hold the oldest lines", rotated)
	}
}

func TestWriterKeepsOnlyTheConfiguredGenerations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := NewRotatingWriter(path, 20, 2)
	if err != nil {
		t.Fatalf("NewRotatingWriter() error = %v", err)
	}
	defer w.Close()

	for i := 0; i < 12; i++ {
		if _, err := w.Write([]byte(fmt.Sprintf("line-%04d\n", i))); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}

	if _, err := os.Stat(path + ".2"); err != nil {
		t.Errorf("expected %s.2 to exist: %v", path, err)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Errorf("expected %s.3 not to exist with keep=2", path)
	}
}

func TestWriteLargerThanTheLimitStillSucceeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	w, err := NewRotatingWriter(path, 8, 1)
	if err != nil {
		t.Fatalf("NewRotatingWriter() error = %v", err)
	}
	defer w.Close()

	big := strings.Repeat("x", 100)
	n, err := w.Write([]byte(big))
	if err != nil {
		t.Fatalf("Write() error = %v, want nil", err)
	}
	if n != len(big) {
		t.Errorf("Write() = %d, want %d", n, len(big))
	}
}

func TestWriteAfterCloseReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	w, err := NewRotatingWriter(path, 1024, 2)
	if err != nil {
		t.Fatalf("NewRotatingWriter() error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	// A service whose log file is its only output must not panic here.
	n, err := w.Write([]byte("after close\n"))
	if !errors.Is(err, ErrClosed) {
		t.Errorf("Write() error = %v, want ErrClosed", err)
	}
	if n != 0 {
		t.Errorf("Write() = %d, want 0", n)
	}

	// Close must remain idempotent.
	if err := w.Close(); err != nil {
		t.Errorf("second Close() error = %v, want nil", err)
	}
}
