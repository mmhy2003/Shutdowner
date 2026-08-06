// Package volume controls the master volume of the machine's default playback
// device.
//
// Unlike everything else Shutdowner does, this cannot be done from the service
// itself. Windows audio endpoints are per-session and a LocalSystem service
// lives in session 0, which has none, so the Windows implementation spawns a
// helper into the logged-in user's session. See windows.go.
package volume

import (
	"context"
	"errors"
)

// State is the master volume of the default playback device. Level is whole
// percent; the device's own step scale never escapes this package.
type State struct {
	Level int  `json:"level"`
	Muted bool `json:"muted"`
}

var (
	// ErrNoSession means nobody is signed in at the PC, so no session holds an
	// audio endpoint. It is a temporary condition rather than a failure, and
	// the web layer reports it as 503.
	ErrNoSession = errors.New("volume: nobody is signed in at the PC")

	// ErrUnsupported is what the non-Windows build returns.
	ErrUnsupported = errors.New("volume: not supported on this platform")
)

type Controller interface {
	Get(ctx context.Context) (State, error)
	Set(ctx context.Context, s State) error

	// Available reports whether a session is attached to the console. It must
	// stay cheap enough for the 3-second status poll, so it queries the session
	// manager and never spawns a helper. It is deliberately optimistic: a
	// machine at the lock screen reports true while Get and Set may still fail
	// with ErrNoSession, which is the authoritative answer.
	Available() bool
}
