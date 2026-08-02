// Package action owns the countdown between confirming a power action and
// executing it, and the abort that cancels it.
package action

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"shutdowner/internal/power"
)

type State string

const (
	StateIdle      State = "idle"
	StatePending   State = "pending"
	StateExecuting State = "executing"
	StateFailed    State = "failed"
)

// FailedRetention is how long a failure is reported before the manager returns
// to idle on its own, so no dismissal endpoint is needed.
const FailedRetention = 60 * time.Second

var (
	ErrInvalidAction     = errors.New("action: unknown action")
	ErrUnsupportedAction = errors.New("action: not available on this system")
	ErrConflict          = errors.New("action: another action is already in progress")
	ErrNoPending         = errors.New("action: no matching pending action")
)

type Pending struct {
	ID     string       `json:"id"`
	Action power.Action `json:"action"`
	Force  bool         `json:"force"`
	// RemainingSeconds is relative rather than an absolute deadline so a client
	// with a skewed clock still renders an accurate countdown.
	RemainingSeconds int `json:"remainingSeconds"`
}

type Status struct {
	State   State    `json:"state"`
	Pending *Pending `json:"pending"`
	Error   string   `json:"error"`
}

// Timer is the part of time.Timer the manager needs, so tests can fire the
// countdown immediately instead of waiting for it.
type Timer interface{ Stop() bool }

type Option func(*Manager)

// WithClock replaces the time source. Intended for tests.
func WithClock(now func() time.Time) Option {
	return func(m *Manager) { m.now = now }
}

// WithAfterFunc replaces the countdown scheduler. Intended for tests.
func WithAfterFunc(f func(time.Duration, func()) Timer) Option {
	return func(m *Manager) { m.afterFunc = f }
}

// WithIDFunc replaces action ID generation. Intended for tests.
func WithIDFunc(f func() string) Option {
	return func(m *Manager) { m.newID = f }
}

// Manager holds the single in-flight action. The countdown lives here rather
// than in shutdown.exe /t because Windows cannot cancel a pending sleep or
// hibernate, so relying on shutdown /a would give Abort for only two of the
// four actions.
//
// An action pending when the process dies is lost rather than executed. Failing
// toward "the PC stays on" is the safe direction.
type Manager struct {
	ctrl  power.Controller
	delay time.Duration

	now       func() time.Time
	afterFunc func(time.Duration, func()) Timer
	newID     func() string

	mu       sync.Mutex
	state    State
	id       string
	action   power.Action
	force    bool
	firesAt  time.Time
	timer    Timer
	lastErr  string
	failedAt time.Time
}

func New(ctrl power.Controller, delay time.Duration, opts ...Option) *Manager {
	m := &Manager{
		ctrl:  ctrl,
		delay: delay,
		now:   time.Now,
		afterFunc: func(d time.Duration, fn func()) Timer {
			return time.AfterFunc(d, fn)
		},
		newID: randomID,
		state: StateIdle,
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Schedule starts the countdown for a. Capabilities are checked here rather
// than when the timer fires, so an unavailable action is refused immediately
// instead of failing silently a minute later.
func (m *Manager) Schedule(ctx context.Context, a power.Action, force bool) (Pending, error) {
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

	now := m.now()
	m.state = StatePending
	m.id = m.newID()
	m.action = a
	m.force = force
	m.firesAt = now.Add(m.delay)
	m.lastErr = ""

	id := m.id
	m.timer = m.afterFunc(m.delay, func() { m.fire(id) })

	return m.pendingLocked(now), nil
}

// fire executes the action if it is still the one that was scheduled. The id
// check makes a stale timer callback a no-op after an abort.
func (m *Manager) fire(id string) {
	m.mu.Lock()
	if m.state != StatePending || m.id != id {
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

// execute runs the controller and turns a panic into an ordinary error. This
// runs on the time.AfterFunc goroutine, outside the HTTP server's recoverPanic
// middleware, so an unrecovered panic here takes the whole process down — and
// the Windows power path can panic: LazyProc.Call panics via mustFind() when
// powrprof.dll or one of its exports cannot be resolved, which both
// SetSuspendState and GetPwrCapabilities go through.
//
// The panic value is the message, without the stack: it reaches the UI through
// Status().Error, and mustFind's own text already names the missing DLL or
// export.
func (m *Manager) execute(a power.Action, force bool) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("the power action panicked: %v", v)
		}
	}()
	return m.ctrl.Execute(context.Background(), a, force)
}

// Abort cancels the pending action when id matches it, so a stale browser tab
// cannot cancel something queued after its page was rendered.
func (m *Manager) Abort(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != StatePending || m.id != id {
		return ErrNoPending
	}
	if m.timer != nil {
		m.timer.Stop()
	}
	m.timer = nil
	m.state = StateIdle
	m.id = ""
	return nil
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireFailedLocked()

	s := Status{State: m.state}
	switch m.state {
	case StatePending:
		p := m.pendingLocked(m.now())
		s.Pending = &p
	case StateFailed:
		s.Error = m.lastErr
	}
	return s
}

func (m *Manager) pendingLocked(now time.Time) Pending {
	remaining := int(m.firesAt.Sub(now) / time.Second)
	if remaining < 0 {
		remaining = 0
	}
	return Pending{ID: m.id, Action: m.action, Force: m.force, RemainingSeconds: remaining}
}

func (m *Manager) expireFailedLocked() {
	if m.state == StateFailed && m.now().Sub(m.failedAt) >= FailedRetention {
		m.state = StateIdle
		m.lastErr = ""
	}
}

func randomID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// A predictable id is still safe: Abort also requires a valid session.
		return "fallback"
	}
	return hex.EncodeToString(b)
}
