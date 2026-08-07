package volume

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// helperFailure turns a failed helper run into the error the caller sees.
//
// It lives outside the Windows-only spawn code deliberately: it touches no
// Windows API, so keeping it here means the one piece of that path with any
// branching is covered by tests that run on any platform.
//
// runErr is what exec returned. ctxErr is the derived context's error, set when
// the helper outlived its own timeout. parentErr is the caller's context error,
// set when the request was abandoned upstream — checked first, because blaming
// the helper for a timeout the caller caused is a misleading thing to show
// someone.
func helperFailure(runErr, ctxErr, parentErr error, timeout time.Duration) error {
	if parentErr != nil {
		return errors.New("volume: the request was cancelled before the helper finished")
	}
	if ctxErr != nil {
		return fmt.Errorf("volume: the helper did not finish within %s", timeout)
	}

	var exit *exec.ExitError
	if errors.As(runErr, &exit) {
		if stderr := strings.TrimSpace(string(exit.Stderr)); stderr != "" {
			return fmt.Errorf("volume: the helper failed: %s", stderr)
		}
		return fmt.Errorf("volume: the helper exited with status %d", exit.ExitCode())
	}
	return fmt.Errorf("volume: could not start the helper: %w", runErr)
}
