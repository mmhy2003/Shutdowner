package volume

import (
	"errors"
	"strings"
	"testing"
)

func TestResultRoundTrip(t *testing.T) {
	want := State{Level: 45, Muted: true}
	got, err := DecodeResult(EncodeResult(want, nil))
	if err != nil {
		t.Fatalf("DecodeResult() error = %v", err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestEncodeResultCarriesTheError(t *testing.T) {
	line := EncodeResult(State{Level: 45, Muted: true}, errors.New("no default playback device"))

	if !strings.Contains(line, "no default playback device") {
		t.Fatalf("encoded = %q, want it to carry the error text", line)
	}
	// The guarantee is structural: no state key may appear at all, so that
	// reordering the decoder's checks cannot turn a failure into "silence".
	if strings.Contains(line, `"level"`) {
		t.Errorf("encoded = %q, want no level key alongside an error", line)
	}
	if strings.Contains(line, `"muted"`) {
		t.Errorf("encoded = %q, want no muted key alongside an error", line)
	}
	if _, err := DecodeResult(line); err == nil {
		t.Error("DecodeResult() error = nil for an error payload, want an error")
	}
}

func TestDecodeResultDiscardsStateThatArrivesWithAnError(t *testing.T) {
	// A hostile or buggy helper sending both must not yield a usable reading.
	got, err := DecodeResult(`{"level":90,"muted":false,"error":"boom"}`)
	if err == nil {
		t.Fatal("DecodeResult() error = nil, want an error")
	}
	if got != (State{}) {
		t.Errorf("state = %+v, want the zero state when an error is present", got)
	}
}

func TestSilenceSurvivesTheRoundTrip(t *testing.T) {
	// Level 0 is a real reading, not an absent one.
	got, err := DecodeResult(EncodeResult(State{Level: 0, Muted: false}, nil))
	if err != nil {
		t.Fatalf("DecodeResult() error = %v", err)
	}
	if got != (State{Level: 0, Muted: false}) {
		t.Errorf("round trip = %+v, want a level of 0 unmuted", got)
	}
}

func TestDecodeResultRejectsBadInput(t *testing.T) {
	for _, tt := range []struct{ name, line string }{
		{"empty", ""},
		{"whitespace only", "  \n"},
		{"not json at all", "volume is 45"},
		{"truncated json", `{"level":45`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeResult(tt.line); err == nil {
				t.Error("DecodeResult() error = nil, want an error")
			}
		})
	}
}

func TestDecodeResultToleratesSurroundingWhitespace(t *testing.T) {
	got, err := DecodeResult("  " + EncodeResult(State{Level: 10}, nil) + "\r\n")
	if err != nil {
		t.Fatalf("DecodeResult() error = %v", err)
	}
	if got.Level != 10 {
		t.Errorf("Level = %d, want 10", got.Level)
	}
}

func TestDecodeResultClampsAnOutOfRangeLevel(t *testing.T) {
	// The helper is our own code, but it crosses a process boundary; a level
	// outside 0-100 must never reach the UI.
	got, err := DecodeResult(`{"level":900,"muted":false}`)
	if err != nil {
		t.Fatalf("DecodeResult() error = %v", err)
	}
	if got.Level != 100 {
		t.Errorf("Level = %d, want it clamped to 100", got.Level)
	}
}

func TestHelperArgsRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		op   Op
		want State
	}{
		{"get", OpGet, State{}},
		{"set unmuted", OpSet, State{Level: 45}},
		{"set muted", OpSet, State{Level: 0, Muted: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := FormatHelperArgs(tt.op, tt.want)
			if len(args) == 0 || args[0] != HelperFlag {
				t.Fatalf("FormatHelperArgs() = %v, want it to lead with %s", args, HelperFlag)
			}
			op, state, err := ParseHelperArgs(args[1:])
			if err != nil {
				t.Fatalf("ParseHelperArgs(%v) error = %v", args[1:], err)
			}
			if op != tt.op {
				t.Errorf("op = %q, want %q", op, tt.op)
			}
			if op == OpSet && state != tt.want {
				t.Errorf("state = %+v, want %+v", state, tt.want)
			}
		})
	}
}

func TestParseHelperArgsRejectsBadInput(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
	}{
		{"nothing", nil},
		{"unknown operation", []string{"louder"}},
		{"get with an argument", []string{"get", "45"}},
		{"set with no arguments", []string{"set"}},
		{"set missing the mute flag", []string{"set", "45"}},
		{"set with a non-numeric level", []string{"set", "loud", "false"}},
		{"set with a non-boolean mute flag", []string{"set", "45", "maybe"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := ParseHelperArgs(tt.args); err == nil {
				t.Error("ParseHelperArgs() error = nil, want an error")
			}
		})
	}
}

func TestFormatHelperArgsRejectsAnUnknownOp(t *testing.T) {
	if args := FormatHelperArgs(Op("louder"), State{}); args != nil {
		t.Errorf("FormatHelperArgs() = %v, want nil", args)
	}
}
