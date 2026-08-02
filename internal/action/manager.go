// Package action owns the countdown between confirming a power action and
// executing it, and the abort that cancels it.
package action

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
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
	StateMissed    State = "missed"
)

// FailedRetention is how long a failure is reported before the manager returns
// to idle on its own, so no dismissal endpoint is needed.
const FailedRetention = 60 * time.Second

// MissedGrace is how far past its deadline an action may still fire. Beyond it
// the action is skipped and reported instead.
//
// The window exists because a deadline can go by while nothing is watching: the
// service was stopped, or Windows suspended the machine. Executing on the way
// back would mean shutting the PC down moments after somebody deliberately
// woke it, which is the worst outcome available here.
const MissedGrace = 5 * time.Minute

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

// Missed records an action whose deadline went by unobserved.
type Missed struct {
	Action   power.Action `json:"action"`
	WasDueAt string       `json:"wasDueAt"`
}

type Status struct {
	State   State    `json:"state"`
	Pending *Pending `json:"pending"`
	Missed  *Missed  `json:"missed"`
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

// Manager holds the single in-flight action. The countdown lives here rather
// than in shutdown.exe /t because Windows cannot cancel a pending sleep or
// hibernate, so relying on shutdown /a would give Abort for only two of the
// four actions.
//
// A pending action is persisted by the store and restored at startup; see
// Restore.
type Manager struct {
	ctrl  power.Controller
	store Store

	now    func() time.Time
	newID  func() string
	logger *slog.Logger

	mu       sync.Mutex
	state    State
	id       string
	action   power.Action
	force    bool
	firesAt  time.Time
	lastErr  string
	failedAt time.Time
	missed   *Missed
}

func New(ctrl power.Controller, opts ...Option) *Manager {
	m := &Manager{
		ctrl:   ctrl,
		store:  NopStore{},
		now:    time.Now,
		newID:  randomID,
		logger: slog.New(slog.DiscardHandler),
		state:  StateIdle,
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
	m.missed = nil

	m.state = StatePending
	m.id = m.newID()
	m.action = a
	m.force = force
	// Round(0) strips the monotonic reading. Go compares two monotonic-carrying
	// times on the monotonic clock alone, and that clock stops while Windows is
	// suspended — which is the exact case this deadline exists to survive.
	m.firesAt = firesAt.Round(0)
	m.lastErr = ""

	pending := m.pendingLocked(m.now())
	m.persistLocked()
	return pending, nil
}

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

// Tick fires the pending action if its deadline has arrived. Start calls it on
// the interval Start was given; tests call it directly, which is why it takes
// no arguments and reads the clock itself.
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
	a, force := m.action, m.force
	m.mu.Unlock()

	err := m.execute(a, force)

	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.state = StateFailed
		m.lastErr = err.Error()
		m.failedAt = m.now()
		// The deadline that just fired is spent either way: clearing the record
		// keeps a restart from re-arming and silently re-running an action an
		// operator has not confirmed again.
		m.persistLocked()
		return
	}
	// Sleep and hibernate reach here only once the machine has resumed, because
	// SetSuspendState blocks for the whole suspension. Shutdown and restart
	// never reach here at all: the process dies mid-call.
	m.state = StateIdle
	m.lastErr = ""
	m.persistLocked()
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
	m.persistLocked()
	return nil
}

// missLocked converts the pending action into a missed record.
func (m *Manager) missLocked() {
	m.missed = &Missed{Action: m.action, WasDueAt: m.firesAt.Format(time.RFC3339)}
	m.state = StateMissed
	m.id = ""
	m.persistLocked()
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
	m.persistLocked()
}

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

	// A re-armed deadline and a missed record cannot coexist: Schedule enforces
	// that everywhere else, and Status exposes Missed regardless of state, so a
	// stale one left over from the persisted file would surface next to the
	// pending action it has nothing to do with.
	m.missed = nil
	m.state = StatePending
	m.id = p.Pending.ID
	m.action = p.Pending.Action
	m.force = p.Pending.Force
	m.firesAt = p.Pending.FiresAt
	return nil
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireFailedLocked()

	s := Status{State: m.state, Missed: m.missed}
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
