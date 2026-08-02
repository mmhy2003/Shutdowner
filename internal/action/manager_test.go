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

// memStore is an in-memory Store, so manager tests never touch a disk.
type memStore struct {
	saved Persisted
	err   error
}

func (s *memStore) Load() (Persisted, error) { return s.saved, s.err }
func (s *memStore) Save(p Persisted) error   { s.saved = p; return nil }
func (s *memStore) Clear() error             { s.saved = Persisted{}; return nil }

func TestScheduleIsPersistedAndClearedOnAbort(t *testing.T) {
	h := newHarness(t)
	store := &memStore{}
	h.mgr = New(h.fake, WithClock(func() time.Time { return h.now }),
		WithIDFunc(func() string { return "id-a" }), WithStore(store))

	p, err := h.schedule(t, power.ActionShutdown, true, time.Hour)
	if err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	if store.saved.Pending == nil || store.saved.Pending.ID != p.ID {
		t.Fatalf("saved = %+v, want the pending action", store.saved)
	}

	if err := h.mgr.Abort(p.ID); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
	if store.saved.Pending != nil {
		t.Error("aborting did not clear the persisted schedule")
	}
}

func TestRestoreReArmsADeadlineStillAhead(t *testing.T) {
	h := newHarness(t)
	store := &memStore{saved: Persisted{Pending: &PersistedPending{
		ID: "id-restored", Action: power.ActionShutdown, Force: true,
		FiresAt: h.now.Add(time.Hour),
	}}}
	h.mgr = New(h.fake, WithClock(func() time.Time { return h.now }), WithStore(store))

	if err := h.mgr.Restore(); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}

	s := h.mgr.Status()
	if s.State != StatePending || s.Pending == nil || s.Pending.ID != "id-restored" {
		t.Fatalf("Status() = %+v, want the restored action pending", s)
	}
	h.advance(time.Hour)
	if len(h.fake.Calls()) != 1 {
		t.Error("the restored action did not fire at its deadline")
	}
}

func TestRestoreMissesADeadlineLongPast(t *testing.T) {
	h := newHarness(t)
	store := &memStore{saved: Persisted{Pending: &PersistedPending{
		ID: "id-stale", Action: power.ActionShutdown, Force: true,
		FiresAt: h.now.Add(-2 * time.Hour),
	}}}
	h.mgr = New(h.fake, WithClock(func() time.Time { return h.now }), WithStore(store))

	if err := h.mgr.Restore(); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}

	if len(h.fake.Calls()) != 0 {
		t.Fatal("a deadline missed while the service was down was executed at startup")
	}
	s := h.mgr.Status()
	if s.State != StateMissed || s.Missed == nil || s.Missed.Action != power.ActionShutdown {
		t.Errorf("Status() = %+v, want a missed shutdown", s)
	}
}

func TestRestoreReportsAStoreFailure(t *testing.T) {
	h := newHarness(t)
	store := &memStore{err: errors.New("corrupt")}
	h.mgr = New(h.fake, WithClock(func() time.Time { return h.now }), WithStore(store))

	if err := h.mgr.Restore(); err == nil {
		t.Error("Restore() error = nil, want the corrupt state reported so main can log it")
	}
	// The manager must still be usable: a bad state file cannot stop the service.
	if s := h.mgr.Status(); s.State != StateIdle {
		t.Errorf("State = %q, want idle after a failed restore", s.State)
	}
}

// TestRestoreClearsAStaleMissedRecordWhenReArming pins that a re-armed deadline
// and a missed record never coexist. Schedule enforces that everywhere else,
// and Status exposes Missed regardless of state, so a stale Missed left over in
// the persisted file must not survive alongside the pending action Restore just
// re-armed.
func TestRestoreClearsAStaleMissedRecordWhenReArming(t *testing.T) {
	h := newHarness(t)
	store := &memStore{saved: Persisted{
		Missed: &Missed{Action: power.ActionSleep, WasDueAt: h.now.Format(time.RFC3339)},
		Pending: &PersistedPending{
			ID: "id-restored", Action: power.ActionShutdown, Force: true,
			FiresAt: h.now.Add(time.Hour),
		},
	}}
	h.mgr = New(h.fake, WithClock(func() time.Time { return h.now }), WithStore(store))

	if err := h.mgr.Restore(); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}

	s := h.mgr.Status()
	if s.State != StatePending || s.Missed != nil {
		t.Fatalf("Status() = %+v, want pending with no missed record", s)
	}
}

// TestFailedExecutionClearsThePersistedSchedule pins the actual consequence a
// stale on-disk record would have: without clearing it, a restart within
// MissedGrace of a failed attempt silently re-runs a power action nobody
// confirmed a second time.
func TestFailedExecutionClearsThePersistedSchedule(t *testing.T) {
	h := newHarness(t)
	store := &memStore{}
	h.mgr = New(h.fake, WithClock(func() time.Time { return h.now }),
		WithIDFunc(func() string { return "id-a" }), WithStore(store))
	h.fake.SetError(errors.New("shutdown.exe exited 1"))

	if _, err := h.schedule(t, power.ActionShutdown, true, 45*time.Second); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	h.advance(45 * time.Second)

	if s := h.mgr.Status(); s.State != StateFailed {
		t.Fatalf("State = %q, want failed", s.State)
	}
	if store.saved.Pending != nil {
		t.Fatalf("saved = %+v, want the failed deadline cleared from disk", store.saved)
	}

	fresh := New(h.fake, WithClock(func() time.Time { return h.now }), WithStore(store))
	if err := fresh.Restore(); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if s := fresh.Status(); s.State == StatePending {
		t.Errorf("Status() = %+v, want the failed action not re-armed after a restart", s)
	}
}

// TestClaimingAnActionClearsThePersistedScheduleBeforeItRuns pins the actual
// consequence of persisting on completion instead of on claim: the service is
// installed with StartAutomatic, so it returns at boot. If the on-disk record
// still says "pending" for the whole duration Execute runs, a restart landing
// in that window — a scheduled RESTART rebooting the machine mid-Execute is
// exactly such a restart — re-arms the same deadline and fires it again. This
// schedules an action, fires it against a controller that blocks inside
// Execute, and asserts that the store no longer holds a pending record while
// Execute is still running, not only after it returns.
func TestClaimingAnActionClearsThePersistedScheduleBeforeItRuns(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	ctrl := &blockingController{release: make(chan struct{}), entered: make(chan struct{})}
	store := &memStore{}
	m := New(ctrl, WithClock(func() time.Time { return now }), WithStore(store))

	if _, err := m.Schedule(context.Background(), power.ActionRestart, true, now); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	if store.saved.Pending == nil {
		t.Fatal("setup: Schedule() did not persist the pending action")
	}

	// Tick blocks for as long as Execute does, so it runs on its own goroutine;
	// <-ctrl.entered proves Execute has started and not yet returned.
	go m.Tick()
	<-ctrl.entered

	if store.saved.Pending != nil {
		t.Errorf("saved = %+v, want the pending record cleared before Execute returns, "+
			"not after — a restart landing here must not re-arm it", store.saved)
	}

	close(ctrl.release)
}

// TestADeadlineExactlyAtMissedGraceStillFires pins the inclusive MissedGrace
// boundary in Tick: the comparison is now.Sub(firesAt) > MissedGrace, so an
// exact match still fires rather than being reported as missed.
func TestADeadlineExactlyAtMissedGraceStillFires(t *testing.T) {
	h := newHarness(t)
	if _, err := h.schedule(t, power.ActionShutdown, true, time.Hour); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}

	h.advance(time.Hour + MissedGrace)

	if len(h.fake.Calls()) != 1 {
		t.Errorf("Calls() = %+v, want the action to fire exactly at the grace boundary", h.fake.Calls())
	}
}

// TestRestoreReArmsADeadlineExactlyAtMissedGrace mirrors the Tick boundary test
// for Restore's identical comparison.
func TestRestoreReArmsADeadlineExactlyAtMissedGrace(t *testing.T) {
	h := newHarness(t)
	store := &memStore{saved: Persisted{Pending: &PersistedPending{
		ID: "id-boundary", Action: power.ActionShutdown, Force: true,
		FiresAt: h.now.Add(-MissedGrace),
	}}}
	h.mgr = New(h.fake, WithClock(func() time.Time { return h.now }), WithStore(store))

	if err := h.mgr.Restore(); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}

	if s := h.mgr.Status(); s.State != StatePending {
		t.Errorf("State = %q, want pending: a deadline exactly MissedGrace past re-arms rather than misses", s.State)
	}
}

// TestRestoreReArmingPersistsTheClearedMissedRecord pins that clearing a stale
// Missed in memory during a re-arm also reaches disk. Without the write, the
// file still names the old miss, and a restart before the next
// Schedule/Abort/Tick would restore it and disagree with everything Status
// has reported since the first restore.
func TestRestoreReArmingPersistsTheClearedMissedRecord(t *testing.T) {
	h := newHarness(t)
	store := &memStore{saved: Persisted{
		Missed: &Missed{Action: power.ActionSleep, WasDueAt: h.now.Format(time.RFC3339)},
		Pending: &PersistedPending{
			ID: "id-restored", Action: power.ActionShutdown, Force: true,
			FiresAt: h.now.Add(time.Hour),
		},
	}}
	h.mgr = New(h.fake, WithClock(func() time.Time { return h.now }), WithStore(store))

	if err := h.mgr.Restore(); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}

	if store.saved.Missed != nil {
		t.Errorf("saved = %+v, want the stale missed record cleared from disk, not just in memory", store.saved)
	}
	if store.saved.Pending == nil || store.saved.Pending.ID != "id-restored" {
		t.Errorf("saved = %+v, want the re-armed pending action persisted", store.saved)
	}
}

// TestRestoreRejectsAnInvalidRestoredAction pins that Restore validates a
// restored action name instead of arming whatever a corrupted or hand-edited
// state file names. An action that fails Valid() would otherwise sit armed
// until it fires and fails unpredictably deep inside execute.
func TestRestoreRejectsAnInvalidRestoredAction(t *testing.T) {
	h := newHarness(t)
	store := &memStore{saved: Persisted{Pending: &PersistedPending{
		ID: "id-bad", Action: power.Action("explode"), Force: true,
		FiresAt: h.now.Add(time.Hour),
	}}}
	h.mgr = New(h.fake, WithClock(func() time.Time { return h.now }), WithStore(store))

	if err := h.mgr.Restore(); err == nil {
		t.Error("Restore() error = nil, want an error reported for an invalid restored action")
	}

	s := h.mgr.Status()
	if s.State != StateIdle {
		t.Errorf("State = %q, want idle rather than an invalid action armed", s.State)
	}
	if store.saved.Pending != nil {
		t.Errorf("saved = %+v, want the invalid schedule cleared from disk", store.saved)
	}
}

// TestRestoreRejectsADeadlineBeyondMaxHorizon pins the other half of finding
// 5: a deadline further out than MaxHorizon — from a hand-edited or corrupted
// file — must not be armed either. Restore's own missed-vs-pending comparison
// only catches deadlines in the past; nothing previously stopped one far in
// the future from being armed and permanently occupying the single schedule
// slot with ErrConflict until an operator noticed and aborted it.
func TestRestoreRejectsADeadlineBeyondMaxHorizon(t *testing.T) {
	h := newHarness(t)
	store := &memStore{saved: Persisted{Pending: &PersistedPending{
		ID: "id-toofar", Action: power.ActionShutdown, Force: true,
		FiresAt: h.now.Add(MaxHorizon + time.Hour),
	}}}
	h.mgr = New(h.fake, WithClock(func() time.Time { return h.now }), WithStore(store))

	if err := h.mgr.Restore(); err == nil {
		t.Error("Restore() error = nil, want an error reported for a deadline beyond MaxHorizon")
	}

	s := h.mgr.Status()
	if s.State != StateIdle {
		t.Errorf("State = %q, want idle rather than a too-distant deadline permanently occupying the slot", s.State)
	}
	if store.saved.Pending != nil {
		t.Errorf("saved = %+v, want the invalid schedule cleared from disk", store.saved)
	}

	// The manager must still be usable: rejecting the bad file must not wedge
	// the single slot behind ErrConflict.
	if _, err := h.mgr.Schedule(context.Background(), power.ActionRestart, true, h.now.Add(time.Hour)); err != nil {
		t.Errorf("Schedule() after rejecting a restored schedule error = %v, want nil", err)
	}
}

// TestRestoreConvertsRestoredTimesToTheCurrentZone pins finding 6: a restored
// deadline must be reformatted with the PC's current offset, not the one it
// was saved with, or a DST change between saving and loading would show the
// wrong hour in FiresAtLocal (and, on the missed path, WasDueAt). This uses a
// fixed offset far from the test process's own zone so a Location() that
// silently stayed unconverted would show up as a wrong offset in the
// formatted string.
func TestRestoreConvertsRestoredTimesToTheCurrentZone(t *testing.T) {
	fixed := time.FixedZone("FIXED+05", 5*3600)
	h := newHarness(t)
	firesAt := h.now.Add(time.Hour).In(fixed)
	store := &memStore{saved: Persisted{Pending: &PersistedPending{
		ID: "id-restored", Action: power.ActionShutdown, Force: true,
		FiresAt: firesAt,
	}}}
	h.mgr = New(h.fake, WithClock(func() time.Time { return h.now }), WithStore(store))

	if err := h.mgr.Restore(); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}

	h.mgr.mu.Lock()
	gotLocation := h.mgr.firesAt.Location()
	h.mgr.mu.Unlock()
	if gotLocation == fixed {
		t.Error("firesAt kept the offset it was saved with instead of being converted with .Local()")
	}
}
