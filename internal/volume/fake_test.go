package volume

import (
	"context"
	"errors"
	"testing"
)

func TestFakeStartsAvailableAtHalfVolume(t *testing.T) {
	f := NewFake()
	if !f.Available() {
		t.Error("Available() = false, want true")
	}
	got, err := f.Get(context.Background())
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got != (State{Level: 50}) {
		t.Errorf("Get() = %+v, want level 50 unmuted", got)
	}
}

func TestFakeSetRecordsAndPersists(t *testing.T) {
	f := NewFake()
	want := State{Level: 20, Muted: true}
	if err := f.Set(context.Background(), want); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	got, _ := f.Get(context.Background())
	if got != want {
		t.Errorf("Get() after Set() = %+v, want %+v", got, want)
	}
	calls := f.Calls()
	if len(calls) != 1 || calls[0].State != want {
		t.Errorf("Calls() = %+v, want one call carrying %+v", calls, want)
	}
}

func TestFakeReportsConfiguredErrors(t *testing.T) {
	boom := errors.New("boom")

	f := NewFake()
	f.SetGetError(boom)
	if _, err := f.Get(context.Background()); !errors.Is(err, boom) {
		t.Errorf("Get() error = %v, want boom", err)
	}

	g := NewFake()
	g.SetSetError(boom)
	if err := g.Set(context.Background(), State{Level: 10}); !errors.Is(err, boom) {
		t.Errorf("Set() error = %v, want boom", err)
	}
	// A failed Set must not have changed the stored state.
	if got, _ := g.Get(context.Background()); got.Level != 50 {
		t.Errorf("Get() after a failed Set = %+v, want the original level", got)
	}
}

func TestFakeAvailabilityIsConfigurable(t *testing.T) {
	f := NewFake()
	f.SetAvailable(false)
	if f.Available() {
		t.Error("Available() = true after SetAvailable(false)")
	}
}

func TestFakeCallsReturnsACopy(t *testing.T) {
	f := NewFake()
	_ = f.Set(context.Background(), State{Level: 10})
	calls := f.Calls()
	calls[0].State.Level = 999
	if again := f.Calls(); again[0].State.Level != 10 {
		t.Error("Calls() handed out the live slice; a caller mutated the fake's history")
	}
}
