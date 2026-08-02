//go:build windows

package power

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	powrprof               = windows.NewLazySystemDLL("powrprof.dll")
	procSetSuspendState    = powrprof.NewProc("SetSuspendState")
	procGetPwrCapabilities = powrprof.NewProc("GetPwrCapabilities")
)

type systemController struct{}

func New() Controller { return systemController{} }

func (systemController) Execute(ctx context.Context, a Action, force bool) error {
	switch a {
	case ActionShutdown, ActionRestart:
		return runShutdownExe(ctx, a, force)
	case ActionSleep:
		return setSuspendState(false, force)
	case ActionHibernate:
		return setSuspendState(true, force)
	}
	return fmt.Errorf("power: unknown action %q", a)
}

// runShutdownExe drives shutdown.exe for the two actions it handles well. Its
// /f semantics are exactly what is wanted and are better tested than anything
// reimplemented over InitiateSystemShutdownEx would be.
func runShutdownExe(ctx context.Context, a Action, force bool) error {
	args := buildShutdownArgs(a, force)
	if args == nil {
		return fmt.Errorf("power: %q is not a shutdown.exe action", a)
	}
	cmd := exec.CommandContext(ctx, shutdownExePath(), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return fmt.Errorf("power: shutdown.exe %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return fmt.Errorf("power: shutdown.exe %s: %w", strings.Join(args, " "), err)
}

func shutdownExePath() string {
	if root := os.Getenv("SystemRoot"); root != "" {
		p := filepath.Join(root, "System32", "shutdown.exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "shutdown.exe"
}

// setSuspendState calls the API directly rather than going through
// "rundll32 powrprof.dll,SetSuspendState", because that form hibernates instead
// of sleeping whenever hibernation is enabled and so cannot distinguish the two.
//
// The call blocks until the machine resumes, so it returns when the sleep or
// hibernation ends, not when it begins.
func setSuspendState(hibernate, force bool) error {
	r, _, err := procSetSuspendState.Call(boolArg(hibernate), boolArg(force), 0)
	if r != 0 {
		return nil
	}
	return fmt.Errorf("power: SetSuspendState(hibernate=%t): %w", hibernate, syscallError(err))
}

func (systemController) Capabilities(context.Context) (Capabilities, error) {
	var caps systemPowerCapabilities
	r, _, err := procGetPwrCapabilities.Call(uintptr(unsafe.Pointer(&caps)))
	if r == 0 {
		return Capabilities{}, fmt.Errorf("power: GetPwrCapabilities: %w", syscallError(err))
	}
	return caps.capabilities(), nil
}

func boolArg(b bool) uintptr {
	if b {
		return 1
	}
	return 0
}

// syscallError normalises the error LazyProc.Call returns. It is never nil, and
// carries errno 0 ("The operation completed successfully") when the call failed
// without setting one, which would otherwise read as a confusing success.
func syscallError(err error) error {
	var errno syscall.Errno
	if errors.As(err, &errno) && errno != 0 {
		return errno
	}
	return errors.New("the call failed without setting an error code")
}
