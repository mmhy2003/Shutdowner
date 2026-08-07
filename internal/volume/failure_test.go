package volume

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// realExitError produces a genuine *exec.ExitError with stderr attached, so the
// test exercises the same type the Windows path will actually receive rather
// than a hand-built stand-in.
func realExitError(t *testing.T, script string) error {
	t.Helper()
	_, err := exec.Command("sh", "-c", script).Output()
	if err == nil {
		t.Fatalf("expected %q to fail", script)
	}
	return err
}

func TestHelperFailureBlamesTheCallerFirst(t *testing.T) {
	// Both contexts are done: the caller's cancellation is the true cause, and
	// reporting a helper timeout would send someone hunting the wrong problem.
	err := helperFailure(context.Canceled, context.DeadlineExceeded, context.Canceled, 5*time.Second)
	if !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("error = %q, want it to name the cancellation", err)
	}
	if strings.Contains(err.Error(), "5s") {
		t.Errorf("error = %q, want it not to blame the helper timeout", err)
	}
}

func TestHelperFailureReportsItsOwnTimeout(t *testing.T) {
	err := helperFailure(context.DeadlineExceeded, context.DeadlineExceeded, nil, 5*time.Second)
	if !strings.Contains(err.Error(), "did not finish within 5s") {
		t.Errorf("error = %q, want it to name the 5s timeout", err)
	}
}

func TestHelperFailureSurfacesStderr(t *testing.T) {
	err := helperFailure(realExitError(t, "echo 'no default playback device' >&2; exit 1"), nil, nil, 5*time.Second)
	if !strings.Contains(err.Error(), "no default playback device") {
		t.Errorf("error = %q, want it to carry the helper's stderr", err)
	}
}

func TestHelperFailureReportsTheExitStatusWhenStderrIsEmpty(t *testing.T) {
	err := helperFailure(realExitError(t, "exit 3"), nil, nil, 5*time.Second)
	if !strings.Contains(err.Error(), "status 3") {
		t.Errorf("error = %q, want it to name the exit status", err)
	}
}

func TestHelperFailureReportsASpawnFailure(t *testing.T) {
	_, runErr := exec.Command("/definitely/not/a/real/binary").Output()
	if runErr == nil {
		t.Fatal("expected spawning a nonexistent binary to fail")
	}
	err := helperFailure(runErr, nil, nil, 5*time.Second)
	if !strings.Contains(err.Error(), "could not start the helper") {
		t.Errorf("error = %q, want it to say the helper could not be started", err)
	}
}
