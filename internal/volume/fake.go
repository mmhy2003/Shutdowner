package volume

import (
	"context"
	"sync"
)

// Call records one Set invocation.
type Call struct {
	State State
}

// Fake is a Controller holding its state in memory instead of touching the
// machine. It backs the unit tests and the --fake-volume development flag, so
// it carries no build tag and is not a test file.
type Fake struct {
	mu        sync.Mutex
	state     State
	available bool
	getErr    error
	setErr    error
	calls     []Call
}

// NewFake returns a Fake that is available and sitting at half volume.
func NewFake() *Fake {
	return &Fake{state: State{Level: 50}, available: true}
}

func (f *Fake) SetState(s State) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = s
}

// SetAvailable controls what Available reports, so the disabled-controls path
// is testable.
func (f *Fake) SetAvailable(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.available = v
}

// SetGetError makes every subsequent Get return err.
func (f *Fake) SetGetError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getErr = err
}

// SetSetError makes every subsequent Set return err without storing anything.
func (f *Fake) SetSetError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setErr = err
}

func (f *Fake) Get(context.Context) (State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return State{}, f.getErr
	}
	return f.state, nil
}

// Set stores the state and returns it. A real device may settle on a nearby
// step instead, but a fake that invented a different answer would be lying
// about a machine it does not have.
func (f *Fake) Set(_ context.Context, s State) (State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{State: s})
	if f.setErr != nil {
		return State{}, f.setErr
	}
	f.state = s
	return f.state, nil
}

func (f *Fake) Available() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.available
}

// Calls returns a copy of the recorded Set calls.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Call, len(f.calls))
	copy(out, f.calls)
	return out
}
