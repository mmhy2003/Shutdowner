//go:build windows

package winsvc

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

// Run hosts start under the service control manager when this process was
// launched by it, and runs it in the foreground otherwise, so the same binary
// works as a service and from a console.
func Run(logger *slog.Logger, start func() error, stop func() error) error {
	isSvc, err := svc.IsWindowsService()
	if err != nil {
		return fmt.Errorf("determining the service context: %w", err)
	}
	if !isSvc {
		return start()
	}
	return svc.Run(ServiceName, &handler{logger: logger, start: start, stop: stop})
}

type handler struct {
	logger *slog.Logger
	start  func() error
	stop   func() error
}

func (h *handler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown

	status <- svc.Status{State: svc.StartPending}
	errc := make(chan error, 1)
	go func() { errc <- h.start() }()
	status <- svc.Status{State: svc.Running, Accepts: accepted}

	for {
		select {
		case err := <-errc:
			// The server stopped on its own, which for a service is a failure
			// unless something asked it to.
			if err != nil {
				h.logger.Error("server stopped unexpectedly", "error", err)
				status <- svc.Status{State: svc.StopPending}
				return true, 1
			}
			status <- svc.Status{State: svc.StopPending}
			return false, 0

		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				// svc.Shutdown also arrives when this app shuts the machine
				// down, so the graceful path runs either way.
				status <- svc.Status{State: svc.StopPending}
				if err := h.stop(); err != nil {
					h.logger.Error("stopping the server", "error", err)
				}
				return false, 0
			}
		}
	}
}

func Install(exePath string, args ...string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connecting to the service manager (run this from an elevated prompt): %w", err)
	}
	defer m.Disconnect()

	if s, err := m.OpenService(ServiceName); err == nil {
		s.Close()
		return fmt.Errorf("service %q already exists; run --uninstall-service first", ServiceName)
	}

	s, err := m.CreateService(ServiceName, exePath, mgr.Config{
		DisplayName: displayName,
		Description: description,
		StartType:   mgr.StartAutomatic,
	}, args...)
	if err != nil {
		return fmt.Errorf("creating the service: %w", err)
	}
	defer s.Close()

	// Restart on failure, so a crash does not silently leave the PC unreachable.
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}, uint32(24*time.Hour/time.Second)); err != nil {
		return fmt.Errorf("setting recovery actions: %w", err)
	}

	// The event log source is how startup failures become visible: a service
	// that dies before it can open its log file has nowhere else to report.
	if err := eventlog.InstallAsEventCreate(ServiceName, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil &&
		!strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("registering the event log source: %w", err)
	}

	if err := s.Start(); err != nil {
		return fmt.Errorf("starting the service: %w", err)
	}
	return nil
}

func Uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connecting to the service manager (run this from an elevated prompt): %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(ServiceName)
	if err != nil {
		return fmt.Errorf("service %q is not installed", ServiceName)
	}
	defer s.Close()

	// Best effort: a service that will not stop can still be marked for
	// deletion and disappears at the next reboot.
	_, _ = s.Control(svc.Stop)
	waitForStop(s)

	if err := s.Delete(); err != nil {
		return fmt.Errorf("deleting the service: %w", err)
	}
	_ = eventlog.Remove(ServiceName)
	return nil
}

func waitForStop(s *mgr.Service) {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		status, err := s.Query()
		if err != nil || status.State == svc.Stopped {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// ReportError writes to the Windows event log. It is best effort: it is called
// on startup failure, which is exactly when the normal log file may be
// unavailable.
func ReportError(msg string) {
	l, err := eventlog.Open(ServiceName)
	if err != nil {
		return
	}
	defer l.Close()
	_ = l.Error(1, msg)
}
