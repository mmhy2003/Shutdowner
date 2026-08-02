// Package logging provides a size-rotating io.Writer. It is hand-rolled to keep
// the dependency count at three.
package logging

import (
	"fmt"
	"os"
	"sync"
)

const (
	DefaultMaxBytes int64 = 5 << 20 // 5 MB
	DefaultKeep           = 2
)

// RotatingWriter appends to path, rolling it over to path.1, path.2 and so on
// once it exceeds maxBytes. A Windows service has no console, so this file is
// the only place its output goes.
type RotatingWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	keep     int
	size     int64
	f        *os.File
}

func NewRotatingWriter(path string, maxBytes int64, keep int) (*RotatingWriter, error) {
	w := &RotatingWriter{path: path, maxBytes: maxBytes, keep: keep}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *RotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening log file %s: %w", w.path, err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("stat log file %s: %w", w.path, err)
	}
	w.f = f
	w.size = info.Size()
	return nil
}

func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// A single write larger than the limit is written whole rather than split;
	// rotating first keeps it in a file of its own.
	if w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *RotatingWriter) rotateLocked() error {
	if err := w.f.Close(); err != nil {
		return err
	}
	// Drop the oldest generation, then shift each remaining one down.
	_ = os.Remove(fmt.Sprintf("%s.%d", w.path, w.keep))
	for i := w.keep - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1))
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil {
		// Reopen so logging survives a rename failure rather than going silent.
		if oerr := w.open(); oerr != nil {
			return oerr
		}
		return err
	}
	return w.open()
}

func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
