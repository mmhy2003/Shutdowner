package power

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestBuildShutdownArgs(t *testing.T) {
	tests := []struct {
		name   string
		action Action
		force  bool
		want   []string
	}{
		{"shutdown forced", ActionShutdown, true, []string{"/s", "/t", "0", "/f"}},
		{"shutdown graceful", ActionShutdown, false, []string{"/s", "/t", "0"}},
		{"restart forced", ActionRestart, true, []string{"/r", "/t", "0", "/f"}},
		{"restart graceful", ActionRestart, false, []string{"/r", "/t", "0"}},
		{"sleep is not a shutdown.exe action", ActionSleep, true, nil},
		{"hibernate is not a shutdown.exe action", ActionHibernate, false, nil},
		{"unknown action", Action("explode"), true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildShutdownArgs(tt.action, tt.force)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildShutdownArgs(%q, %t) = %v, want %v", tt.action, tt.force, got, tt.want)
			}
		})
	}
}

func TestActionValid(t *testing.T) {
	for _, a := range []Action{ActionShutdown, ActionRestart, ActionSleep, ActionHibernate} {
		if !a.Valid() {
			t.Errorf("Action(%q).Valid() = false, want true", a)
		}
	}
	for _, a := range []Action{"", "Shutdown", "poweroff", "explode"} {
		if a.Valid() {
			t.Errorf("Action(%q).Valid() = true, want false", a)
		}
	}
}

func TestActionSuspends(t *testing.T) {
	if !ActionSleep.Suspends() || !ActionHibernate.Suspends() {
		t.Error("sleep and hibernate must report Suspends() = true")
	}
	if ActionShutdown.Suspends() || ActionRestart.Suspends() {
		t.Error("shutdown and restart must report Suspends() = false")
	}
}

func TestCapabilitiesAllows(t *testing.T) {
	none := Capabilities{}
	// Shutdown and restart are never capability-gated.
	if !none.Allows(ActionShutdown) || !none.Allows(ActionRestart) {
		t.Error("shutdown and restart must be allowed regardless of capabilities")
	}
	if none.Allows(ActionSleep) || none.Allows(ActionHibernate) {
		t.Error("sleep and hibernate must be refused when unavailable")
	}

	both := Capabilities{Sleep: true, Hibernate: true}
	if !both.Allows(ActionSleep) || !both.Allows(ActionHibernate) {
		t.Error("sleep and hibernate must be allowed when available")
	}

	sleepOnly := Capabilities{Sleep: true}
	if !sleepOnly.Allows(ActionSleep) || sleepOnly.Allows(ActionHibernate) {
		t.Error("capabilities must be honoured independently")
	}
}

func TestFakeRecordsCalls(t *testing.T) {
	f := NewFake()
	ctx := context.Background()

	if err := f.Execute(ctx, ActionShutdown, true); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if err := f.Execute(ctx, ActionSleep, false); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}

	want := []Call{{Action: ActionShutdown, Force: true}, {Action: ActionSleep, Force: false}}
	if got := f.Calls(); !reflect.DeepEqual(got, want) {
		t.Errorf("Calls() = %v, want %v", got, want)
	}
}

func TestFakeDefaultsToFullCapabilities(t *testing.T) {
	caps, err := NewFake().Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities() error = %v", err)
	}
	if !caps.Sleep || !caps.Hibernate {
		t.Errorf("Capabilities() = %+v, want both true", caps)
	}
}

func TestFakeReturnsConfiguredError(t *testing.T) {
	f := NewFake()
	boom := errors.New("boom")
	f.SetError(boom)
	if err := f.Execute(context.Background(), ActionShutdown, true); !errors.Is(err, boom) {
		t.Errorf("Execute() error = %v, want boom", err)
	}
}

func TestFakeHonoursConfiguredCapabilities(t *testing.T) {
	f := NewFake()
	f.SetCapabilities(Capabilities{Sleep: true})
	caps, _ := f.Capabilities(context.Background())
	if !caps.Sleep || caps.Hibernate {
		t.Errorf("Capabilities() = %+v, want {Sleep:true Hibernate:false}", caps)
	}
}
