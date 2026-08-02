//go:build !windows

package winsvc

import (
	"errors"
	"io"
	"log/slog"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRunCallsStartInTheForeground(t *testing.T) {
	called := false
	err := Run(discardLogger(), func() error {
		called = true
		return nil
	}, func() error { return nil })

	if err != nil {
		t.Errorf("Run() error = %v, want nil", err)
	}
	if !called {
		t.Error("Run() did not call start")
	}
}

func TestRunPropagatesTheStartError(t *testing.T) {
	boom := errors.New("bind: address already in use")
	err := Run(discardLogger(), func() error { return boom }, func() error { return nil })
	if !errors.Is(err, boom) {
		t.Errorf("Run() error = %v, want the start error", err)
	}
}

func TestInstallAndUninstallAreWindowsOnly(t *testing.T) {
	if err := Install("/tmp/shutdowner"); !errors.Is(err, ErrWindowsOnly) {
		t.Errorf("Install() error = %v, want ErrWindowsOnly", err)
	}
	if err := Uninstall(); !errors.Is(err, ErrWindowsOnly) {
		t.Errorf("Uninstall() error = %v, want ErrWindowsOnly", err)
	}
}

func TestReportErrorIsSafeOffWindows(t *testing.T) {
	// Must not panic; there is no Windows event log to write to.
	ReportError("something went wrong")
}
