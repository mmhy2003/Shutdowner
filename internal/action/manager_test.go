package action

import (
	"context"
	"errors"
	"testing"
	"time"

	"shutdowner/internal/power"
)

// manualTimer replaces time.AfterFunc so tests fire the countdown immediately
// instead of waiting for it.
type manualTimer struct {
	fn      func()
	stopped bool
}

func (t *manualTimer) Stop() bool {
	t.stopped = true
	return true
}

type harness struct {
	mgr   *Manager
	fake  *power.Fake
	timer *manualTimer
	now   time.Time
	ids   int
}

func newHarness(t *testing.T, delay time.Duration) *harness {
	t.Helper()
	h := &harness{
		fake: power.NewFake(),
		now:  time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
	}
	h.mgr = New(h.fake, delay,
		WithClock(func() time.Time { return h.now }),
		WithAfterFunc(func(_ time.Duration, fn func()) Timer {
			h.timer = &manualTimer{fn: fn}
			return h.timer
		}),
		WithIDFunc(func() string {
			h.ids++
			return "id-" + string(rune('a'+h.ids-1))
		}),
	)
	return h
}

// fire runs the scheduled callback, standing in for the countdown elapsing.
func (h *harness) fire(t *testing.T) {
	t.Helper()
	if h.timer == nil {
		t.Fatal("no timer was scheduled")
	}
	h.timer.fn()
}

func TestScheduleFromIdle(t *testing.T) {
	h := newHarness(t, 45*time.Second)

	p, err := h.mgr.Schedule(context.Background(), power.ActionShutdown, true)
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
	if s := h.mgr.Status(); s.State != StatePending {
		t.Errorf("State = %q, want pending", s.State)
	}
	if len(h.fake.Calls()) != 0 {
		t.Error("the controller was called before the countdown elapsed")
	}
}

func TestScheduleRejectsASecondAction(t *testing.T) {
	h := newHarness(t, 45*time.Second)
	if _, err := h.mgr.Schedule(context.Background(), power.ActionShutdown, true); err != nil {
		t.Fatalf("first Schedule() error = %v", err)
	}
	if _, err := h.mgr.Schedule(context.Background(), power.ActionRestart, true); !errors.Is(err, ErrConflict) {
		t.Errorf("second Schedule() error = %v, want ErrConflict", err)
	}
}

func TestScheduleRejectsAnInvalidAction(t *testing.T) {
	h := newHarness(t, 45*time.Second)
	if _, err := h.mgr.Schedule(context.Background(), power.Action("explode"), false); !errors.Is(err, ErrInvalidAction) {
		t.Errorf("Schedule() error = %v, want ErrInvalidAction", err)
	}
}

func TestScheduleRejectsUnavailableSuspendActions(t *testing.T) {
	h := newHarness(t, 45*time.Second)
	h.fake.SetCapabilities(power.Capabilities{Sleep: true})

	if _, err := h.mgr.Schedule(context.Background(), power.ActionHibernate, false); !errors.Is(err, ErrUnsupportedAction) {
		t.Errorf("hibernate Schedule() error = %v, want ErrUnsupportedAction", err)
	}
	if _, err := h.mgr.Schedule(context.Background(), power.ActionSleep, false); err != nil {
		t.Errorf("sleep Schedule() error = %v, want nil", err)
	}
}

func TestShutdownIsNeverCapabilityGated(t *testing.T) {
	h := newHarness(t, 45*time.Second)
	h.fake.SetCapabilities(power.Capabilities{})

	if _, err := h.mgr.Schedule(context.Background(), power.ActionShutdown, true); err != nil {
		t.Errorf("Schedule(shutdown) error = %v, want nil even with no capabilities", err)
	}
}

func TestFiringExecutesTheAction(t *testing.T) {
	h := newHarness(t, 45*time.Second)
	if _, err := h.mgr.Schedule(context.Background(), power.ActionRestart, false); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}

	h.fire(t)

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
	h := newHarness(t, 45*time.Second)
	h.fake.SetError(errors.New("shutdown.exe exited 1"))

	if _, err := h.mgr.Schedule(context.Background(), power.ActionShutdown, true); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	h.fire(t)

	s := h.mgr.Status()
	if s.State != StateFailed {
		t.Fatalf("State = %q, want failed", s.State)
	}
	if s.Error != "shutdown.exe exited 1" {
		t.Errorf("Error = %q, want the underlying message", s.Error)
	}
}

func TestFailedRevertsToIdle(t *testing.T) {
	h := newHarness(t, 45*time.Second)
	h.fake.SetError(errors.New("nope"))
	if _, err := h.mgr.Schedule(context.Background(), power.ActionShutdown, true); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	h.fire(t)

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
	h := newHarness(t, 45*time.Second)
	h.fake.SetError(errors.New("nope"))
	if _, err := h.mgr.Schedule(context.Background(), power.ActionShutdown, true); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	h.fire(t)
	if s := h.mgr.Status(); s.State != StateFailed {
		t.Fatalf("State = %q, want failed", s.State)
	}

	h.fake.SetError(nil)
	if _, err := h.mgr.Schedule(context.Background(), power.ActionRestart, true); err != nil {
		t.Errorf("Schedule() from failed error = %v, want nil", err)
	}
}

func TestAbortCancelsAPendingAction(t *testing.T) {
	h := newHarness(t, 45*time.Second)
	p, err := h.mgr.Schedule(context.Background(), power.ActionShutdown, true)
	if err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}

	if err := h.mgr.Abort(p.ID); err != nil {
		t.Fatalf("Abort() error = %v, want nil", err)
	}
	if !h.timer.stopped {
		t.Error("the timer was not stopped")
	}
	if s := h.mgr.Status(); s.State != StateIdle {
		t.Errorf("State = %q, want idle", s.State)
	}

	// Even if the timer had already been racing towards firing, a stale
	// callback must not execute the aborted action.
	h.fire(t)
	if len(h.fake.Calls()) != 0 {
		t.Error("an aborted action still executed")
	}
}

func TestAbortRejectsAStaleID(t *testing.T) {
	h := newHarness(t, 45*time.Second)
	if _, err := h.mgr.Schedule(context.Background(), power.ActionShutdown, true); err != nil {
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
	h := newHarness(t, 45*time.Second)
	if err := h.mgr.Abort("anything"); !errors.Is(err, ErrNoPending) {
		t.Errorf("Abort() error = %v, want ErrNoPending", err)
	}
}

func TestRemainingSecondsCountsDown(t *testing.T) {
	h := newHarness(t, 45*time.Second)
	if _, err := h.mgr.Schedule(context.Background(), power.ActionShutdown, true); err != nil {
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
	h := newHarness(t, 45*time.Second)
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

func TestScheduleRejectedWhileExecuting(t *testing.T) {
	ctrl := &blockingController{release: make(chan struct{}), entered: make(chan struct{})}
	var timer *manualTimer
	m := New(ctrl, time.Second, WithAfterFunc(func(_ time.Duration, fn func()) Timer {
		timer = &manualTimer{fn: fn}
		return timer
	}))

	if _, err := m.Schedule(context.Background(), power.ActionShutdown, true); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	go timer.fn()
	<-ctrl.entered

	if s := m.Status(); s.State != StateExecuting {
		t.Errorf("State = %q, want executing", s.State)
	}
	if _, err := m.Schedule(context.Background(), power.ActionRestart, true); !errors.Is(err, ErrConflict) {
		t.Errorf("Schedule() while executing error = %v, want ErrConflict", err)
	}
	close(ctrl.release)
}
