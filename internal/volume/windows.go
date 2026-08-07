//go:build windows

package volume

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// helperTimeout bounds one helper run. A hung COM call must fail one request,
// not wedge the service.
const helperTimeout = 5 * time.Second

// noSession is what WTSGetActiveConsoleSessionId returns when no session is
// attached to the physical console.
const noSession = 0xFFFFFFFF

type systemController struct{}

func New() Controller { return systemController{} }

// Available asks the session manager whether a session is attached to the
// console. It never spawns a helper, so it is cheap enough for the 3-second
// status poll.
//
// It is deliberately optimistic: a machine sitting at the lock screen has a
// session attached but no token to query, so Get and Set may still fail with
// ErrNoSession. This only drives the disabled state in the UI; the error is the
// authoritative answer.
func (systemController) Available() bool {
	return windows.WTSGetActiveConsoleSessionId() != noSession
}

func (c systemController) Get(ctx context.Context) (State, error) {
	return c.run(ctx, OpGet, State{})
}

// Set applies the state and returns what the device settled on. The helper
// re-reads after writing, so this is the device's own answer rather than an
// echo of the request: a device with coarse steps cannot hit every percentage.
func (c systemController) Set(ctx context.Context, s State) (State, error) {
	return c.run(ctx, OpSet, s)
}

// run spawns the helper inside the logged-in user's session and reads its one
// line of JSON.
//
// The session hop is the entire point of this file. Audio endpoints are
// per-session and this process is in session 0, which has none, so the work has
// to happen in a process that belongs to the user's session. Impersonating the
// user on this thread is not enough: the MMDevice API resolves the endpoint
// from the process's session, not the thread's token.
func (systemController) run(ctx context.Context, op Op, want State) (State, error) {
	args := FormatHelperArgs(op, want)
	if args == nil {
		return State{}, fmt.Errorf("volume: unknown operation %q", op)
	}

	session := windows.WTSGetActiveConsoleSessionId()
	if session == noSession {
		return State{}, ErrNoSession
	}

	var token windows.Token
	if err := windows.WTSQueryUserToken(session, &token); err != nil {
		// Nobody is signed in, the console is at the lock screen, or this
		// process lacks SeTcbPrivilege — which is what happens when the binary
		// is run under --console as an ordinary administrator rather than as
		// the service. Wrapped so errors.Is still matches while the underlying
		// reason survives into the log.
		return State{}, fmt.Errorf("%w: %v", ErrNoSession, err)
	}
	defer token.Close()

	exe, err := os.Executable()
	if err != nil {
		return State{}, fmt.Errorf("volume: locating the executable: %w", err)
	}

	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, helperTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, exe, args...)
	// Setting Token is what makes os/exec use CreateProcessAsUser, which is
	// what puts the helper in the user's session. HideWindow keeps a console
	// from flashing on their screen every time the slider moves.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Token:      syscall.Token(token),
		HideWindow: true,
	}

	out, err := cmd.Output()
	if err != nil {
		return State{}, helperFailure(err, ctx.Err(), parent.Err(), helperTimeout)
	}
	return DecodeResult(string(out))
}
