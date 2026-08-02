// Package winsvc hosts Shutdowner under the Windows service control manager and
// installs or removes the service. It owns the "am I a service?" decision so
// that cmd/shutdowner needs no build tags of its own.
package winsvc

import "errors"

const (
	ServiceName = "Shutdowner"
	displayName = "Shutdowner remote power control"
	description = "Serves the Shutdowner web UI for remote shutdown, restart, sleep and hibernate."
)

// ErrWindowsOnly is returned by Install and Uninstall on other platforms.
var ErrWindowsOnly = errors.New("winsvc: service management is only available on Windows")
