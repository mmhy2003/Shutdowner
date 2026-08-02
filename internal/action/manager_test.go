package action

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"shutdowner/internal/power"
)

type harness struct {
	mgr  *Manager
	fake *power.Fake
	now  time.Time
	ids  int
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		fake: power.NewFake(),
		now:  time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
	}
	h.mgr = New(h.fake,
		WithClock(func() time.Time { return h.now }),
		WithIDFunc(func() string {
			h.ids++
			return "id-" + string(rune('a'+h.ids-1))
		}),
	)
	return h
}

// schedule queues a after delay, which is what the caller now computes.
func (h *harness) schedule(t *testing.T, a power.Action, force bool, delay time.Duration) (Pending, error) {
	t.Helper()
	return h.mgr.Schedule(context.Background(), a, force, h.now.Add(delay))
}

// advance moves the clock and ticks once, standing in for time passing.
func (h *harness) advance(d time.Duration) {
	h.now = h.now.Add(d)
	h.mgr.Tick()
}

func TestScheduleFromIdle(t *testing.T) {
	h := newHarness(t)

	p, err := h.schedule(t, power.ActionShutdown, true, 45*time.Second)
	if err != nil {
		t.Fatalf("Schedule() error = %v, want nil", err)
	}
	if p.ID != "id-a" {
		t.Errorf("ID = %q, want id-a", p.ID)
	}
	if p.Action != power.ActionShutdown || !p.Force {
		t.Errorf("Pending = %+v, want shutdown with force", p)
	}
	if p.RemainingSeconds != 45 {
		t.Errorf("RemainingSeconds = %d, want 45", p.RemainingSeconds)
	}
	wantFiresAt := h.now.Add(45 * time.Second)
	gotFiresAt, err := time.Parse(time.RFC3339, p.FiresAtLocal)
	if err != nil {
		t.Errorf("FiresAtLocal = %q, does not parse as RFC3339: %v", p.FiresAtLocal, err)
	} else if !gotFiresAt.Equal(wantFiresAt) {
		t.Errorf("FiresAtLocal = %q, want the instant %v", p.FiresAtLocal, wantFiresAt)
	}
	if s := h.mgr.Status(); s.State != StatePending {
		t.Errorf("State = %q, want pending", s.State)
	}
	if len(h.fake.Calls()) != 0 {
		t.Error("the controller was called before the countdown elapsed")
	}
}

func TestScheduleRejectsASecondAction(t *testing.T) {
	h := newHarness(t)
	if _, err := h.schedule(t, power.ActionShutdown, true, 45*time.Second); err != nil {
		t.Fatalf("first Schedule() error = %v", err)
	}
	if _, err := h.schedule(t, power.ActionRestart, true, 45*time.Second); !errors.Is(err, ErrConflict) {
		t.Errorf("second Schedule() error = %v, want ErrConflict", err)
	}
}

func TestScheduleRejectsAnInvalidAction(t *testing.T) {
	h := newHarness(t)
	if _, err := h.schedule(t, power.Action("explode"), false, 45*time.Second); !errors.Is(err, ErrInvalidAction) {
		t.Errorf("Schedule() error = %v, want ErrInvalidAction", err)
	}
}

func TestScheduleRejectsUnavailableSuspendActions(t *testing.T) {
	h := newHarness(t)
	h.fake.SetCapabilities(power.Capabilities{Sleep: true})

	if _, err := h.schedule(t, power.ActionHibernate, false, 45*time.Second); !errors.Is(err, ErrUnsupportedAction) {
		t.Errorf("hibernate Schedule() error = %v, want ErrUnsupportedAction", err)
	}
	if _, err := h.schedule(t, power.ActionSleep, false, 45*time.Second); err != nil {
		t.Errorf("sleep Schedule() error = %v, want nil", err)
	}
}

func TestShutdownIsNeverCapabilityGated(t *testing.T) {
	h := newHarness(t)
	h.fake.SetCapabilities(power.Capabilities{})

	if _, err := h.schedule(t, power.ActionShutdown, true, 45*time.Second); err != nil {
		t.Errorf("Schedule(shutdown) error = %v, want nil even with no capabilities", err)
	}
}

func TestFiringExecutesTheAction(t *testing.T) {
	h := newHarness(t)
	if _, err := h.schedule(t, power.ActionRestart, false, 45*time.Second); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}

	h.advance(45 * time.Second)

	calls := h.fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("len(Calls()) = %d, want 1", len(calls))
	}
	if calls[0].Action != power.ActionRestart || calls[0].Force {
		t.Errorf("Calls()[0] = %+v, want restart without force", calls[0])
	}
	if s := h.mgr.Status(); s.State != StateIdle {
		t.Errorf("State = %q, want idle after a successful action", s.State)
	}
}

func TestExecutionFailureIsReported(t *testing.T) {
	h := newHarness(t)
	h.fake.SetError(errors.New("shutdown.exe exited 1"))

	if _, err := h.schedule(t, power.ActionShutdown, true, 45*time.Second); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	h.advance(45 * time.Second)

	s := h.mgr.Status()
	if s.State != StateFailed {
		t.Fatalf("State = %q, want failed", s.State)
	}
	if s.Error != "shutdown.exe exited 1" {
		t.Errorf("Error = %q, want the underlying message", s.Error)
	}
}

func TestFailedRevertsToIdle(t *testing.T) {
	h := newHarness(t)
	h.fake.SetError(errors.New("nope"))
	if _, err := h.schedule(t, power.ActionShutdown, true, 45*time.Second); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	h.advance(45 * time.Second)

	h.now = h.now.Add(FailedRetention - time.Second)
	if s := h.mgr.Status(); s.State != StateFailed {
		t.Errorf("State = %q just before the retention elapses, want failed", s.State)
	}

	h.now = h.now.Add(2 * time.Second)
	if s := h.mgr.Status(); s.State != StateIdle {
		t.Errorf("State = %q after the retention elapsed, want idle", s.State)
	}
}

func TestScheduleIsAllowedFromFailed(t *testing.T) {
	h := newHarness(t)
	h.fake.SetError(errors.New("nope"))
	if _, err := h.schedule(t, power.ActionShutdown, true, 45*time.Second); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	h.advance(45 * time.Second)
	if s := h.mgr.Status(); s.State != StateFailed {
		t.Fatalf("State = %q, want failed", s.State)
	}

	h.fake.SetError(nil)
	if _, err := h.schedule(t, power.ActionRestart, true, 45*time.Second); err != nil {
		t.Errorf("Schedule() from failed error = %v, want nil", err)
	}
}

func TestAbortCancelsAPendingAction(t *testing.T) {
	h := newHarness(t)
	p, err := h.schedule(t, power.ActionShutdown, true, 45*time.Second)
	if err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	if err := h.mgr.Abort(p.ID); err != nil {
		t.Fatalf("Abort() error = %v, want nil", err)
	}
	if s := h.mgr.Status(); s.State != StateIdle {
		t.Errorf("State = %q, want idle", s.State)
	}

	h.advance(45 * time.Second)
	if len(h.fake.Calls()) != 0 {
		t.Error("the action ran after being aborted")
	}
}

func TestAbortRejectsAStaleID(t *testing.T) {
	h := newHarness(t)
	if _, err := h.schedule(t, power.ActionShutdown, true, 45*time.Second); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	if err := h.mgr.Abort("id-from-an-old-tab"); !errors.Is(err, ErrNoPending) {
		t.Errorf("Abort() error = %v, want ErrNoPending", err)
	}
	if s := h.mgr.Status(); s.State != StatePending {
		t.Errorf("State = %q, want the original action still pending", s.State)
	}
}

func TestAbortWhenIdle(t *testing.T) {
	h := newHarness(t)
	if err := h.mgr.Abort("anything"); !errors.Is(err, ErrNoPending) {
		t.Errorf("Abort() error = %v, want ErrNoPending", err)
	}
}

func TestRemainingSecondsCountsDown(t *testing.T) {
	h := newHarness(t)
	if _, err := h.schedule(t, power.ActionShutdown, true, 45*time.Second); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}

	h.now = h.now.Add(30 * time.Second)
	s := h.mgr.Status()
	if s.Pending == nil {
		t.Fatal("Status().Pending = nil, want a pending action")
	}
	if s.Pending.RemainingSeconds != 15 {
		t.Errorf("RemainingSeconds = %d, want 15", s.Pending.RemainingSeconds)
	}

	h.now = h.now.Add(time.Minute)
	if got := h.mgr.Status().Pending.RemainingSeconds; got != 0 {
		t.Errorf("RemainingSeconds = %d past the deadline, want 0 rather than a negative number", got)
	}
}

func TestStatusOmitsPendingWhenIdle(t *testing.T) {
	h := newHarness(t)
	s := h.mgr.Status()
	if s.State != StateIdle {
		t.Errorf("State = %q, want idle", s.State)
	}
	if s.Pending != nil {
		t.Errorf("Pending = %+v, want nil", s.Pending)
	}
	if s.Error != "" {
		t.Errorf("Error = %q, want empty", s.Error)
	}
}

func TestTickBeforeTheDeadlineDoesNothing(t *testing.T) {
	h := newHarness(t)
	if _, err := h.schedule(t, power.ActionShutdown, true, 2*time.Hour); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}

	h.advance(119 * time.Minute)
	if len(h.fake.Calls()) != 0 {
		t.Fatal("the action ran before its deadline")
	}
	if s := h.mgr.Status(); s.State != StatePending {
		t.Errorf("State = %q, want pending", s.State)
	}

	h.advance(time.Minute)
	if len(h.fake.Calls()) != 1 {
		t.Error("the action did not run at its deadline")
	}
}

func TestScheduleStripsTheMonotonicReading(t *testing.T) {
	m := New(power.NewFake())
	firesAt := time.Now().Add(time.Hour) // monotonic-carrying, as the API layer's is
	if _, err := m.Schedule(context.Background(), power.ActionShutdown, true, firesAt); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	m.mu.Lock()
	stored := m.firesAt
	m.mu.Unlock()
	if stored.Round(0) != stored {
		t.Error("firesAt kept its monotonic reading, so a deadline that passed while the machine was suspended will never fire")
	}
}

// blockingController holds Execute open so the Executing state is observable.
type blockingController struct {
	release chan struct{}
	entered chan struct{}
}

func (b *blockingController) Execute(context.Context, power.Action, bool) error {
	close(b.entered)
	<-b.release
	return nil
}

func (b *blockingController) Capabilities(context.Context) (power.Capabilities, error) {
	return power.Capabilities{Sleep: true, Hibernate: true}, nil
}

// panickingController stands in for the Windows power path, where
// LazyProc.Call panics if a DLL export cannot be resolved.
type panickingController struct{}

func (panickingController) Execute(context.Context, power.Action, bool) error {
	panic("Failed to find SetSuspendState procedure in powrprof.dll")
}

func (panickingController) Capabilities(context.Context) (power.Capabilities, error) {
	return power.Capabilities{Sleep: true, Hibernate: true}, nil
}

func TestAPanicDuringExecutionBecomesAFailure(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	m := New(panickingController{}, WithClock(func() time.Time { return now }))

	// A deadline equal to now fires on the next tick.
	if _, err := m.Schedule(context.Background(), power.ActionShutdown, true, now); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}

	// The process must survive this: firing happens on the tick goroutine,
	// outside the HTTP server's recoverPanic middleware.
	m.Tick()

	s := m.Status()
	if s.State != StateFailed {
		t.Fatalf("State = %q, want failed after a panicking Execute", s.State)
	}
	if s.Error == "" {
		t.Error("Status().Error is empty, so the panic is invisible in the UI")
	}
	if !strings.Contains(s.Error, "powrprof.dll") {
		t.Errorf("Error = %q, want it to name the panic", s.Error)
	}

	// And the manager must still be usable rather than wedged in executing.
	if _, err := m.Schedule(context.Background(), power.ActionRestart, true, now); err != nil {
		t.Errorf("Schedule() after a panic error = %v, want nil", err)
	}
}

// TestStartStopDoesNotBlockOnAnExecutingAction pins that stop() returns even
// while the tick goroutine is wedged inside Execute for a sleep/hibernate that
// has not resumed yet. A stop that waited for the goroutine to exit would hang
// service shutdown for as long as the machine stays suspended.
func TestStartStopDoesNotBlockOnAnExecutingAction(t *testing.T) {
	ctrl := &blockingController{release: make(chan struct{}), entered: make(chan struct{})}
	m := New(ctrl)

	if _, err := m.Schedule(context.Background(), power.ActionShutdown, true, time.Now()); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}

	stop := m.Start(time.Millisecond)
	<-ctrl.entered

	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stop() blocked behind a Tick stuck inside Execute")
	}

	close(ctrl.release)
}

func TestScheduleRejectedWhileExecuting(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	ctrl := &blockingController{release: make(chan struct{}), entered: make(chan struct{})}
	m := New(ctrl, WithClock(func() time.Time { return now }))

	if _, err := m.Schedule(context.Background(), power.ActionShutdown, true, now); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	// Tick blocks for as long as Execute does, which is the whole point of this
	// test, so it runs on its own goroutine.
	go m.Tick()
	<-ctrl.entered

	if s := m.Status(); s.State != StateExecuting {
		t.Errorf("State = %q, want executing", s.State)
	}
	if _, err := m.Schedule(context.Background(), power.ActionRestart, true, now); !errors.Is(err, ErrConflict) {
		t.Errorf("Schedule() while executing error = %v, want ErrConflict", err)
	}
	close(ctrl.release)
}

func TestADeadlineWithinTheGraceWindowStillFires(t *testing.T) {
	h := newHarness(t)
	if _, err := h.schedule(t, power.ActionShutdown, true, time.Hour); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}

	// The machine was busy, or suspended briefly, and the tick lands late.
	h.advance(time.Hour + 4*time.Minute)

	if len(h.fake.Calls()) != 1 {
		t.Errorf("Calls() = %+v, want the action to have fired", h.fake.Calls())
	}
}

func TestADeadlineBeyondTheGraceWindowIsMissed(t *testing.T) {
	h := newHarness(t)
	if _, err := h.schedule(t, power.ActionSleep, false, time.Hour); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}

	// The machine was asleep across the deadline and resumed hours later.
	h.advance(6 * time.Hour)

	if len(h.fake.Calls()) != 0 {
		t.Fatal("a missed action was executed; the machine would suspend on resume")
	}
	s := h.mgr.Status()
	if s.State != StateMissed {
		t.Fatalf("State = %q, want missed", s.State)
	}
	if s.Missed == nil {
		t.Fatal("Status().Missed = nil, want the skipped action reported")
	}
	if s.Missed.Action != power.ActionSleep {
		t.Errorf("Missed.Action = %q, want sleep", s.Missed.Action)
	}
	if s.Pending != nil {
		t.Error("Status().Pending is set for a missed action")
	}
}

func TestMissedIsSchedulable(t *testing.T) {
	h := newHarness(t)
	if _, err := h.schedule(t, power.ActionSleep, false, time.Hour); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	h.advance(6 * time.Hour)
	if h.mgr.Status().State != StateMissed {
		t.Fatal("setup: expected a missed action")
	}

	// A skipped action must not leave the machine unable to accept the next one.
	if _, err := h.schedule(t, power.ActionShutdown, true, time.Minute); err != nil {
		t.Fatalf("Schedule() after a miss error = %v, want nil", err)
	}
	if s := h.mgr.Status(); s.Missed != nil {
		t.Error("scheduling a new action did not clear the missed record")
	}
}

func TestDismissClearsAMissedRecord(t *testing.T) {
	h := newHarness(t)
	if _, err := h.schedule(t, power.ActionSleep, false, time.Hour); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	h.advance(6 * time.Hour)

	h.mgr.Dismiss()

	s := h.mgr.Status()
	if s.State != StateIdle || s.Missed != nil {
		t.Errorf("Status() = %+v, want idle with no missed record", s)
	}
	// Idempotent: a second tab racing the first must not produce an error.
	h.mgr.Dismiss()
}
