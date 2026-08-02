package power

import (
	"context"
	"sync"
)

// Call records one Execute invocation.
type Call struct {
	Action Action
	Force  bool
}

// Fake is a Controller that records calls instead of touching the machine. It
// backs the unit tests and the --fake-power development flag.
type Fake struct {
	mu      sync.Mutex
	caps    Capabilities
	err     error
	capsErr error
	calls   []Call
}

// NewFake returns a Fake reporting every capability as available.
func NewFake() *Fake {
	return &Fake{caps: Capabilities{Sleep: true, Hibernate: true}}
}

func (f *Fake) SetCapabilities(c Capabilities) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.caps = c
}

// SetError makes every subsequent Execute return err.
func (f *Fake) SetError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *Fake) Execute(_ context.Context, a Action, force bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{Action: a, Force: force})
	return f.err
}

// SetCapabilitiesError makes every subsequent Capabilities call fail. It is
// separate from SetError because the two failures are meant to be handled
// differently: a failed Execute is reported to the user, a failed
// GetPwrCapabilities has to leave the dashboard standing.
//
// The configured capabilities keep being returned alongside the error, so a
// caller that trusts the value instead of the error is caught rather than
// accidentally passing.
func (f *Fake) SetCapabilitiesError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.capsErr = err
}

func (f *Fake) Capabilities(context.Context) (Capabilities, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.caps, f.capsErr
}

// Calls returns a copy of the recorded calls.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Call, len(f.calls))
	copy(out, f.calls)
	return out
}
