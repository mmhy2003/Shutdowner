# Scheduled Power Actions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the operator schedule any of the four power actions for a chosen duration ("in 2 hours") or a chosen wall-clock time ("at 03:00") on the PC's own clock, surviving a service restart.

**Architecture:** `action.Manager` stops sleeping for a duration and instead stores an absolute deadline that a one-second tick compares against the wall clock, firing only if the deadline is less than five minutes past. A small JSON file beside the executable persists the schedule so a restart restores it. Resolution of "when" is a pure function; persistence is an interface.

**Tech Stack:** Go 1.25, stdlib only (`encoding/json`, `time`, `sync`). Frontend is `html/template` plus vanilla JS in `internal/web/static/app.js`. No new dependencies.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-08-02-scheduled-actions-design.md`. Read it before Task 1.
- `MissedGrace = 5 * time.Minute`. A deadline further past than this is never executed.
- `MaxHorizon = 7 * 24 * time.Hour` (604800 seconds).
- `AtLayout = "2006-01-02T15:04"` — naive, no offset, interpreted in the server's location.
- `TickInterval = time.Second`.
- Existing behaviour must not change when a request carries neither `delaySeconds` nor `at`.
- One schedule at a time. `ErrConflict` semantics are unchanged.
- No inline `<script>` or `<style>`: the CSP is `script-src 'self'; style-src 'self'`.
- Run `make test` and `make vet` before every commit. `make vet` runs `go vet` for both the host and `GOOS=windows`.
- `TestWriteStarterEnv` in `cmd/shutdowner` fails on Windows for unrelated reasons (expects mode 600, Windows reports 666). It was failing before this work. Do not "fix" it; do not let it mask a real failure.

---

## File Structure

| File | Responsibility | Status |
|---|---|---|
| `internal/action/when.go` | `ResolveWhen` — request fields to an absolute deadline, with bounds | Create |
| `internal/action/when_test.go` | Table tests for resolution and every bound | Create |
| `internal/action/store.go` | `Store` interface, the `schedule.json` file implementation | Create |
| `internal/action/store_test.go` | Round trip, absent file, corrupt file, atomic replace | Create |
| `internal/action/manager.go` | State machine, tick, miss rule, restore | Modify |
| `internal/action/manager_test.go` | Harness moves from `WithAfterFunc` to direct `Tick()` | Modify |
| `internal/web/api_handlers.go` | Request fields, validation, `handleDismiss` | Modify |
| `internal/web/api_handlers_test.go` | Validation rules, status shape | Modify |
| `internal/web/server.go` | `/api/dismiss` route, fallback delay on `Server` | Modify |
| `internal/web/templates/dashboard.html` | `When` fieldset, missed banner | Modify |
| `internal/web/static/app.js` | When controls, PC-clock arithmetic, formatting, dismiss | Modify |
| `internal/web/static/app.css` | Fieldset and banner styling | Modify |
| `cmd/shutdowner/main.go` | Construct the store, restore at startup, start the tick | Modify |

`manager.go` is around 250 lines already. Resolution and persistence go in their own files rather than growing it into a state machine that also owns a codec and a file.

---

## Task 1: Resolve "when" into a deadline

Pure function, no manager and no HTTP. Everything about bounds and timezones is decided here and tested here.

**Files:**
- Create: `internal/action/when.go`
- Test: `internal/action/when_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `func ResolveWhen(now time.Time, delaySeconds *int, at string, fallback time.Duration) (time.Time, error)`
  - `const AtLayout = "2006-01-02T15:04"`
  - `const MaxHorizon = 7 * 24 * time.Hour`
  - `var ErrConflictingWhen, ErrDelayRange, ErrBadAtFormat, ErrAtInPast, ErrAtTooFar error`

- [ ] **Step 1: Write the failing test**

Create `internal/action/when_test.go`:

```go
package action

import (
	"errors"
	"testing"
	"time"
)

func intPtr(n int) *int { return &n }

// A fixed non-UTC location, so a bug that resolves "at" in UTC instead of the
// server's location shows up as a three-hour error rather than passing.
var testLoc = time.FixedZone("test", 3*60*60)

func TestResolveWhen(t *testing.T) {
	now := time.Date(2026, 8, 2, 14, 30, 0, 0, testLoc)
	fallback := 45 * time.Second

	tests := []struct {
		name         string
		delaySeconds *int
		at           string
		want         time.Time
		wantErr      error
	}{
		{
			name: "neither field falls back to the configured delay",
			want: now.Add(45 * time.Second),
		},
		{
			name:         "delaySeconds is relative to now",
			delaySeconds: intPtr(7200),
			want:         now.Add(2 * time.Hour),
		},
		{
			name:         "a zero delay is legal and means now",
			delaySeconds: intPtr(0),
			want:         now,
		},
		{
			name: "at is interpreted in the server's location",
			at:   "2026-08-03T03:00",
			want: time.Date(2026, 8, 3, 3, 0, 0, 0, testLoc),
		},
		{
			name: "at may be later the same day",
			at:   "2026-08-02T23:15",
			want: time.Date(2026, 8, 2, 23, 15, 0, 0, testLoc),
		},
		{
			name:         "both fields is a conflict",
			delaySeconds: intPtr(60),
			at:           "2026-08-03T03:00",
			wantErr:      ErrConflictingWhen,
		},
		{
			name:         "a negative delay is refused",
			delaySeconds: intPtr(-1),
			wantErr:      ErrDelayRange,
		},
		{
			name:         "a delay past the horizon is refused",
			delaySeconds: intPtr(int(MaxHorizon/time.Second) + 1),
			wantErr:      ErrDelayRange,
		},
		{
			name:         "a delay exactly at the horizon is allowed",
			delaySeconds: intPtr(int(MaxHorizon / time.Second)),
			want:         now.Add(MaxHorizon),
		},
		{
			name:    "an unparseable at is refused",
			at:      "tomorrow please",
			wantErr: ErrBadAtFormat,
		},
		{
			name:    "an at carrying an offset is refused, since it is meant to be naive",
			at:      "2026-08-03T03:00:00+05:00",
			wantErr: ErrBadAtFormat,
		},
		{
			name:    "an at in the past is refused",
			at:      "2026-08-02T14:29",
			wantErr: ErrAtInPast,
		},
		{
			name:    "an at past the horizon is refused",
			at:      "2026-08-10T00:00",
			wantErr: ErrAtTooFar,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveWhen(now, tt.delaySeconds, tt.at, fallback)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ResolveWhen() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveWhen() error = %v, want nil", err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("ResolveWhen() = %s, want %s", got, tt.want)
			}
		})
	}
}

// The error text is what the operator sees when their phone and the PC
// disagree about the time, so it has to name the PC's clock.
func TestResolveWhenPastErrorQuotesThePCsClock(t *testing.T) {
	now := time.Date(2026, 8, 2, 14, 30, 0, 0, testLoc)
	_, err := ResolveWhen(now, nil, "2026-08-02T09:00", 45*time.Second)
	if err == nil {
		t.Fatal("ResolveWhen() error = nil, want a past-time error")
	}
	if !strings.Contains(err.Error(), "14:30") {
		t.Errorf("error %q does not quote the PC's current time", err)
	}
}
```

Add `"strings"` to that file's imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/action/ -run TestResolveWhen -v`
Expected: FAIL to build — `undefined: ResolveWhen`, `undefined: ErrConflictingWhen`, and so on.

- [ ] **Step 3: Write the implementation**

Create `internal/action/when.go`:

```go
package action

import (
	"errors"
	"fmt"
	"time"
)

// AtLayout is the shape an absolute schedule arrives in: a naive wall clock
// with no offset, which is exactly what <input type="datetime-local"> produces.
// The absence of an offset is the point — the time means what it says on the
// PC's clock, not on the clock of whatever device is holding the browser.
const AtLayout = "2006-01-02T15:04"

// MaxHorizon caps how far ahead an action may be scheduled. A schedule further
// out than a week is likelier to be forgotten than honoured.
const MaxHorizon = 7 * 24 * time.Hour

var (
	ErrConflictingWhen = errors.New("action: give either delaySeconds or at, not both")
	ErrDelayRange      = fmt.Errorf("action: delaySeconds must be between 0 and %d", int(MaxHorizon/time.Second))
	ErrBadAtFormat     = fmt.Errorf("action: at must look like %s, with no timezone", AtLayout)
	ErrAtInPast        = errors.New("action: at is in the past")
	ErrAtTooFar        = fmt.Errorf("action: at is more than %d days ahead", int(MaxHorizon/(24*time.Hour)))
)

// ResolveWhen turns a request's optional timing fields into the absolute
// instant the action should fire. Both fields absent means the configured
// fallback delay, which is what keeps every existing caller working unchanged.
//
// now carries the server's location, and at is parsed in it. That single
// detail is the whole of the "the PC's clock decides" decision.
func ResolveWhen(now time.Time, delaySeconds *int, at string, fallback time.Duration) (time.Time, error) {
	if delaySeconds != nil && at != "" {
		return time.Time{}, ErrConflictingWhen
	}

	switch {
	case delaySeconds != nil:
		d := time.Duration(*delaySeconds) * time.Second
		if *delaySeconds < 0 || d > MaxHorizon {
			return time.Time{}, ErrDelayRange
		}
		return now.Add(d), nil

	case at != "":
		firesAt, err := time.ParseInLocation(AtLayout, at, now.Location())
		if err != nil {
			return time.Time{}, ErrBadAtFormat
		}
		if firesAt.Before(now) {
			// Quoting the PC's clock is what lets a timezone mix-up diagnose
			// itself: the operator sees the machine disagreeing with the phone.
			return time.Time{}, fmt.Errorf("%w; it is currently %s on the PC",
				ErrAtInPast, now.Format("2006-01-02 15:04"))
		}
		if firesAt.Sub(now) > MaxHorizon {
			return time.Time{}, ErrAtTooFar
		}
		return firesAt, nil

	default:
		return now.Add(fallback), nil
	}
}
```

Note `time.ParseInLocation` with `AtLayout` rejects a trailing offset on its own, because the layout has no offset to consume — no extra check is needed for that case.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/action/ -run TestResolveWhen -v`
Expected: PASS, every subtest.

- [ ] **Step 5: Commit**

```bash
git add internal/action/when.go internal/action/when_test.go
git commit -m "feat: resolve a relative or absolute schedule into a deadline"
```

---

## Task 2: Manager holds a deadline and ticks

Mechanical swap: the manager stops being handed a duration and stops using `time.AfterFunc`. Behaviour is unchanged — the miss rule arrives in Task 3. Existing tests keep their assertions and change only how firing is triggered.

**Files:**
- Modify: `internal/action/manager.go`
- Modify: `internal/action/manager_test.go`

**Interfaces:**
- Consumes: nothing from Task 1 yet.
- Produces:
  - `func New(ctrl power.Controller, opts ...Option) *Manager` — the `delay` parameter is gone
  - `func (m *Manager) Schedule(ctx context.Context, a power.Action, force bool, firesAt time.Time) (Pending, error)`
  - `func (m *Manager) Tick()`
  - `func (m *Manager) Start(interval time.Duration) (stop func())`
  - `const TickInterval = time.Second`
  - `Pending` gains `FiresAtLocal string \`json:"firesAtLocal"\``
  - `WithAfterFunc` and the `Timer` interface are deleted; `WithClock` and `WithIDFunc` stay

- [ ] **Step 1: Update the test harness to drive ticks directly**

Replace lines 13–60 of `internal/action/manager_test.go` (the `manualTimer` type, `harness`, `newHarness`, and `fire`) with:

```go
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
```

- [ ] **Step 2: Update every existing test to the new harness**

Mechanical, and the assertions do not change. `newHarness(t, 45*time.Second)` becomes `newHarness(t)`; every `h.mgr.Schedule(context.Background(), X, Y)` becomes `h.schedule(t, X, Y, 45*time.Second)`; every `h.fire(t)` becomes `h.advance(45 * time.Second)`.

For example `TestFiringExecutesTheAction` becomes:

```go
func TestFiringExecutesTheAction(t *testing.T) {
	h := newHarness(t)
	if _, err := h.schedule(t, power.ActionRestart, false, 45*time.Second); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}

	h.advance(45 * time.Second)

	calls := h.fake.Calls()
	if len(calls) != 1 || calls[0].Action != power.ActionRestart || calls[0].Force {
		t.Errorf("Calls() = %+v, want one graceful restart", calls)
	}
}
```

`TestAbortCancelsAPendingAction` needs its post-abort check changed from "the timer was stopped" to "ticking past the deadline does nothing":

```go
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
```

**Do not convert `TestRemainingSecondsCountsDown`.** It moves `h.now` forward by
hand and then calls `Status()` without firing, deliberately, to check that a
deadline already past reports `0` rather than a negative number. Using
`h.advance` there would fire the action, leave `Pending` nil, and crash the test
on the next line. Change only its `newHarness` and `Schedule` calls; leave both
`h.now = h.now.Add(...)` lines exactly as they are.

Two tests build a `Manager` directly rather than through the harness, so the
mechanical rule does not reach them. Rewrite them in full.

`TestAPanicDuringExecutionBecomesAFailure`:

```go
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
```

`TestScheduleRejectedWhileExecuting`:

```go
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
```

Add one test pinning that the deadline is respected rather than the first tick winning:

```go
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
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/action/ -v`
Expected: FAIL to build — `New` takes a delay, `Schedule` takes three arguments, `Tick` is undefined.

- [ ] **Step 4: Rewrite the manager**

In `internal/action/manager.go`:

Delete the `Timer` interface and `WithAfterFunc`. Add:

```go
// TickInterval is how often the manager compares the wall clock against the
// deadline. A tick is a mutex acquisition and a time comparison, so the cost is
// nothing next to not having to reason about the monotonic clock.
const TickInterval = time.Second
```

Change `Pending`:

```go
type Pending struct {
	ID     string       `json:"id"`
	Action power.Action `json:"action"`
	Force  bool         `json:"force"`
	// RemainingSeconds is relative rather than an absolute deadline so a client
	// with a skewed clock still renders an accurate countdown.
	RemainingSeconds int `json:"remainingSeconds"`
	// FiresAtLocal is the same instant on the PC's clock, for display only. The
	// browser must read its wall-clock fields as text: parsing it converts it to
	// the browser's timezone, which is the thing this design decided against.
	FiresAtLocal string `json:"firesAtLocal"`
}
```

Change the struct and constructor:

```go
type Manager struct {
	ctrl power.Controller

	now   func() time.Time
	newID func() string

	mu       sync.Mutex
	state    State
	id       string
	action   power.Action
	force    bool
	firesAt  time.Time
	lastErr  string
	failedAt time.Time
}

func New(ctrl power.Controller, opts ...Option) *Manager {
	m := &Manager{
		ctrl:  ctrl,
		now:   time.Now,
		newID: randomID,
		state: StateIdle,
	}
	for _, o := range opts {
		o(m)
	}
	return m
}
```

Change `Schedule` to take the deadline:

```go
// Schedule queues a to fire at firesAt. Capabilities are checked here rather
// than when the deadline arrives, so an unavailable action is refused
// immediately instead of failing silently hours later.
func (m *Manager) Schedule(ctx context.Context, a power.Action, force bool, firesAt time.Time) (Pending, error) {
	if !a.Valid() {
		return Pending{}, ErrInvalidAction
	}
	if a.Suspends() {
		caps, err := m.ctrl.Capabilities(ctx)
		if err != nil || !caps.Allows(a) {
			return Pending{}, ErrUnsupportedAction
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireFailedLocked()
	if m.state == StatePending || m.state == StateExecuting {
		return Pending{}, ErrConflict
	}

	m.state = StatePending
	m.id = m.newID()
	m.action = a
	m.force = force
	m.firesAt = firesAt
	m.lastErr = ""

	return m.pendingLocked(m.now()), nil
}
```

Replace `fire` with `Tick`:

```go
// Tick fires the pending action if its deadline has arrived. Start calls it
// once a second; tests call it directly, which is why it takes no arguments and
// reads the clock itself.
func (m *Manager) Tick() {
	m.mu.Lock()
	if m.state != StatePending || m.now().Before(m.firesAt) {
		m.mu.Unlock()
		return
	}
	m.state = StateExecuting
	a, force := m.action, m.force
	m.mu.Unlock()

	err := m.execute(a, force)

	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.state = StateFailed
		m.lastErr = err.Error()
		m.failedAt = m.now()
		return
	}
	// Sleep and hibernate reach here only once the machine has resumed, because
	// SetSuspendState blocks for the whole suspension. Shutdown and restart
	// never reach here at all: the process dies mid-call.
	m.state = StateIdle
	m.lastErr = ""
}

// Start runs the tick loop until the returned stop function is called.
//
// Stopping does not wait for an action already executing: a sleep blocks inside
// the controller for the entire suspension, and holding service shutdown open
// for that would be worse than letting the goroutine end on its own.
func (m *Manager) Start(interval time.Duration) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				m.Tick()
			case <-done:
				return
			}
		}
	}()
	return func() { close(done) }
}
```

`Abort` loses its `m.timer.Stop()` block — setting the state to idle is now the whole of cancelling, because `Tick` refuses any state but pending:

```go
func (m *Manager) Abort(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != StatePending || m.id != id {
		return ErrNoPending
	}
	m.state = StateIdle
	m.id = ""
	return nil
}
```

`pendingLocked` gains the display field:

```go
func (m *Manager) pendingLocked(now time.Time) Pending {
	remaining := int(m.firesAt.Sub(now) / time.Second)
	if remaining < 0 {
		remaining = 0
	}
	return Pending{
		ID:               m.id,
		Action:           m.action,
		Force:            m.force,
		RemainingSeconds: remaining,
		FiresAtLocal:     m.firesAt.Format(time.RFC3339),
	}
}
```

Update the `Manager` doc comment: the sentence about an action pending when the process dies being lost is about to stop being true, so replace it with "A pending action is persisted by the store and restored at startup; see Restore."

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/action/ -v`
Expected: PASS. Then `go build ./...` — expect a failure in `cmd/shutdowner` and `internal/web`, which still call the old `New` and `Schedule`. Fix both call sites minimally so the tree builds:

In `cmd/shutdowner/main.go`, `action.New(ctrl, cfg.Delay)` becomes `action.New(ctrl)`.

In `internal/web/api_handlers.go`, `s.actions.Schedule(r.Context(), req.Action, req.Force)` becomes `s.actions.Schedule(r.Context(), req.Action, req.Force, time.Now().Add(s.delay))`, and `Server` gains a `delay time.Duration` field set in `New` from `time.Duration(o.DelaySeconds) * time.Second`. Add `"time"` to the imports of both files.

- [ ] **Step 6: Run the whole suite**

Run: `make test && make vet`
Expected: everything passes except the known `TestWriteStarterEnv`.

- [ ] **Step 7: Commit**

```bash
git add internal/action/manager.go internal/action/manager_test.go internal/web/api_handlers.go internal/web/server.go cmd/shutdowner/main.go
git commit -m "refactor: drive the countdown from a deadline and a tick"
```

---

## Task 3: The miss rule

**Files:**
- Modify: `internal/action/manager.go`
- Modify: `internal/action/manager_test.go`

**Interfaces:**
- Consumes: `Tick`, `Schedule` from Task 2.
- Produces:
  - `const StateMissed State = "missed"`
  - `const MissedGrace = 5 * time.Minute`
  - `type Missed struct { Action power.Action \`json:"action"\`; WasDueAt string \`json:"wasDueAt"\` }`
  - `Status` gains `Missed *Missed \`json:"missed"\``
  - `func (m *Manager) Dismiss()`

- [ ] **Step 1: Write the failing tests**

Append to `internal/action/manager_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/action/ -run 'Missed|Grace|Dismiss' -v`
Expected: FAIL to build — `StateMissed`, `Status.Missed` and `Dismiss` are undefined.

- [ ] **Step 3: Implement the miss rule**

In `internal/action/manager.go`, add to the state constants and add the grace window:

```go
const (
	StateIdle      State = "idle"
	StatePending   State = "pending"
	StateExecuting State = "executing"
	StateFailed    State = "failed"
	StateMissed    State = "missed"
)

// MissedGrace is how far past its deadline an action may still fire. Beyond it
// the action is skipped and reported instead.
//
// The window exists because a deadline can go by while nothing is watching: the
// service was stopped, or Windows suspended the machine. Executing on the way
// back would mean shutting the PC down moments after somebody deliberately
// woke it, which is the worst outcome available here.
const MissedGrace = 5 * time.Minute

// Missed records an action whose deadline went by unobserved.
type Missed struct {
	Action   power.Action `json:"action"`
	WasDueAt string       `json:"wasDueAt"`
}
```

Add the field to `Status` and to `Manager`:

```go
type Status struct {
	State   State    `json:"state"`
	Pending *Pending `json:"pending"`
	Missed  *Missed  `json:"missed"`
	Error   string   `json:"error"`
}
```

```go
	failedAt time.Time
	missed   *Missed
```

In `Tick`, replace the early-return block:

```go
func (m *Manager) Tick() {
	m.mu.Lock()
	if m.state != StatePending {
		m.mu.Unlock()
		return
	}
	now := m.now()
	if now.Before(m.firesAt) {
		m.mu.Unlock()
		return
	}
	if now.Sub(m.firesAt) > MissedGrace {
		m.missLocked()
		m.mu.Unlock()
		return
	}
	m.state = StateExecuting
	...
```

Add:

```go
// missLocked converts the pending action into a missed record.
func (m *Manager) missLocked() {
	m.missed = &Missed{Action: m.action, WasDueAt: m.firesAt.Format(time.RFC3339)}
	m.state = StateMissed
	m.id = ""
}

// Dismiss clears a missed record. It is deliberately idempotent: two browser
// tabs racing each other should not produce an error anybody has to think about.
func (m *Manager) Dismiss() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == StateMissed {
		m.state = StateIdle
	}
	m.missed = nil
}
```

In `Schedule`, clear the record on a new schedule, just after the conflict check:

```go
	m.missed = nil
```

`StateMissed` is not in the conflict check, so it is schedulable exactly as `StateFailed` already is — no further change needed there.

In `Status`, report it:

```go
	s := Status{State: m.state, Missed: m.missed}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/action/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/action/manager.go internal/action/manager_test.go
git commit -m "feat: skip and report an action whose deadline went by unobserved"
```

---

## Task 4: The state file

Pure I/O behind an interface. Nothing here knows about the manager.

**Files:**
- Create: `internal/action/store.go`
- Test: `internal/action/store_test.go`

**Interfaces:**
- Consumes: `Missed` from Task 3, `power.Action`.
- Produces:
  - `type Persisted struct { Pending *PersistedPending; Missed *Missed }`
  - `type PersistedPending struct { ID string; Action power.Action; Force bool; FiresAt time.Time }`
  - `type Store interface { Load() (Persisted, error); Save(Persisted) error; Clear() error }`
  - `func NewFileStore(path string) *FileStore`
  - `type NopStore struct{}` implementing `Store`

- [ ] **Step 1: Write the failing test**

Create `internal/action/store_test.go`:

```go
package action

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"shutdowner/internal/power"
)

func TestFileStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule.json")
	s := NewFileStore(path)

	firesAt := time.Date(2026, 8, 3, 3, 0, 0, 0, time.FixedZone("test", 3*60*60))
	want := Persisted{Pending: &PersistedPending{
		ID: "id-a", Action: power.ActionShutdown, Force: true, FiresAt: firesAt,
	}}
	if err := s.Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Pending == nil {
		t.Fatal("Load() returned no pending action")
	}
	if got.Pending.ID != "id-a" || got.Pending.Action != power.ActionShutdown || !got.Pending.Force {
		t.Errorf("Load() = %+v, want the saved action", got.Pending)
	}
	// The offset must survive, so a clock that shifts between writing and
	// reading does not move the deadline.
	if !got.Pending.FiresAt.Equal(firesAt) {
		t.Errorf("FiresAt = %s, want %s", got.Pending.FiresAt, firesAt)
	}
}

func TestFileStoreLoadWithNoFile(t *testing.T) {
	s := NewFileStore(filepath.Join(t.TempDir(), "absent.json"))
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil for an absent file", err)
	}
	if got.Pending != nil || got.Missed != nil {
		t.Errorf("Load() = %+v, want an empty state", got)
	}
}

func TestFileStoreLoadRejectsCorruptContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("writing the corrupt file: %v", err)
	}
	if _, err := NewFileStore(path).Load(); err == nil {
		t.Error("Load() error = nil, want a decode error the caller can log")
	}
}

func TestFileStoreClearRemovesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule.json")
	s := NewFileStore(path)
	if err := s.Save(Persisted{Missed: &Missed{Action: power.ActionSleep, WasDueAt: "x"}}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := s.Clear(); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the file still exists after Clear(): %v", err)
	}
	// Clearing an already-absent file is not an error.
	if err := s.Clear(); err != nil {
		t.Errorf("second Clear() error = %v, want nil", err)
	}
}

func TestFileStoreSaveLeavesNoTempFileBehind(t *testing.T) {
	dir := t.TempDir()
	s := NewFileStore(filepath.Join(dir, "schedule.json"))
	if err := s.Save(Persisted{Missed: &Missed{Action: power.ActionSleep, WasDueAt: "x"}}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "schedule.json" {
		t.Errorf("directory holds %d entries, want only schedule.json", len(entries))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/action/ -run FileStore -v`
Expected: FAIL to build — `undefined: NewFileStore`, `undefined: Persisted`.

- [ ] **Step 3: Write the implementation**

Create `internal/action/store.go`:

```go
package action

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"shutdowner/internal/power"
)

// PersistedPending is a scheduled action as it survives a restart.
//
// FiresAt keeps its offset, unlike the naive wall clock the operator typed. The
// intent was captured when the action was scheduled, so that is the instant to
// replay; storing it naively would let a DST change move the deadline.
type PersistedPending struct {
	ID      string       `json:"id"`
	Action  power.Action `json:"action"`
	Force   bool         `json:"force"`
	FiresAt time.Time    `json:"firesAt"`
}

// Persisted is the whole of what the manager keeps across restarts.
type Persisted struct {
	Pending *PersistedPending `json:"pending,omitempty"`
	Missed  *Missed           `json:"missed,omitempty"`
}

// Store keeps the schedule somewhere it survives the process.
type Store interface {
	Load() (Persisted, error)
	Save(Persisted) error
	Clear() error
}

// NopStore discards everything. It is the default, so a Manager built without a
// store behaves exactly as it did before there was one.
type NopStore struct{}

func (NopStore) Load() (Persisted, error) { return Persisted{}, nil }
func (NopStore) Save(Persisted) error     { return nil }
func (NopStore) Clear() error             { return nil }

// FileStore keeps the schedule in a JSON file.
type FileStore struct{ path string }

func NewFileStore(path string) *FileStore { return &FileStore{path: path} }

// Load reports an absent file as an empty state rather than an error: a machine
// with nothing scheduled is the ordinary case, not a fault.
func (s *FileStore) Load() (Persisted, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return Persisted{}, nil
	}
	if err != nil {
		return Persisted{}, fmt.Errorf("reading %s: %w", s.path, err)
	}
	var p Persisted
	if err := json.Unmarshal(data, &p); err != nil {
		return Persisted{}, fmt.Errorf("parsing %s: %w", s.path, err)
	}
	return p, nil
}

// Save writes through a temporary file and a rename, so a crash mid-write
// cannot leave a half-written schedule that fails to parse at startup.
func (s *FileStore) Save(p Persisted) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the schedule: %w", err)
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".schedule-*.tmp")
	if err != nil {
		return fmt.Errorf("creating a temporary file in %s: %w", dir, err)
	}
	name := tmp.Name()
	// Any failure from here on leaves the temp file behind unless it is removed,
	// and a directory slowly filling with .schedule-*.tmp is its own bug report.
	defer os.Remove(name)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", name, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("setting the mode of %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", name, err)
	}
	if err := os.Rename(name, s.path); err != nil {
		return fmt.Errorf("replacing %s: %w", s.path, err)
	}
	return nil
}

func (s *FileStore) Clear() error {
	if err := os.Remove(s.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", s.path, err)
	}
	return nil
}
```

Note `os.Rename` replaces an existing file on Windows as well as on Unix, which is why this is safe to call repeatedly.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/action/ -run FileStore -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/action/store.go internal/action/store_test.go
git commit -m "feat: persist the schedule to a file beside the executable"
```

---

## Task 5: Restore the schedule at startup

**Files:**
- Modify: `internal/action/manager.go`
- Modify: `internal/action/manager_test.go`
- Modify: `cmd/shutdowner/main.go`

**Interfaces:**
- Consumes: `Store`, `Persisted` from Task 4; `MissedGrace`, `Missed` from Task 3.
- Produces:
  - `func WithStore(s Store) Option`
  - `func (m *Manager) Restore() error`

- [ ] **Step 1: Write the failing tests**

Add to `internal/action/manager_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/action/ -run 'Persisted|Restore' -v`
Expected: FAIL to build — `WithStore` and `Restore` are undefined.

- [ ] **Step 3: Implement persistence in the manager**

In `internal/action/manager.go`, add the field and default, plus the option:

```go
type Manager struct {
	ctrl  power.Controller
	store Store
	...
```

```go
	m := &Manager{
		ctrl:  ctrl,
		store: NopStore{},
		now:   time.Now,
		newID: randomID,
		state: StateIdle,
	}
```

```go
// WithStore persists the schedule so it survives a restart. Without it the
// manager keeps everything in memory, which is what the tests want.
func WithStore(s Store) Option {
	return func(m *Manager) { m.store = s }
}

// WithLogger gives the manager somewhere to report a persistence failure. It
// cannot return one: a schedule that will not survive a restart is worth much
// more than a refused request, so the write failing must not fail the action.
func WithLogger(l *slog.Logger) Option {
	return func(m *Manager) { m.logger = l }
}
```

Add `logger *slog.Logger` to the struct, default it to `slog.New(slog.DiscardHandler)` in `New`, and add `"log/slog"` to the imports. Discarding by default keeps every existing test and the `NopStore` path silent.

Add the persistence helper. Store calls happen under the manager's mutex on purpose: the file holds one small object, and letting the lock cover it removes any question of two writers interleaving.

```go
// persistLocked writes the current state, logging rather than returning a
// failure. Losing the file means losing the schedule across a restart, which is
// worth a warning and nothing more.
func (m *Manager) persistLocked() {
	var p Persisted
	if m.state == StatePending {
		p.Pending = &PersistedPending{ID: m.id, Action: m.action, Force: m.force, FiresAt: m.firesAt}
	}
	p.Missed = m.missed

	var err error
	if p.Pending == nil && p.Missed == nil {
		err = m.store.Clear()
	} else {
		err = m.store.Save(p)
	}
	if err != nil {
		m.logger.Warn("persisting the schedule; it will not survive a restart", "error", err)
	}
}
```

Call it at the end of `Schedule` (before the return), in `Abort`, in `Dismiss`, in `missLocked`, and after a successful fire in `Tick`:

```go
	pending := m.pendingLocked(m.now())
	m.persistLocked()
	return pending, nil
```

Add `Restore`:

```go
// Restore reloads a schedule left by a previous run. A deadline still ahead, or
// less than MissedGrace past, is re-armed; anything older becomes a missed
// record. It returns an error for the caller to log, having left the manager
// idle and usable — a bad state file must never stop the service from starting.
func (m *Manager) Restore() error {
	p, err := m.store.Load()
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.missed = p.Missed
	if m.missed != nil {
		m.state = StateMissed
	}
	if p.Pending == nil {
		return nil
	}

	now := m.now()
	if now.Sub(p.Pending.FiresAt) > MissedGrace {
		m.action = p.Pending.Action
		m.firesAt = p.Pending.FiresAt
		m.missLocked()
		return nil
	}

	m.state = StatePending
	m.id = p.Pending.ID
	m.action = p.Pending.Action
	m.force = p.Pending.Force
	m.firesAt = p.Pending.FiresAt
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/action/ -v`
Expected: PASS.

- [ ] **Step 5: Wire it into the application**

In `cmd/shutdowner/main.go`, inside `runServer`, replace the manager construction:

```go
	statePath, err := schedulePath(*flagConfig)
	if err != nil {
		return err
	}
	actions := action.New(ctrl,
		action.WithStore(action.NewFileStore(statePath)),
		action.WithLogger(logger),
	)
	if err := actions.Restore(); err != nil {
		// A corrupt or unreadable state file loses the schedule, which is a
		// great deal better than refusing to start the thing that answers the
		// door.
		logger.Warn("restoring the saved schedule", "path", statePath, "error", err)
	}
	stopTicking := actions.Start(action.TickInterval)
	defer stopTicking()
```

and pass `actions` to `web.New` where `action.New(ctrl, cfg.Delay)` was passed before.

Add beside `configPath`:

```go
// schedulePath puts the state file beside the .env it belongs to, so a
// --config pointing elsewhere keeps its schedule with it rather than in
// whatever directory the service happened to start in.
func schedulePath(configFlag string) (string, error) {
	if configFlag != "" {
		abs, err := filepath.Abs(configFlag)
		if err != nil {
			return "", fmt.Errorf("resolving --config path: %w", err)
		}
		return filepath.Join(filepath.Dir(abs), "schedule.json"), nil
	}
	dir, err := config.ExeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "schedule.json"), nil
}
```

Add `schedule.json` to `.gitignore` beside the existing `.env` entry.

- [ ] **Step 6: Verify the whole tree**

Run: `make test && make vet && make build-windows`
Expected: builds; everything passes except the known `TestWriteStarterEnv`.

- [ ] **Step 7: Commit**

```bash
git add internal/action/manager.go internal/action/manager_test.go cmd/shutdowner/main.go .gitignore
git commit -m "feat: restore a saved schedule at startup"
```

---

## Task 6: Accept a schedule over the API

**Files:**
- Modify: `internal/web/api_handlers.go`
- Modify: `internal/web/server.go`
- Modify: `internal/web/api_handlers_test.go`

**Interfaces:**
- Consumes: `ResolveWhen` and its errors from Task 1; `Schedule(ctx, a, force, firesAt)` from Task 2; `Dismiss` from Task 3.
- Produces: `POST /api/dismiss`; `actionRequest` gains `DelaySeconds *int` and `At string`; `statusResponse` gains `Missed`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/web/api_handlers_test.go`:

```go
func TestActionAcceptsARelativeSchedule(t *testing.T) {
	e := newTestEnv(t)
	res := postJSON(t, e, "/api/action", `{"action":"shutdown","force":true,"delaySeconds":7200}`)
	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", res.Code, res.Body)
	}
	var got struct {
		RemainingSeconds int `json:"remainingSeconds"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding the response: %v", err)
	}
	// Allow a second of slack for the clock moving during the request.
	if got.RemainingSeconds < 7199 || got.RemainingSeconds > 7200 {
		t.Errorf("remainingSeconds = %d, want about 7200", got.RemainingSeconds)
	}
}

func TestActionRejectsBadSchedules(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"both timing fields", `{"action":"shutdown","delaySeconds":60,"at":"2030-01-01T00:00"}`},
		{"a negative delay", `{"action":"shutdown","delaySeconds":-5}`},
		{"a delay past the horizon", `{"action":"shutdown","delaySeconds":604801}`},
		{"an unparseable at", `{"action":"shutdown","at":"tomorrow"}`},
		{"an at in the past", `{"action":"shutdown","at":"2000-01-01T00:00"}`},
		{"an at past the horizon", `{"action":"shutdown","at":"2099-01-01T00:00"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			res := postJSON(t, e, "/api/action", tt.body)
			if res.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400: %s", res.Code, res.Body)
			}
		})
	}
}

func TestActionWithNoTimingFieldsKeepsTheConfiguredDelay(t *testing.T) {
	e := newTestEnv(t)
	res := postJSON(t, e, "/api/action", `{"action":"shutdown","force":true}`)
	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", res.Code, res.Body)
	}
	var got struct {
		RemainingSeconds int `json:"remainingSeconds"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding the response: %v", err)
	}
	if got.RemainingSeconds < 44 || got.RemainingSeconds > 45 {
		t.Errorf("remainingSeconds = %d, want the configured 45", got.RemainingSeconds)
	}
}

func TestDismissIsIdempotent(t *testing.T) {
	e := newTestEnv(t)
	// Nothing has been missed, and it still succeeds.
	if res := postJSON(t, e, "/api/dismiss", `{}`); res.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: %s", res.Code, res.Body)
	}
}

func TestDismissRequiresCSRF(t *testing.T) {
	e := newTestEnv(t)
	r := httptest.NewRequest(http.MethodPost, "/api/dismiss", strings.NewReader(`{}`))
	r.AddCookie(e.sessionCookie(t))
	r.Header.Set("Content-Type", "application/json")
	if res := do(t, e.handler, r); res.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 without a CSRF token", res.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/web/ -run 'Schedule|Dismiss|ConfiguredDelay' -v`
Expected: FAIL — 400 where 202 was wanted (unknown fields are ignored today), and 404 for `/api/dismiss`.

- [ ] **Step 3: Implement the handler changes**

In `internal/web/api_handlers.go`:

```go
type actionRequest struct {
	Action power.Action `json:"action"`
	Force  bool         `json:"force"`
	// A pointer so that an absent field is distinguishable from an explicit
	// zero, which is legal and means "at the next tick".
	DelaySeconds *int   `json:"delaySeconds"`
	At           string `json:"at"`
}
```

```go
type statusResponse struct {
	sysinfo.Info
	Capabilities power.Capabilities `json:"capabilities"`
	State        action.State       `json:"state"`
	Pending      *action.Pending    `json:"pending"`
	Missed       *action.Missed     `json:"missed"`
	Error        string             `json:"error"`
}
```

In `handleStatus`, add `Missed: st.Missed,`.

In `handleAction`, resolve before scheduling:

```go
	firesAt, err := action.ResolveWhen(time.Now(), req.DelaySeconds, req.At, s.delay)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	pending, err := s.actions.Schedule(r.Context(), req.Action, req.Force, firesAt)
```

and extend the log line with `"firesAt", pending.FiresAtLocal`.

Add the handler:

```go
// handleDismiss clears a missed action. It takes no body and never conflicts:
// dismissing nothing is a success, so two tabs racing produce no error anybody
// has to explain.
func (s *Server) handleDismiss(w http.ResponseWriter, r *http.Request) {
	s.actions.Dismiss()
	s.logger.Info("missed action dismissed", "ip", ClientIP(r))
	writeJSON(w, http.StatusOK, map[string]string{"status": "dismissed"})
}
```

In `internal/web/server.go`, register it beside the others so it gets the same body cap, session and CSRF treatment:

```go
	mux.HandleFunc("POST /api/dismiss", limitBody(maxRequestBody, s.requireSession(s.requireCSRF(s.handleDismiss))))
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/web/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/web/api_handlers.go internal/web/server.go internal/web/api_handlers_test.go
git commit -m "feat: accept a relative or absolute schedule over the API"
```

---

## Task 7: The When controls

**Files:**
- Modify: `internal/web/templates/dashboard.html`
- Modify: `internal/web/static/app.js`
- Modify: `internal/web/static/app.css`
- Modify: `internal/web/static_test.go`

**Interfaces:**
- Consumes: `POST /api/action` with `delaySeconds`/`at`, `POST /api/dismiss`, `pending.firesAtLocal`, `missed` from Task 6.
- Produces: no Go API.

- [ ] **Step 1: Write the failing test**

Add to `internal/web/static_test.go`:

```go
// The dialog has to offer all three timings, and the missed banner has to exist
// for app.js to fill in.
func TestDashboardOffersTheWhenControls(t *testing.T) {
	e := newTestEnv(t)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(e.sessionCookie(t))

	body := do(t, e.handler, r).Body.String()
	for _, want := range []string{
		`name="when" value="now"`,
		`name="when" value="in"`,
		`name="when" value="at"`,
		`id="when-in-value"`,
		`id="when-in-unit"`,
		`id="when-at"`,
		`id="missed"`,
		`id="dismiss"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the dashboard is missing %s", want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/web/ -run TestDashboardOffersTheWhenControls -v`
Expected: FAIL, listing every missing control.

- [ ] **Step 3: Add the markup**

In `internal/web/templates/dashboard.html`, insert the missed banner after the error paragraph:

```html
    <p class="error hidden" id="error" role="alert"></p>

    <div class="missed hidden" id="missed" role="alert">
      <p id="missed-text"></p>
      <button type="button" id="dismiss" class="link">Dismiss</button>
    </div>
```

and replace the dialog's form contents with:

```html
    <form method="dialog">
      <h2 id="confirm-title"></h2>
      <label class="checkbox"><input type="checkbox" id="graceful"> Close apps gracefully</label>

      <fieldset class="when">
        <legend>When</legend>
        <label class="radio">
          <input type="radio" name="when" value="now" checked>
          <span id="when-now-label">Now</span>
        </label>
        <label class="radio">
          <input type="radio" name="when" value="in">
          In
          <input type="number" id="when-in-value" min="1" max="10080" value="1" inputmode="numeric">
          <select id="when-in-unit">
            <option value="60">minutes</option>
            <option value="3600" selected>hours</option>
          </select>
        </label>
        <label class="radio">
          <input type="radio" name="when" value="at">
          At
          <input type="datetime-local" id="when-at">
        </label>
        <p class="hint hidden" id="tz-note"></p>
      </fieldset>

      <p class="hint">Leave unchecked to force apps closed. Unsaved work will be lost.</p>
      <menu>
        <button value="cancel">Cancel</button>
        <button value="confirm" class="danger">Confirm</button>
      </menu>
    </form>
```

- [ ] **Step 4: Add the styling**

Append to `internal/web/static/app.css`:

```css
.when {
  border: 1px solid var(--line);
  border-radius: 10px;
  padding: 0.5rem 0.75rem 0.75rem;
  margin: 0.75rem 0 0;
}
.when legend { color: var(--muted); font-size: 0.875rem; padding: 0 0.35rem; }

/* Each row is one tap target, so the label and its inputs stay on one line and
   wrap together rather than the number sliding under the radio. */
.radio {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 0.4rem;
  color: var(--fg);
  font-size: 1rem;
  margin: 0 0 0.4rem;
  min-height: 2.25rem;
}
.radio:last-of-type { margin-bottom: 0; }
.when input[type="number"] { width: 4.5rem; }
.when input[type="number"],
.when input[type="datetime-local"],
.when select {
  font: inherit;
  color: var(--fg);
  background: var(--bg);
  border: 1px solid var(--line);
  border-radius: 8px;
  padding: 0.35rem 0.5rem;
}

.missed {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  border: 1px solid var(--restart);
  border-radius: 10px;
  padding: 0.75rem 1rem;
  margin-top: 0.75rem;
}
.missed p { color: var(--restart); font-size: 0.9rem; }
```

- [ ] **Step 5: Wire up the JavaScript**

In `internal/web/static/app.js`:

Add the PC-clock helpers after `formatUptime`. Everything about the PC's clock is arithmetic on `getUTC*` of a Date built from the wall-clock text, so the browser's own timezone never enters into it:

```js
  // The status endpoint reports the PC's time as RFC3339 with the PC's offset.
  // Slicing the wall-clock part and appending "Z" gives a Date whose UTC fields
  // hold the PC's reading, which makes date arithmetic possible without the
  // browser's timezone ever being consulted.
  function pcWallDate(iso) {
    return new Date(iso.slice(0, 19) + "Z");
  }

  function pcWallInput(d) {
    return d.toISOString().slice(0, 16);
  }

  function pcOffsetMinutes(iso) {
    var m = /([+-])(\d{2}):(\d{2})$/.exec(iso);
    if (!m) return 0;
    var mins = parseInt(m[2], 10) * 60 + parseInt(m[3], 10);
    return m[1] === "-" ? -mins : mins;
  }

  function formatWait(seconds) {
    if (seconds < 60) return seconds + "s";
    var mins = Math.round(seconds / 60);
    if (mins < 60) return mins + "m";
    var h = Math.floor(mins / 60);
    var m = mins % 60;
    return m === 0 ? h + "h" : h + "h " + m + "m";
  }
```

Record the PC's clock in `state` by adding `localTime: null` to the initial object, and in `applyStatus` set `state.localTime = data.localTime;` before the existing `el("localtime")` line.

Replace `render`:

```js
  function render() {
    var box = el("pending");
    if (!state.pending) {
      box.classList.add("hidden");
      return;
    }
    box.classList.remove("hidden");
    var left = Math.max(0, Math.round((state.pending.firesAtMs - Date.now()) / 1000));
    var label = LABELS[state.pending.action] || state.pending.action;
    var text = label + " in " + formatWait(left);
    // Past a minute the countdown alone stops being useful, so name the hour it
    // lands on. The wall-clock characters are taken from the server's string as
    // text: passing it through Date() would re-read it in the browser's
    // timezone, which is the one thing the design rules out.
    if (left >= 60 && state.pending.firesAtLocal) {
      text += " · at " + state.pending.firesAtLocal.slice(11, 16);
    }
    el("pending-text").textContent = text;
    if (left === 0) state.firedAction = state.pending.action;
  }
```

In `applyStatus`, carry the new field through and render the missed banner. Replace the pending block and the `showError` line with:

```js
    if (data.state === "pending" && data.pending) {
      state.pending = {
        id: data.pending.id,
        action: data.pending.action,
        firesAtMs: Date.now() + data.pending.remainingSeconds * 1000,
        firesAtLocal: data.pending.firesAtLocal
      };
    } else {
      state.pending = null;
    }

    var missed = el("missed");
    if (data.missed) {
      var mLabel = LABELS[data.missed.action] || data.missed.action;
      el("missed-text").textContent =
        mLabel + " was due at " + data.missed.wasDueAt.slice(11, 16) +
        " and was skipped: the PC was off or asleep.";
      missed.classList.remove("hidden");
    } else {
      missed.classList.add("hidden");
    }

    showError(data.state === "failed" ? data.error : "");
```

Add the When helpers before the dialog handlers:

```js
  function selectedWhen() {
    var checked = document.querySelector('input[name="when"]:checked');
    return checked ? checked.value : "now";
  }

  // Reset to Now, bound the picker to the PC's clock, and say so when the phone
  // holding the browser disagrees with the machine about what time it is.
  function resetWhen() {
    document.querySelector('input[name="when"][value="now"]').checked = true;
    el("when-in-value").value = "1";
    el("when-in-unit").value = "3600";
    el("when-now-label").textContent =
      defaultDelay > 0 ? "Now (" + defaultDelay + "s countdown)" : "Now";

    var note = el("tz-note");
    var at = el("when-at");
    if (!state.localTime) {
      at.value = "";
      at.removeAttribute("min");
      at.removeAttribute("max");
      note.classList.add("hidden");
      return;
    }

    var pcNow = pcWallDate(state.localTime);
    at.min = pcWallInput(pcNow);
    at.max = pcWallInput(new Date(pcNow.getTime() + 7 * 86400000));
    at.value = pcWallInput(new Date(pcNow.getTime() + 3600000));

    var pcOffset = pcOffsetMinutes(state.localTime);
    var browserOffset = -new Date().getTimezoneOffset();
    if (pcOffset === browserOffset) {
      note.classList.add("hidden");
      return;
    }
    var browserNow = new Date();
    note.textContent =
      "Times are the PC's clock, which reads " + at.min.slice(11) +
      ". Your device reads " +
      String(browserNow.getHours()).padStart(2, "0") + ":" +
      String(browserNow.getMinutes()).padStart(2, "0") + ".";
    note.classList.remove("hidden");
  }

  function whenPayload() {
    switch (selectedWhen()) {
      case "in":
        var n = parseInt(el("when-in-value").value, 10);
        if (!(n > 0)) throw new Error("Enter how long to wait.");
        return { delaySeconds: n * parseInt(el("when-in-unit").value, 10) };
      case "at":
        var at = el("when-at").value;
        if (!at) throw new Error("Pick a date and time.");
        // The control emits seconds when the user types them; the server wants
        // minute precision.
        return { at: at.slice(0, 16) };
      default:
        return {};
    }
  }
```

Call `resetWhen()` in the action-button click handler, replacing `el("graceful").checked = false;` with:

```js
      el("graceful").checked = false;
      resetWhen();
```

and simplify the title, since the delay is now shown on the Now row:

```js
      el("confirm-title").textContent = label + " this PC?";
```

Replace the body of the dialog `close` handler's try block:

```js
    try {
      showError("");
      var payload = whenPayload();
      payload.action = chosenAction;
      payload.force = force;
      var data = await post("/api/action", payload);
      state.firedAction = null;
      state.pending = {
        id: data.id,
        action: chosenAction,
        firesAtMs: Date.now() + data.remainingSeconds * 1000,
        firesAtLocal: data.firesAtLocal
      };
      render();
    } catch (e) {
      showError(e.message);
    }
```

For that `firesAtLocal` to arrive, add it to `actionResponse` in `internal/web/api_handlers.go`:

```go
type actionResponse struct {
	ID               string `json:"id"`
	RemainingSeconds int    `json:"remainingSeconds"`
	FiresAtLocal     string `json:"firesAtLocal"`
}
```

and set `FiresAtLocal: pending.FiresAtLocal` where the response is written.

Finally add the dismiss handler beside the abort one:

```js
  el("dismiss").addEventListener("click", async function () {
    try {
      await post("/api/dismiss", {});
      el("missed").classList.add("hidden");
    } catch (e) {
      showError(e.message);
    }
  });
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/web/ -v`
Expected: PASS.

- [ ] **Step 7: Check it in the running application**

```bash
make build-windows
```

Start it against a scratch config on a spare port, sign in, and confirm by hand:

1. The dialog opens with **Now** selected and the countdown length on that row.
2. `In 1 minutes` schedules, and the pending line reads `Shut down in 1m · at HH:MM`.
3. Abort clears it.
4. `At` refuses a time in the past through the picker's own `min`.
5. Sending `{"action":"shutdown","at":"2000-01-01T00:00"}` by hand returns 400 with the PC's clock quoted in the message.

Then test the restart path, which is the whole point of the store:

6. Schedule something an hour out, stop the process, confirm `schedule.json` holds it, start again, and confirm the dashboard still shows it pending.
7. Schedule something a minute out, stop the process, wait six minutes, start again. The action must **not** run, and the missed banner must name it. Dismiss clears it.

- [ ] **Step 8: Commit**

```bash
git add internal/web/templates/dashboard.html internal/web/static/app.js internal/web/static/app.css internal/web/static_test.go internal/web/api_handlers.go
git commit -m "feat: choose when an action runs from the confirm dialog"
```

---

## Task 8: Document it

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Describe the feature and the state file**

Add to `README.md`, after the section describing the actions:

```markdown
### Scheduling

Every action can run now, after a delay, or at a wall-clock time, chosen in the
confirm dialog. A time means the PC's clock, not the clock of the device holding
the browser; when the two disagree the dialog says so and shows both. Schedules
reach at most 7 days ahead.

A pending schedule is kept in `schedule.json` beside the `.env`, so restarting
the service does not lose it. If its moment passes while the service is stopped
or the machine is asleep, the action is **skipped rather than run late** — being
shut down moments after deliberately waking the PC is worse than the action not
happening — and the dashboard reports it until dismissed. The cutoff is five
minutes past the deadline.
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: describe scheduled actions and the state file"
```

---

## Self-review notes

Checked against the spec:

| Spec section | Task |
|---|---|
| One rule / MissedGrace | 3 (tick), 5 (restore) |
| API fields and validation table | 1 (rules), 6 (wiring and status codes) |
| `delaySeconds: 0` is legal | 1 |
| `Pending.FiresAtLocal`, no `Date()` in the browser | 2 (field), 7 (consumption) |
| `Status.Missed` | 3 |
| `POST /api/dismiss`, idempotent, CSRF | 3 (manager), 6 (route) |
| `StateMissed` is schedulable | 3 |
| Store interface, `0600`, temp-and-rename | 4 |
| `firesAt` persisted with offset | 4 |
| Startup: corrupt → idle; ahead → re-arm; stale → missed | 5 |
| Save failure warns rather than refuses | 5 (`WithLogger`, `persistLocked`) |
| Single slot, `ErrConflict` unchanged | 2 |
| `When` fieldset, `min`/`max`, timezone note | 7 |
| Pending formatting, missed banner | 7 |
| No inline script or style | 7 |
| File layout | all |

Naming is consistent across tasks: `ResolveWhen`, `Schedule(ctx, a, force, firesAt)`, `Tick`, `Start`, `Restore`, `Dismiss`, `WithStore`, `NewFileStore`, `Persisted`, `PersistedPending`, `Missed`, `StateMissed`, `MissedGrace`, `MaxHorizon`, `AtLayout`, `TickInterval`.

Two spec items are deliberately handled outside a dedicated task because they are
one line each in an existing file: `Server.delay` (Task 2, Step 5) and
`actionResponse.FiresAtLocal` (Task 7, Step 5).

Three problems found during this review and fixed above, recorded because each
would have cost an implementer real time:

- **`persistLocked` contradicted itself.** Task 5's interface block said
  `Schedule` returns the persistence error for the handler to log, and the code
  discarded it. The spec requires a warning. Resolved with `WithLogger`, since
  the manager is the only thing that knows a write failed and the caller must not
  be made to fail the request over it.
- **`TestRemainingSecondsCountsDown` would have been broken by the mechanical
  conversion rule.** It advances the clock without firing on purpose; converting
  its `h.now = ...` lines to `h.advance` fires the action, leaves `Pending` nil
  and panics on the following line.
- **Two tests build a `Manager` directly** and never touch the harness, so the
  conversion rule did not reach them. Both are now written out in full.
