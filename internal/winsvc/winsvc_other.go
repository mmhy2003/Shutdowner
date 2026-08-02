//go:build !windows

package winsvc

import "log/slog"

// Run executes start in the foreground. There is no service control manager to
// report to off Windows.
func Run(_ *slog.Logger, start func() error, _ func() error) error {
	return start()
}

func Install(string, ...string) error { return ErrWindowsOnly }

func Uninstall() error { return ErrWindowsOnly }

// ReportError is a no-op off Windows.
func ReportError(string) {}
