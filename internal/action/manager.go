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

// TickInterval is how often the manager compares now against the deadline. A
// tick is a mutex acquisition and a time comparison, so the cost is nothing
// next to the alternative of re-arming a timer across a suspend/resume.
const TickInterval = time.Second

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
	// FiresAtLocal is the same instant on the PC's clock, for display only. The
	// browser must read its wall-clock fields as text: parsing it converts it to
	// the browser's timezone, which is the thing this design decided against.
	FiresAtLocal string `json:"firesAtLocal"`
}

type Status struct {
	State   State    `json:"state"`
	Pending *Pending `json:"pending"`
	Error   string   `json:"error"`
}

type Option func(*Manager)

// WithClock replaces the time source. Intended for tests.
func WithClock(now func() time.Time) Option {
	return func(m *Manager) { m.now = now }
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
// A pending action is persisted by the store and restored at startup; see
// Restore.
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
	// Round(0) strips the monotonic reading. Go compares two monotonic-carrying
	// times on the monotonic clock alone, and that clock stops while Windows is
	// suspended — which is the exact case this deadline exists to survive.
	m.firesAt = firesAt.Round(0)
	m.lastErr = ""

	return m.pendingLocked(m.now()), nil
}

// Tick fires the pending action if its deadline has arrived. Start calls it on
// the interval Start was given; tests call it directly, which is why it takes
// no arguments and reads the clock itself.
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

// Start runs the tick loop until the returned stop function is called. A
// non-positive interval falls back to TickInterval rather than panicking,
// since time.NewTicker itself panics on one and the Windows service manager is
// not a caller worth crashing over a bad config value here.
//
// Stopping does not wait for an action already executing: a sleep blocks inside
// the controller for the entire suspension, and holding service shutdown open
// for that would be worse than letting the goroutine end on its own. stop is
// wrapped in a sync.Once because Windows service control can deliver both a
// stop and a shutdown request, and closing done twice would panic.
func (m *Manager) Start(interval time.Duration) (stop func()) {
	if interval <= 0 {
		interval = TickInterval
	}
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
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// execute runs the controller and turns a panic into an ordinary error. This
// runs on the tick goroutine, outside the HTTP server's recoverPanic
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
// cannot cancel something queued after its page was rendered. Setting the
// state to idle is the whole of cancelling, because Tick refuses any state but
// pending.
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
	return Pending{
		ID:               m.id,
		Action:           m.action,
		Force:            m.force,
		RemainingSeconds: remaining,
		FiresAtLocal:     m.firesAt.Format(time.RFC3339),
	}
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
