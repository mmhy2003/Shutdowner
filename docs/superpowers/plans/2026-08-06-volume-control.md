# Volume Control Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a mute toggle and a volume slider to the Shutdowner dashboard, controlling the master volume of the Windows PC's default playback device.

**Architecture:** A new `internal/volume` package mirroring `internal/power`'s seam: a `Controller` interface with a `Fake`, a Windows implementation, and a `!windows` stub. Because Windows audio endpoints are per-session and the service runs in session 0, the Windows implementation spawns the same binary under a hidden `--audio-helper` subcommand into the logged-in user's session, using the user's token, and reads one line of JSON back.

**Tech Stack:** Go 1.25, `golang.org/x/sys/windows` (already a dependency), raw COM through `syscall.SyscallN`, vanilla JS.

**Spec:** `docs/superpowers/specs/2026-08-06-volume-control-design.md`

## Global Constraints

- **Module path:** `shutdowner`. Internal imports are `shutdowner/internal/...`.
- **go.mod declares `go 1.25.0`** and must not change. **Exactly three external dependencies** — `golang.org/x/crypto`, `golang.org/x/sys`, `github.com/joho/godotenv`. Adding a fourth is what this whole design exists to avoid.
- **Every task ends green:** `go test ./...` passes AND `GOOS=windows GOARCH=amd64 go build ./...` succeeds. Tasks touching Windows-only files additionally require `GOOS=windows GOARCH=amd64 go vet ./...`, because vet is the only automated check that reaches them.
- **Levels are whole percent, 0–100**, everywhere: API, UI, and `State`. The device's own step scale never escapes `internal/volume`.
- **No float ever crosses a `syscall.SyscallN` boundary.** Floats travel in XMM registers on Windows amd64 and `SyscallN` uses integer registers; such a call silently sets garbage. Only the step-based COM methods are used.
- **Commit after every task**, message `feat: <what>` or `fix: <what>`. Run `gofmt -w .` before every commit.
- Go is at `/usr/local/go/bin/go`; shell state does not persist between commands, so prefix each with `export PATH=$PATH:/usr/local/go/bin && cd /opt/shutdowner &&`.
- **Never run `go mod tidy`** — it has previously deleted needed requirements from this project.

---

### Task 1: Volume core — types, rules, arithmetic, wire format

**Files:**
- Create: `internal/volume/controller.go`, `internal/volume/apply.go`, `internal/volume/steps.go`, `internal/volume/protocol.go`
- Test: `internal/volume/apply_test.go`, `internal/volume/steps_test.go`, `internal/volume/protocol_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `volume.State{Level int, Muted bool}` with JSON tags `level`/`muted`; `volume.Controller` with `Get(context.Context) (State, error)`, `Set(context.Context, State) error`, `Available() bool`; `volume.ErrNoSession`, `volume.ErrUnsupported`; `volume.Apply(current State, level *int, muted *bool) State`; `volume.Clamp(int) int`; `volume.MinLevel`/`MaxLevel`; `volume.LevelFromStep(step, stepCount uint32) int`; `volume.StepFromLevel(level int, stepCount uint32) uint32`; `volume.Op` with `OpGet`/`OpSet`; `volume.HelperFlag`; `volume.EncodeResult(State, error) string`; `volume.DecodeResult(string) (State, error)`; `volume.FormatHelperArgs(Op, State) []string`; `volume.ParseHelperArgs([]string) (Op, State, error)`.

This task is the entire testable core. Everything in it is pure Go with no platform dependency, which is deliberate: the two Windows files added later contain no branch worth reasoning about because all the reasoning lives here.

- [ ] **Step 1: Write the failing tests**

Create `internal/volume/apply_test.go`:

```go
package volume

import "testing"

func intPtr(v int) *int    { return &v }
func boolPtr(v bool) *bool { return &v }

func TestApply(t *testing.T) {
	tests := []struct {
		name    string
		current State
		level   *int
		muted   *bool
		want    State
	}{
		{"a level change clears mute", State{Level: 30, Muted: true}, intPtr(60), nil, State{Level: 60, Muted: false}},
		{"muting preserves the level", State{Level: 60}, nil, boolPtr(true), State{Level: 60, Muted: true}},
		{"unmuting restores the same level", State{Level: 60, Muted: true}, nil, boolPtr(false), State{Level: 60, Muted: false}},
		{"an explicit mute wins over the implicit unmute", State{Level: 30}, intPtr(60), boolPtr(true), State{Level: 60, Muted: true}},
		{"neither field leaves the state alone", State{Level: 42, Muted: true}, nil, nil, State{Level: 42, Muted: true}},
		{"a negative level clamps to zero", State{Level: 50}, intPtr(-5), nil, State{Level: 0}},
		{"an over-range level clamps to a hundred", State{Level: 50}, intPtr(105), nil, State{Level: 100}},
		// Sliding to zero is a volume of zero, not a mute — the device stays
		// unmuted so nudging the slider back up is audible immediately.
		{"zero is a level, not a mute", State{Level: 50, Muted: true}, intPtr(0), nil, State{Level: 0, Muted: false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Apply(tt.current, tt.level, tt.muted); got != tt.want {
				t.Errorf("Apply(%+v, level, muted) = %+v, want %+v", tt.current, got, tt.want)
			}
		})
	}
}

func TestClamp(t *testing.T) {
	for _, tt := range []struct{ in, want int }{
		{-1000, 0}, {-1, 0}, {0, 0}, {50, 50}, {100, 100}, {101, 100}, {1000, 100},
	} {
		if got := Clamp(tt.in); got != tt.want {
			t.Errorf("Clamp(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
```

Create `internal/volume/steps_test.go`:

```go
package volume

import "testing"

func TestLevelFromStep(t *testing.T) {
	tests := []struct {
		name      string
		step      uint32
		stepCount uint32
		want      int
	}{
		// Windows commonly reports 101 steps, which maps exactly onto 0-100.
		{"101 steps, silent", 0, 101, 0},
		{"101 steps, halfway", 50, 101, 50},
		{"101 steps, full", 100, 101, 100},
		{"17 steps, halfway", 8, 17, 50},
		{"17 steps, full", 16, 17, 100},
		{"rounds to nearest", 1, 3, 50},
		// Degenerate device reports must not divide by zero.
		{"one step is always full", 0, 1, 100},
		{"no steps reported", 0, 0, 0},
		// A step beyond the count is saturated rather than exceeding 100.
		{"step past the end", 200, 101, 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LevelFromStep(tt.step, tt.stepCount); got != tt.want {
				t.Errorf("LevelFromStep(%d, %d) = %d, want %d", tt.step, tt.stepCount, got, tt.want)
			}
		})
	}
}

func TestStepFromLevel(t *testing.T) {
	tests := []struct {
		name      string
		level     int
		stepCount uint32
		want      uint32
	}{
		{"101 steps, silent", 0, 101, 0},
		{"101 steps, halfway", 50, 101, 50},
		{"101 steps, full", 100, 101, 100},
		{"17 steps, halfway", 50, 17, 8},
		{"17 steps, full", 100, 17, 16},
		{"out-of-range level is clamped first", 250, 101, 100},
		{"negative level is clamped first", -5, 101, 0},
		{"one step", 50, 1, 0},
		{"no steps reported", 50, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StepFromLevel(tt.level, tt.stepCount); got != tt.want {
				t.Errorf("StepFromLevel(%d, %d) = %d, want %d", tt.level, tt.stepCount, got, tt.want)
			}
		})
	}
}

func TestStepRoundTripIsStable(t *testing.T) {
	// Converting a level to a step and back must not drift, or repeatedly
	// nudging the slider would walk the volume away from where it was put.
	for _, stepCount := range []uint32{101, 51, 17, 2} {
		for level := 0; level <= 100; level++ {
			step := StepFromLevel(level, stepCount)
			back := LevelFromStep(step, stepCount)
			again := StepFromLevel(back, stepCount)
			if again != step {
				t.Fatalf("stepCount=%d level=%d: step %d -> level %d -> step %d, want a stable round trip",
					stepCount, level, step, back, again)
			}
		}
	}
}
```

Create `internal/volume/protocol_test.go`:

```go
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
	line := EncodeResult(State{Level: 45}, errors.New("no default playback device"))
	if !strings.Contains(line, "no default playback device") {
		t.Fatalf("encoded = %q, want it to carry the error text", line)
	}
	if _, err := DecodeResult(line); err == nil {
		t.Error("DecodeResult() error = nil for an error payload, want an error")
	}
	// A failed read must not arrive looking like a successful one.
	if strings.Contains(line, `"level":45`) {
		t.Errorf("encoded = %q, want no state reported alongside an error", line)
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/volume/ -v`
Expected: FAIL — `undefined: Apply`, `undefined: LevelFromStep`, `undefined: EncodeResult`.

- [ ] **Step 3: Write the types**

Create `internal/volume/controller.go`:

```go
// Package volume controls the master volume of the machine's default playback
// device.
//
// Unlike everything else Shutdowner does, this cannot be done from the service
// itself. Windows audio endpoints are per-session and a LocalSystem service
// lives in session 0, which has none, so the Windows implementation spawns a
// helper into the logged-in user's session. See windows.go.
package volume

import (
	"context"
	"errors"
)

// State is the master volume of the default playback device. Level is whole
// percent; the device's own step scale never escapes this package.
type State struct {
	Level int  `json:"level"`
	Muted bool `json:"muted"`
}

var (
	// ErrNoSession means nobody is signed in at the PC, so no session holds an
	// audio endpoint. It is a temporary condition rather than a failure, and
	// the web layer reports it as 503.
	ErrNoSession = errors.New("volume: nobody is signed in at the PC")

	// ErrUnsupported is what the non-Windows build returns.
	ErrUnsupported = errors.New("volume: not supported on this platform")
)

type Controller interface {
	Get(ctx context.Context) (State, error)
	Set(ctx context.Context, s State) error

	// Available reports whether a session is attached to the console. It must
	// stay cheap enough for the 3-second status poll, so it queries the session
	// manager and never spawns a helper. It is deliberately optimistic: a
	// machine at the lock screen reports true while Get and Set may still fail
	// with ErrNoSession, which is the authoritative answer.
	Available() bool
}
```

Create `internal/volume/apply.go`:

```go
package volume

// A level is whole percent.
const (
	MinLevel = 0
	MaxLevel = 100
)

// Apply resolves a partial request against the current state.
//
// A level change clears mute — that is the whole rule, in one place, so the
// browser and the server can never disagree about it. An explicit muted value
// still wins, because the caller said what it wanted.
func Apply(current State, level *int, muted *bool) State {
	next := current
	if level != nil {
		next.Level = Clamp(*level)
		next.Muted = false
	}
	if muted != nil {
		next.Muted = *muted
	}
	return next
}

// Clamp bounds a level to [MinLevel, MaxLevel].
func Clamp(level int) int {
	if level < MinLevel {
		return MinLevel
	}
	if level > MaxLevel {
		return MaxLevel
	}
	return level
}
```

Create `internal/volume/steps.go`:

```go
package volume

// Windows exposes master volume as a number of discrete steps rather than a
// percentage, and the step-based COM methods are the only ones whose arguments
// are all integers — the scalar ones take a float, which cannot cross
// syscall.SyscallN correctly on amd64. These two functions are the whole
// translation, and being ordinary arithmetic they are the one part of the audio
// path that can be tested without Windows.

// LevelFromStep converts a device step index into whole percent. stepCount is
// the total number of steps, so the highest valid index is stepCount-1.
func LevelFromStep(step, stepCount uint32) int {
	if stepCount == 0 {
		return 0
	}
	if stepCount == 1 {
		// A device with a single step is either on or off, and it is on.
		return MaxLevel
	}
	top := stepCount - 1
	if step > top {
		step = top
	}
	// Rounded rather than truncated so a halfway step reads as 50, not 49.
	return int((uint64(step)*uint64(MaxLevel) + uint64(top)/2) / uint64(top))
}

// StepFromLevel converts whole percent into a device step index.
func StepFromLevel(level int, stepCount uint32) uint32 {
	if stepCount <= 1 {
		return 0
	}
	level = Clamp(level)
	top := stepCount - 1
	return uint32((uint64(level)*uint64(top) + MaxLevel/2) / MaxLevel)
}
```

- [ ] **Step 4: Write the wire format**

Create `internal/volume/protocol.go`:

```go
package volume

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Op is what the helper is being asked to do.
type Op string

const (
	OpGet Op = "get"
	OpSet Op = "set"
)

// HelperFlag puts the binary into helper mode. It is deliberately not a
// registered flag: it is an internal calling convention between the service and
// the copy of itself it spawns.
const HelperFlag = "--audio-helper"

// result is a successful reading. Neither field carries omitempty: a level of 0
// is silence, which is a real reading and must survive the round trip.
//
// Audio failures travel in the payload rather than in the exit code, so a
// helper that could not initialise COM stays distinguishable from one that
// could not be launched at all.
type result struct {
	Level int  `json:"level"`
	Muted bool `json:"muted"`
}

// errorResult is a failure. It carries no state at all.
type errorResult struct {
	Error string `json:"error"`
}

// EncodeResult renders the helper's one output line.
//
// A failure is encoded as an error-only object rather than an error field
// beside a zeroed state: the wire format itself guarantees a failed read cannot
// be mistaken for a successful reading of silence, instead of relying on the
// decoder checking the fields in the right order.
func EncodeResult(s State, opErr error) string {
	var v any = result{Level: s.Level, Muted: s.Muted}
	if opErr != nil {
		v = errorResult{Error: opErr.Error()}
	}
	b, err := json.Marshal(v)
	if err != nil {
		// An int, a bool and a string cannot fail to marshal; if that ever
		// changes, fail in the shape the caller already parses.
		return `{"error":"volume: encoding the helper result failed"}`
	}
	return string(b)
}

// DecodeResult parses the helper's output.
func DecodeResult(line string) (State, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return State{}, errors.New("volume: the helper produced no output")
	}
	// Both shapes are accepted here so a failure is recognised whichever way it
	// was encoded; the error is checked first regardless.
	var r struct {
		Level int    `json:"level"`
		Muted bool   `json:"muted"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &r); err != nil {
		return State{}, fmt.Errorf("volume: unreadable helper output %q: %w", line, err)
	}
	if r.Error != "" {
		return State{}, fmt.Errorf("volume: the helper reported: %s", r.Error)
	}
	return State{Level: Clamp(r.Level), Muted: r.Muted}, nil
}

// FormatHelperArgs builds a helper command line, leading with HelperFlag. The
// wanted state is ignored for OpGet.
func FormatHelperArgs(op Op, want State) []string {
	switch op {
	case OpGet:
		return []string{HelperFlag, string(OpGet)}
	case OpSet:
		return []string{
			HelperFlag, string(OpSet),
			strconv.Itoa(Clamp(want.Level)),
			strconv.FormatBool(want.Muted),
		}
	}
	return nil
}

// ParseHelperArgs reads what FormatHelperArgs produced. args excludes
// HelperFlag itself.
func ParseHelperArgs(args []string) (Op, State, error) {
	if len(args) == 0 {
		return "", State{}, errors.New("volume: no helper operation given")
	}
	switch Op(args[0]) {
	case OpGet:
		if len(args) != 1 {
			return "", State{}, fmt.Errorf("volume: get takes no arguments, got %d", len(args)-1)
		}
		return OpGet, State{}, nil
	case OpSet:
		if len(args) != 3 {
			return "", State{}, fmt.Errorf("volume: set takes a level and a mute flag, got %d arguments", len(args)-1)
		}
		level, err := strconv.Atoi(args[1])
		if err != nil {
			return "", State{}, fmt.Errorf("volume: level %q is not a number: %w", args[1], err)
		}
		muted, err := strconv.ParseBool(args[2])
		if err != nil {
			return "", State{}, fmt.Errorf("volume: mute flag %q is not a boolean: %w", args[2], err)
		}
		return OpSet, State{Level: Clamp(level), Muted: muted}, nil
	}
	return "", State{}, fmt.Errorf("volume: unknown helper operation %q", args[0])
}
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go test ./internal/volume/ -v
go test ./...
GOOS=windows GOARCH=amd64 go build ./...
```

Expected: PASS for every subtest; both builds silent.

- [ ] **Step 6: Commit**

```bash
gofmt -w . && go test ./... && GOOS=windows GOARCH=amd64 go build ./...
git add internal/volume/
git commit -m "feat: volume state, interaction rule, step arithmetic and wire format"
```

---

### Task 2: The fake and the non-Windows build

**Files:**
- Create: `internal/volume/fake.go`, `internal/volume/unsupported.go`, `internal/volume/helper_other.go`
- Test: `internal/volume/fake_test.go`, `internal/volume/unsupported_test.go`

**Interfaces:**
- Consumes: `State`, `Controller`, `ErrUnsupported`, `EncodeResult` from Task 1.
- Produces: `volume.Fake` with `NewFake() *Fake`, `SetState(State)`, `SetAvailable(bool)`, `SetGetError(error)`, `SetSetError(error)`, `Calls() []Call`; `volume.Call{State State}`; `volume.New() Controller` for `!windows`; `volume.RunHelper(args []string) string` for `!windows`.

`fake.go` carries **no build tag** and is **not** a `_test.go` file, exactly as `power/fake.go` does: it backs both the unit tests and the production `--fake-volume` flag, so it must compile everywhere.

A `!windows` file is mandatory, not optional. A package whose every file is `//go:build windows` fails on Linux with "build constraints exclude all Go files in ...".

- [ ] **Step 1: Write the failing tests**

Create `internal/volume/fake_test.go`:

```go
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
```

Create `internal/volume/unsupported_test.go`:

```go
//go:build !windows

package volume

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNewOffWindowsRefusesEverything(t *testing.T) {
	c := New()
	if _, err := c.Get(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Get() error = %v, want ErrUnsupported", err)
	}
	if err := c.Set(context.Background(), State{Level: 50}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Set() error = %v, want ErrUnsupported", err)
	}
	if c.Available() {
		t.Error("Available() = true off Windows, want false")
	}
}

func TestRunHelperOffWindowsReportsInTheNormalShape(t *testing.T) {
	line := RunHelper([]string{"get"})
	if _, err := DecodeResult(line); err == nil {
		t.Error("DecodeResult() error = nil, want the unsupported error")
	}
	if !strings.Contains(line, "not supported") {
		t.Errorf("RunHelper() = %q, want it to say the platform is unsupported", line)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/volume/ -run 'Fake|OffWindows' -v`
Expected: FAIL — `undefined: NewFake`, `undefined: New`, `undefined: RunHelper`.

- [ ] **Step 3: Write the fake**

Create `internal/volume/fake.go`:

```go
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

func (f *Fake) Set(_ context.Context, s State) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{State: s})
	if f.setErr != nil {
		return f.setErr
	}
	f.state = s
	return nil
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
```

- [ ] **Step 4: Write the non-Windows build**

Create `internal/volume/unsupported.go`:

```go
//go:build !windows

package volume

import "context"

type systemController struct{}

// New returns the controller for the current platform. Off Windows there is no
// audio endpoint to reach, so the dashboard degrades to disabled controls
// rather than erroring; use --fake-volume for development.
func New() Controller { return systemController{} }

func (systemController) Get(context.Context) (State, error) { return State{}, ErrUnsupported }

func (systemController) Set(context.Context, State) error { return ErrUnsupported }

func (systemController) Available() bool { return false }
```

Create `internal/volume/helper_other.go`:

```go
//go:build !windows

package volume

// RunHelper is the entry point for the --audio-helper subcommand. Off Windows
// there is nothing to talk to, so it reports that in the same single-line shape
// the Windows build uses — the caller parses one format, never two.
func RunHelper([]string) string {
	return EncodeResult(State{}, ErrUnsupported)
}
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go test ./internal/volume/ -v
go test ./...
GOOS=windows GOARCH=amd64 go build ./...
```

Expected: PASS. The Windows build still succeeds because nothing calls `volume.New()` yet — the Windows implementation lands in Task 3.

- [ ] **Step 6: Commit**

```bash
gofmt -w . && go test ./... && GOOS=windows GOARCH=amd64 go build ./...
git add internal/volume/
git commit -m "feat: volume fake and non-Windows controller"
```

---

### Task 3: Windows controller — token acquisition and the session hop

**Files:**
- Create: `internal/volume/windows.go`

**Interfaces:**
- Consumes: `State`, `Controller`, `Op`, `OpGet`, `OpSet`, `ErrNoSession`, `FormatHelperArgs`, `DecodeResult` from Task 1.
- Produces: the `windows` build of `volume.New() Controller`.

This task has no unit tests: it is a thin wrapper over syscalls that cannot execute here. Its gate is that it compiles and vets cleanly for Windows and that the Linux suite still passes. Runtime behaviour is covered by the manual checklist in Task 7.

**Why this is shorter than you expect.** `syscall.SysProcAttr` on Windows carries a `Token` field, and `syscall/exec_windows.go` calls `CreateProcessAsUser` whenever it is set. So `os/exec` handles process creation, pipe setup, stdout capture and waiting. All this file does is obtain a token and hand it over.

- [ ] **Step 1: Confirm the Windows target has no controller yet**

Run: `GOOS=windows GOARCH=amd64 go doc shutdowner/internal/volume New`
Expected: an error reporting no symbol `New` — the Windows build compiles but exposes no constructor, which is the gap this task fills.

- [ ] **Step 2: Write the implementation**

Create `internal/volume/windows.go`:

```go
//go:build windows

package volume

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// helperTimeout bounds one helper run. A hung COM call must fail one request,
// not wedge the service.
const helperTimeout = 5 * time.Second

// noSession is what WTSGetActiveConsoleSessionId returns when no session is
// attached to the physical console.
const noSession = 0xFFFFFFFF

type systemController struct{}

func New() Controller { return systemController{} }

// Available asks the session manager whether a session is attached to the
// console. It never spawns a helper, so it is cheap enough for the 3-second
// status poll.
//
// It is deliberately optimistic: a machine sitting at the lock screen has a
// session attached but no token to query, so Get and Set may still fail with
// ErrNoSession. This only drives the disabled state in the UI; the error is the
// authoritative answer.
func (systemController) Available() bool {
	return windows.WTSGetActiveConsoleSessionId() != noSession
}

func (c systemController) Get(ctx context.Context) (State, error) {
	return c.run(ctx, OpGet, State{})
}

func (c systemController) Set(ctx context.Context, s State) error {
	_, err := c.run(ctx, OpSet, s)
	return err
}

// run spawns the helper inside the logged-in user's session and reads its one
// line of JSON.
//
// The session hop is the entire point of this file. Audio endpoints are
// per-session and this process is in session 0, which has none, so the work has
// to happen in a process that belongs to the user's session. Impersonating the
// user on this thread is not enough: the MMDevice API resolves the endpoint
// from the process's session, not the thread's token.
func (systemController) run(ctx context.Context, op Op, want State) (State, error) {
	args := FormatHelperArgs(op, want)
	if args == nil {
		return State{}, fmt.Errorf("volume: unknown operation %q", op)
	}

	session := windows.WTSGetActiveConsoleSessionId()
	if session == noSession {
		return State{}, ErrNoSession
	}

	var token windows.Token
	if err := windows.WTSQueryUserToken(session, &token); err != nil {
		// Nobody is signed in, or the console is sitting at the lock screen.
		// Either way there is no user to borrow, and it is not an error worth
		// logging a stack over.
		return State{}, ErrNoSession
	}
	defer token.Close()

	exe, err := os.Executable()
	if err != nil {
		return State{}, fmt.Errorf("volume: locating the executable: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, helperTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, exe, args...)
	// Setting Token is what makes os/exec use CreateProcessAsUser, which is
	// what puts the helper in the user's session. HideWindow keeps a console
	// from flashing on their screen every time the slider moves.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Token:      syscall.Token(token),
		HideWindow: true,
	}

	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return State{}, fmt.Errorf("volume: the helper did not finish within %s", helperTimeout)
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if stderr := strings.TrimSpace(string(exit.Stderr)); stderr != "" {
				return State{}, fmt.Errorf("volume: running the helper: %w: %s", err, stderr)
			}
		}
		return State{}, fmt.Errorf("volume: running the helper: %w", err)
	}
	return DecodeResult(string(out))
}
```

- [ ] **Step 3: Verify the Windows build and vet**

```bash
GOOS=windows GOARCH=amd64 go build ./...
GOOS=windows GOARCH=amd64 go vet ./...
go test ./...
go build ./...
```

Expected: all four silent or passing. The `!windows` tests from Task 2 still pass on Linux; `unsupported.go` and `windows.go` never compile together.

- [ ] **Step 4: Commit**

```bash
gofmt -w .
git add internal/volume/windows.go
git commit -m "feat: spawn the audio helper into the logged-in user's session"
```

---

### Task 4: The COM helper

**Files:**
- Create: `internal/volume/helper_windows.go`

**Interfaces:**
- Consumes: `State`, `Op`, `OpGet`, `OpSet`, `ParseHelperArgs`, `EncodeResult`, `LevelFromStep`, `StepFromLevel` from Task 1.
- Produces: the `windows` build of `volume.RunHelper(args []string) string`.

**This is the highest-risk file in the repository.** It cannot be compiled and run anywhere except the target machine — only cross-compiled and vetted. A wrong vtable index does not fail to build; it calls a different method at runtime. Transcribe the indices exactly and do not reorder anything.

**Read this before writing a line.** Do **not** reach for `SetMasterVolumeLevelScalar` or `GetMasterVolumeLevelScalar`, however natural they look. They take a `float`, and on Windows amd64 floats are passed in XMM registers while `syscall.SyscallN` places every argument in an integer register. Such a call compiles, runs, reports success, and sets a garbage volume. The step-based methods below take only integers and pointers, which is why they are used.

Every COM interface begins with the three `IUnknown` methods, so slot 0 is `QueryInterface`, 1 is `AddRef`, 2 is `Release`, and an interface's own methods start at slot 3.

| Interface | Method | Slot |
|---|---|---|
| `IMMDeviceEnumerator` | `GetDefaultAudioEndpoint` | 4 |
| `IMMDevice` | `Activate` | 3 |
| `IAudioEndpointVolume` | `SetMute` | 14 |
| `IAudioEndpointVolume` | `GetMute` | 15 |
| `IAudioEndpointVolume` | `GetVolumeStepInfo` | 16 |
| `IAudioEndpointVolume` | `VolumeStepUp` | 17 |
| `IAudioEndpointVolume` | `VolumeStepDown` | 18 |

- [ ] **Step 1: Write the implementation**

Create `internal/volume/helper_windows.go`:

```go
//go:build windows

package volume

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// This file is the only place Shutdowner talks COM, and it runs only inside the
// logged-in user's session, never in the service.
//
// Go has no bindings for IAudioEndpointVolume and the project holds itself to
// three dependencies, so the interfaces are called through their vtables by
// hand. Every COM interface starts with IUnknown's three methods, so an
// interface's own methods begin at slot 3.
//
// Only the step-based volume methods are used. The scalar ones take a float,
// which travels in an XMM register on amd64 while syscall.SyscallN uses integer
// registers — that call would silently set a garbage volume.

var (
	modole32 = windows.NewLazySystemDLL("ole32.dll")

	procCoInitializeEx   = modole32.NewProc("CoInitializeEx")
	procCoUninitialize   = modole32.NewProc("CoUninitialize")
	procCoCreateInstance = modole32.NewProc("CoCreateInstance")
)

const (
	coinitApartmentThreaded = 0x2
	clsctxAll               = 0x17

	// EDataFlow and ERole, from mmdeviceapi.h.
	eRender     = 0
	eMultimedia = 1

	// A device is unlikely to report more steps than this; a wildly larger
	// number means we misread the struct, and stepping that many times would
	// hang the helper until its timeout.
	maxPlausibleSteps = 1000
)

// CLSID_MMDeviceEnumerator {BCDE0395-E52F-467C-8E3D-C4579291692E}
var clsidMMDeviceEnumerator = windows.GUID{
	Data1: 0xBCDE0395, Data2: 0xE52F, Data3: 0x467C,
	Data4: [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E},
}

// IID_IMMDeviceEnumerator {A95664D2-9614-4F35-A746-DE8DB63617E6}
var iidIMMDeviceEnumerator = windows.GUID{
	Data1: 0xA95664D2, Data2: 0x9614, Data3: 0x4F35,
	Data4: [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6},
}

// IID_IAudioEndpointVolume {5CDF2C82-841E-4546-9722-0CF74078229A}
var iidIAudioEndpointVolume = windows.GUID{
	Data1: 0x5CDF2C82, Data2: 0x841E, Data3: 0x4546,
	Data4: [8]byte{0x97, 0x22, 0x0C, 0xF7, 0x40, 0x78, 0x22, 0x9A},
}

// call invokes vtable slot on the COM object at ptr, passing ptr as the
// implicit `this`. It treats a negative HRESULT as an error.
func call(ptr uintptr, slot int, args ...uintptr) error {
	vtbl := *(**[64]uintptr)(unsafe.Pointer(ptr))
	all := append([]uintptr{ptr}, args...)
	hr, _, _ := syscall.SyscallN(vtbl[slot], all...)
	if int32(hr) < 0 {
		return fmt.Errorf("HRESULT 0x%08X", uint32(hr))
	}
	return nil
}

// release drops a reference. IUnknown::Release is slot 2 and returns a
// reference count rather than an HRESULT, so it does not go through call.
func release(ptr uintptr) {
	if ptr == 0 {
		return
	}
	vtbl := *(**[64]uintptr)(unsafe.Pointer(ptr))
	_, _, _ = syscall.SyscallN(vtbl[2], ptr)
}

// RunHelper executes one audio operation and returns the single line of JSON
// the service reads from the helper's stdout. args excludes HelperFlag.
func RunHelper(args []string) string {
	op, want, err := ParseHelperArgs(args)
	if err != nil {
		return EncodeResult(State{}, err)
	}
	state, err := runOp(op, want)
	return EncodeResult(state, err)
}

func runOp(op Op, want State) (State, error) {
	// COM is initialised per thread, and the goroutine must not migrate to
	// another one while the interfaces are live.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	// S_FALSE means COM was already initialised on this thread, which is fine;
	// only a negative HRESULT is a failure.
	if int32(hr) < 0 {
		return State{}, fmt.Errorf("volume: CoInitializeEx: HRESULT 0x%08X", uint32(hr))
	}
	defer procCoUninitialize.Call()

	endpoint, err := defaultEndpointVolume()
	if err != nil {
		return State{}, err
	}
	defer release(endpoint)

	switch op {
	case OpGet:
		return readState(endpoint)
	case OpSet:
		if err := writeState(endpoint, want); err != nil {
			return State{}, err
		}
		// Report what the device actually settled on rather than what was
		// asked for: a device with coarse steps cannot hit every percentage,
		// and the UI should show the truth.
		return readState(endpoint)
	}
	return State{}, fmt.Errorf("volume: unknown operation %q", op)
}

// defaultEndpointVolume returns an IAudioEndpointVolume for the default
// playback device. The caller releases it.
func defaultEndpointVolume() (uintptr, error) {
	var enumerator uintptr
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)),
		0,
		clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)),
		uintptr(unsafe.Pointer(&enumerator)),
	)
	if int32(hr) < 0 {
		return 0, fmt.Errorf("volume: creating the device enumerator: HRESULT 0x%08X", uint32(hr))
	}
	defer release(enumerator)

	var device uintptr
	// IMMDeviceEnumerator::GetDefaultAudioEndpoint, slot 4.
	if err := call(enumerator, 4, eRender, eMultimedia, uintptr(unsafe.Pointer(&device))); err != nil {
		return 0, fmt.Errorf("volume: no default playback device: %w", err)
	}
	defer release(device)

	var endpoint uintptr
	// IMMDevice::Activate, slot 3.
	if err := call(device, 3,
		uintptr(unsafe.Pointer(&iidIAudioEndpointVolume)),
		clsctxAll,
		0,
		uintptr(unsafe.Pointer(&endpoint)),
	); err != nil {
		return 0, fmt.Errorf("volume: activating the volume interface: %w", err)
	}
	return endpoint, nil
}

// stepInfo reads the device's current step and how many steps it has.
func stepInfo(endpoint uintptr) (step, stepCount uint32, err error) {
	// IAudioEndpointVolume::GetVolumeStepInfo, slot 16.
	if err := call(endpoint, 16,
		uintptr(unsafe.Pointer(&step)),
		uintptr(unsafe.Pointer(&stepCount)),
	); err != nil {
		return 0, 0, fmt.Errorf("volume: reading the step info: %w", err)
	}
	if stepCount > maxPlausibleSteps {
		return 0, 0, fmt.Errorf("volume: the device reported %d steps, which is not plausible", stepCount)
	}
	return step, stepCount, nil
}

func readState(endpoint uintptr) (State, error) {
	step, stepCount, err := stepInfo(endpoint)
	if err != nil {
		return State{}, err
	}
	var muted int32
	// IAudioEndpointVolume::GetMute, slot 15.
	if err := call(endpoint, 15, uintptr(unsafe.Pointer(&muted))); err != nil {
		return State{}, fmt.Errorf("volume: reading the mute flag: %w", err)
	}
	return State{Level: LevelFromStep(step, stepCount), Muted: muted != 0}, nil
}

func writeState(endpoint uintptr, want State) error {
	step, stepCount, err := stepInfo(endpoint)
	if err != nil {
		return err
	}

	target := StepFromLevel(want.Level, stepCount)
	// Walk to the target one step at a time. There is no integer method that
	// sets a step directly; the scalar one that would takes a float and cannot
	// be called correctly through SyscallN.
	for step < target {
		// IAudioEndpointVolume::VolumeStepUp, slot 17.
		if err := call(endpoint, 17, 0); err != nil {
			return fmt.Errorf("volume: stepping up: %w", err)
		}
		step++
	}
	for step > target {
		// IAudioEndpointVolume::VolumeStepDown, slot 18.
		if err := call(endpoint, 18, 0); err != nil {
			return fmt.Errorf("volume: stepping down: %w", err)
		}
		step--
	}

	var muted int32
	if want.Muted {
		muted = 1
	}
	// IAudioEndpointVolume::SetMute, slot 14.
	if err := call(endpoint, 14, uintptr(muted), 0); err != nil {
		return fmt.Errorf("volume: setting the mute flag: %w", err)
	}
	return nil
}
```

- [ ] **Step 2: Verify the Windows build and vet**

```bash
GOOS=windows GOARCH=amd64 go build ./...
GOOS=windows GOARCH=amd64 go vet ./...
go test ./...
```

Expected: all silent or passing. Vet matters more here than anywhere else in the project — it is the only automated check that reaches `unsafe.Pointer` misuse in this file.

- [ ] **Step 3: Re-read the vtable indices against the table above**

Compare every `call(...)` slot number in the file against the table in this task, one line at a time. This is not busywork: it is the only review this code gets before it runs on real hardware, and a transposed index produces a working build that does the wrong thing.

- [ ] **Step 4: Commit**

```bash
gofmt -w .
git add internal/volume/helper_windows.go
git commit -m "feat: read and write the default device volume over COM"
```

---

### Task 5: Web layer — endpoints, routes, and the availability flag

**Files:**
- Create: `internal/web/volume_handlers.go`
- Modify: `internal/web/server.go` (add `Volume` to `Options`, `volume` to `Server`, two routes), `internal/web/api_handlers.go` (add `AudioAvailable` to `statusResponse`), `internal/web/web_test.go` (extend `newTestEnv`)
- Test: `internal/web/volume_handlers_test.go`

**Interfaces:**
- Consumes: `volume.Controller`, `volume.State`, `volume.Apply`, `volume.ErrNoSession`, `volume.NewFake` from Tasks 1-2.
- Produces: `(*Server).handleVolumeGet`, `(*Server).handleVolumeSet`; the `volumeRequest` JSON shape `{level?: int, muted?: bool}`; `statusResponse.AudioAvailable` with JSON tag `audioAvailable`; `testEnv.volume` for later tasks.

- [ ] **Step 1: Write the failing tests**

Create `internal/web/volume_handlers_test.go`:

```go
package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shutdowner/internal/volume"
)

func getVolume(t *testing.T, e *testEnv) (*httptest.ResponseRecorder, volume.State) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/volume", nil)
	r.AddCookie(e.sessionCookie(t))
	res := do(t, e.handler, r)

	var got volume.State
	if res.Code == http.StatusOK {
		if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding: %v (body %q)", err, res.Body.String())
		}
	}
	return res, got
}

func TestVolumeGetReturnsTheCurrentState(t *testing.T) {
	e := newTestEnv(t)
	e.volume.SetState(volume.State{Level: 37, Muted: true})

	res, got := getVolume(t, e)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	if got != (volume.State{Level: 37, Muted: true}) {
		t.Errorf("state = %+v, want level 37 muted", got)
	}
}

func TestVolumeRoutesRequireASession(t *testing.T) {
	e := newTestEnv(t)
	for _, tt := range []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/volume", ""},
		{http.MethodPost, "/api/volume", `{"level":10}`},
	} {
		r := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
		if res := do(t, e.handler, r); res.Code != http.StatusUnauthorized {
			t.Errorf("%s %s status = %d, want 401", tt.method, tt.path, res.Code)
		}
	}
}

func TestVolumeSetRequiresCSRF(t *testing.T) {
	e := newTestEnv(t)
	r := httptest.NewRequest(http.MethodPost, "/api/volume", strings.NewReader(`{"level":10}`))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(e.sessionCookie(t))

	if res := do(t, e.handler, r); res.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", res.Code)
	}
}

func TestVolumeSetAppliesALevel(t *testing.T) {
	e := newTestEnv(t)
	e.volume.SetState(volume.State{Level: 10, Muted: true})

	res := postJSON(t, e, "/api/volume", `{"level":60}`)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", res.Code, res.Body.String())
	}

	var got volume.State
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	// The response is the resolved state, so the browser never has to
	// reimplement the unmute rule.
	if got != (volume.State{Level: 60, Muted: false}) {
		t.Errorf("state = %+v, want level 60 unmuted", got)
	}
	calls := e.volume.Calls()
	if len(calls) != 1 || calls[0].State != (volume.State{Level: 60}) {
		t.Errorf("Calls() = %+v, want one call setting level 60 unmuted", calls)
	}
}

func TestVolumeSetAppliesMuteWithoutTouchingTheLevel(t *testing.T) {
	e := newTestEnv(t)
	e.volume.SetState(volume.State{Level: 60})

	res := postJSON(t, e, "/api/volume", `{"muted":true}`)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	var got volume.State
	_ = json.Unmarshal(res.Body.Bytes(), &got)
	if got != (volume.State{Level: 60, Muted: true}) {
		t.Errorf("state = %+v, want level 60 muted", got)
	}
}

func TestVolumeSetHonoursAnExplicitMuteAlongsideALevel(t *testing.T) {
	e := newTestEnv(t)
	e.volume.SetState(volume.State{Level: 10})

	res := postJSON(t, e, "/api/volume", `{"level":60,"muted":true}`)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	var got volume.State
	_ = json.Unmarshal(res.Body.Bytes(), &got)
	// The caller said what it wanted; the implicit unmute must not override it.
	if got != (volume.State{Level: 60, Muted: true}) {
		t.Errorf("state = %+v, want level 60 muted", got)
	}
}

func TestVolumeSetRejectsAnEmptyIntent(t *testing.T) {
	e := newTestEnv(t)
	res := postJSON(t, e, "/api/volume", `{}`)
	if res.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.Code)
	}
	if len(e.volume.Calls()) != 0 {
		t.Error("an empty request still reached the controller")
	}
}

func TestVolumeSetRejectsMalformedJSON(t *testing.T) {
	e := newTestEnv(t)
	if res := postJSON(t, e, "/api/volume", `not json`); res.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.Code)
	}
}

func TestVolumeReportsNoSessionAsUnavailable(t *testing.T) {
	e := newTestEnv(t)
	e.volume.SetGetError(volume.ErrNoSession)
	e.volume.SetSetError(volume.ErrNoSession)

	if res, _ := getVolume(t, e); res.Code != http.StatusServiceUnavailable {
		t.Errorf("GET status = %d, want 503", res.Code)
	}
	if res := postJSON(t, e, "/api/volume", `{"level":10}`); res.Code != http.StatusServiceUnavailable {
		t.Errorf("POST status = %d, want 503", res.Code)
	}
}

func TestVolumeReportsOtherFailuresAsServerErrors(t *testing.T) {
	e := newTestEnv(t)
	e.volume.SetGetError(errors.New("the helper did not finish within 5s"))

	if res, _ := getVolume(t, e); res.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", res.Code)
	}
}

func TestStatusCarriesAudioAvailability(t *testing.T) {
	e := newTestEnv(t)

	_, body := getJSON(t, e, "/api/status")
	if body["audioAvailable"] != true {
		t.Errorf("audioAvailable = %v, want true", body["audioAvailable"])
	}

	e.volume.SetAvailable(false)
	_, body = getJSON(t, e, "/api/status")
	if body["audioAvailable"] != false {
		t.Errorf("audioAvailable = %v, want false", body["audioAvailable"])
	}
}

func TestOversizedVolumeBodyIsRejected(t *testing.T) {
	e := newTestEnv(t)
	huge := `{"level":50,"pad":"` + strings.Repeat("x", 16<<10) + `"}`
	if res := postJSON(t, e, "/api/volume", huge); res.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 — the body cap must apply here too", res.Code)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/web/ -run Volume -v`
Expected: FAIL — `e.volume` undefined, and 404s for the unrouted paths.

- [ ] **Step 3: Extend the test harness**

In `internal/web/web_test.go`, add a `volume` field to `testEnv`:

```go
type testEnv struct {
	srv     *Server
	fake    *power.Fake
	volume  *volume.Fake
	actions *action.Manager
	limiter *auth.Limiter
	handler http.Handler
}
```

and in `newTestEnv`, create the fake, pass it to `New`, and return it. Add `"shutdowner/internal/volume"` to the imports.

```go
	fake := power.NewFake()
	vol := volume.NewFake()
	actions := action.New(fake)
	limiter := auth.NewLimiter(auth.DefaultPerIPLimit, auth.DefaultGlobalLimit, auth.DefaultWindow)

	srv, err := New(Options{
		Sessions:     auth.NewSessionManager([]byte("0123456789abcdef0123456789abcdef"), time.Hour),
		Limiter:      limiter,
		Actions:      actions,
		Power:        fake,
		Volume:       vol,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		PasswordHash: hash,
		// A zero delay is not used here: tests need a countdown long enough to
		// observe the pending state before it fires.
		DelaySeconds: 45,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return &testEnv{srv: srv, fake: fake, volume: vol, actions: actions, limiter: limiter, handler: srv.Routes()}
```

- [ ] **Step 4: Wire the controller into the server**

In `internal/web/server.go`: add `Volume volume.Controller` to `Options`, add `volume volume.Controller` to `Server`, add `"shutdowner/internal/volume"` to the imports, require it in `New`'s nil check alongside the other collaborators, and assign it in the returned struct.

Extend the `New` guard so a missing controller fails at construction rather than at the first request:

```go
	if o.Sessions == nil || o.Limiter == nil || o.Actions == nil || o.Power == nil || o.Volume == nil || o.Logger == nil {
		return nil, errors.New("web: Sessions, Limiter, Actions, Power, Volume and Logger are all required")
	}
```

Then add the two routes to `Routes`, immediately after the `/api/dismiss` line, keeping `limitBody` outermost as every other POST does:

```go
	mux.HandleFunc("GET /api/volume", s.requireSession(s.handleVolumeGet))
	mux.HandleFunc("POST /api/volume", limitBody(maxRequestBody, s.requireSession(s.requireCSRF(s.handleVolumeSet))))
```

- [ ] **Step 5: Add the availability flag to the status payload**

In `internal/web/api_handlers.go`, add the field to `statusResponse`:

```go
type statusResponse struct {
	sysinfo.Info
	Capabilities   power.Capabilities `json:"capabilities"`
	AudioAvailable bool               `json:"audioAvailable"`
	State          action.State       `json:"state"`
	Pending        *action.Pending    `json:"pending"`
	Missed         *action.Missed     `json:"missed"`
	Error          string             `json:"error"`
}
```

and populate it in `handleStatus`:

```go
	writeJSON(w, http.StatusOK, statusResponse{
		Info:           sysinfo.Collect(),
		Capabilities:   s.capabilities(r.Context()),
		AudioAvailable: s.volume.Available(),
		State:          st.State,
		Pending:        st.Pending,
		Missed:         st.Missed,
		Error:          st.Error,
	})
```

- [ ] **Step 6: Write the handlers**

Create `internal/web/volume_handlers.go`:

```go
package web

import (
	"encoding/json"
	"errors"
	"net/http"

	"shutdowner/internal/volume"
)

// volumeRequest is a partial intent. The fields are pointers so that "not
// specified" is distinguishable from zero — a level of 0 is a real request.
type volumeRequest struct {
	Level *int  `json:"level"`
	Muted *bool `json:"muted"`
}

func (s *Server) handleVolumeGet(w http.ResponseWriter, r *http.Request) {
	state, err := s.volume.Get(r.Context())
	if err != nil {
		s.writeVolumeError(w, err, "reading the volume")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleVolumeSet(w http.ResponseWriter, r *http.Request) {
	var req volumeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "malformed request body")
		return
	}
	if req.Level == nil && req.Muted == nil {
		writeJSONError(w, http.StatusBadRequest, "specify a level, a mute flag, or both")
		return
	}

	// Read before writing so the unmute rule resolves against what the device
	// is actually doing, not against what a browser last saw.
	current, err := s.volume.Get(r.Context())
	if err != nil {
		s.writeVolumeError(w, err, "reading the volume")
		return
	}

	want := volume.Apply(current, req.Level, req.Muted)
	if err := s.volume.Set(r.Context(), want); err != nil {
		s.writeVolumeError(w, err, "setting the volume")
		return
	}

	s.logger.Info("volume changed", "level", want.Level, "muted", want.Muted, "ip", ClientIP(r))
	writeJSON(w, http.StatusOK, want)
}

// writeVolumeError maps a controller failure onto a status code. Nobody being
// signed in is a temporary condition rather than a fault, so it is 503 and is
// not logged as an error — it is the expected state of a PC at the lock screen.
func (s *Server) writeVolumeError(w http.ResponseWriter, err error, doing string) {
	if errors.Is(err, volume.ErrNoSession) || errors.Is(err, volume.ErrUnsupported) {
		writeJSONError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	s.logger.Error(doing, "error", err)
	writeJSONError(w, http.StatusInternalServerError, err.Error())
}
```

- [ ] **Step 7: Run the tests to verify they pass**

```bash
go test ./internal/web/ -v
go test ./...
GOOS=windows GOARCH=amd64 go build ./...
```

Expected: PASS, including every pre-existing web test — `newTestEnv` changed, so a break there means the harness edit was wrong.

- [ ] **Step 8: Commit**

```bash
gofmt -w . && go test ./... && GOOS=windows GOARCH=amd64 go build ./...
git add internal/web/
git commit -m "feat: volume endpoints and the audio availability flag"
```

---

### Task 6: The dashboard controls

**Files:**
- Modify: `internal/web/templates/dashboard.html`, `internal/web/static/app.css`, `internal/web/static/app.js`
- Test: `internal/web/static_test.go` (extend), `internal/web/api_handlers_test.go` (extend)

**Interfaces:**
- Consumes: `GET /api/volume`, `POST /api/volume`, and `audioAvailable` from Task 5.
- Produces: the element ids `volume-row`, `volume-mute`, `volume-slider`, `volume-readout` that the script drives.

- [ ] **Step 1: Write the failing tests**

Add to `internal/web/api_handlers_test.go`:

```go
func TestDashboardRendersTheVolumeRow(t *testing.T) {
	e := newTestEnv(t)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(e.sessionCookie(t))
	body := do(t, e.handler, r).Body.String()

	for _, want := range []string{`id="volume-row"`, `id="volume-mute"`, `id="volume-slider"`, `id="volume-readout"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the dashboard does not contain %s", want)
		}
	}
	// The slider must be bounded in the markup, not only in JavaScript.
	if !strings.Contains(body, `min="0"`) || !strings.Contains(body, `max="100"`) {
		t.Error("the slider is not bounded to 0-100 in the markup")
	}
}
```

Add to `internal/web/static_test.go`:

```go
func TestClientScriptDrivesTheVolumeControls(t *testing.T) {
	e := newTestEnv(t)
	res := do(t, e.handler, httptest.NewRequest(http.MethodGet, "/static/app.js", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	js := res.Body.String()

	for _, want := range []string{"/api/volume", "volume-slider", "volume-mute", "audioAvailable"} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js does not reference %q", want)
		}
	}
	// Sending on every input event would fire a request per pixel of drag.
	if !strings.Contains(js, `"change"`) {
		t.Error("app.js does not listen for the slider's change event")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/web/ -run 'VolumeRow|ClientScriptDrives' -v`
Expected: FAIL — the ids are absent from the template and the script.

- [ ] **Step 3: Add the markup**

In `internal/web/templates/dashboard.html`, insert this section immediately after the closing `</section>` of the actions block and before the `pending` section:

```html
    <section class="volume" id="volume-row">
      <button type="button" id="volume-mute" class="icon" aria-pressed="false" aria-label="Mute">🔊</button>
      <input type="range" id="volume-slider" min="0" max="100" step="1" value="0" aria-label="Volume">
      <span id="volume-readout" class="muted">—</span>
    </section>
```

The row renders in its unknown state — readout an em dash, slider at 0 — because the page is served before the volume has been read. The script fills it in on load, and an em dash is honest where "0%" would assert a level nobody has checked.

- [ ] **Step 4: Add the styles**

Append to `internal/web/static/app.css`:

```css
.volume {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.5rem 0 0.75rem;
  border-top: 1px solid var(--line);
}

.volume input[type="range"] {
  flex: 1;
  min-width: 0;
  accent-color: var(--accent);
  height: 2rem;
}

.volume button.icon {
  min-height: 2.5rem;
  min-width: 2.5rem;
  padding: 0;
  font-size: 1.1rem;
  line-height: 1;
}

#volume-readout {
  min-width: 3ch;
  text-align: right;
  font-variant-numeric: tabular-nums;
}

.volume[hidden],
.volume.disabled input,
.volume.disabled button {
  opacity: 0.45;
}
```

`tabular-nums` keeps the readout from twitching as the digits change width while dragging.

- [ ] **Step 5: Add the client behaviour**

In `internal/web/static/app.js`, first add `volume: null` to the `state` object.

**Placement matters.** This whole block must sit *above* the trailing
`setInterval`/`poll()` lines at the end of the file. `var` declarations hoist
but assignments do not, so if `applyStatus` runs before this block executes,
`slider` is still `undefined` and the status handler throws on the first poll.
Putting the block before `poll()` guarantees the handles are assigned first.

```js
  // ---- volume -------------------------------------------------------------

  var volumeRow = el("volume-row");
  var muteButton = el("volume-mute");
  var slider = el("volume-slider");
  var readout = el("volume-readout");

  function renderVolume() {
    if (!state.volume) {
      slider.value = 0;
      readout.textContent = "—";
      muteButton.textContent = "🔊";
      muteButton.setAttribute("aria-pressed", "false");
      return;
    }
    slider.value = state.volume.level;
    readout.textContent = state.volume.level + "%";
    muteButton.textContent = state.volume.muted ? "🔇" : "🔊";
    muteButton.setAttribute("aria-pressed", state.volume.muted ? "true" : "false");
    muteButton.setAttribute("aria-label", state.volume.muted ? "Unmute" : "Mute");
  }

  function setVolumeEnabled(enabled) {
    slider.disabled = !enabled;
    muteButton.disabled = !enabled;
    volumeRow.classList.toggle("disabled", !enabled);
    var reason = enabled ? "" : "Nobody is signed in at the PC";
    slider.title = reason;
    muteButton.title = reason;
    if (!enabled) {
      state.volume = null;
      renderVolume();
    }
  }

  async function loadVolume() {
    try {
      var res = await fetch("/api/volume", { headers: { Accept: "application/json" } });
      if (res.status === 401) {
        window.location.href = "/login";
        return;
      }
      if (!res.ok) {
        // 503 means nobody is signed in; anything else is a real failure. In
        // both cases the honest thing is to stop claiming a level.
        setVolumeEnabled(false);
        return;
      }
      state.volume = await res.json();
      setVolumeEnabled(true);
      renderVolume();
    } catch (e) {
      setVolumeEnabled(false);
    }
  }

  async function sendVolume(payload) {
    try {
      showError("");
      state.volume = await post("/api/volume", payload);
      setVolumeEnabled(true);
      renderVolume();
    } catch (e) {
      showError(e.message);
      // Put the controls back where the server last said they were, rather
      // than leaving the slider showing a change that did not take.
      renderVolume();
    }
  }

  // Live feedback while dragging, but only one request when the drag ends.
  slider.addEventListener("input", function () {
    readout.textContent = slider.value + "%";
  });
  slider.addEventListener("change", function () {
    sendVolume({ level: parseInt(slider.value, 10) });
  });

  muteButton.addEventListener("click", function () {
    var muted = state.volume ? !state.volume.muted : true;
    sendVolume({ muted: muted });
  });

  loadVolume();
```

Then, inside `applyStatus`, keep the controls in step with the PC without ever polling the volume itself. Add this immediately after the capability handling for the action buttons:

```js
    // The status poll only says whether anyone is signed in; the level itself
    // is never polled. Coming back from unavailable is the moment to re-read it.
    if (data.audioAvailable && slider.disabled) {
      loadVolume();
    } else if (!data.audioAvailable && !slider.disabled) {
      setVolumeEnabled(false);
    }
```

- [ ] **Step 6: Run the tests to verify they pass**

```bash
go test ./internal/web/ -v
node --check internal/web/static/app.js
go test ./...
GOOS=windows GOARCH=amd64 go build ./...
```

Expected: PASS, and the syntax check silent.

- [ ] **Step 7: Cross-check every element id**

List every `el("...")` and `getElementById` in `app.js` and confirm each exists in `dashboard.html`. A typo here produces a page that silently does nothing, and no Go test will catch it. Report the list in your notes.

- [ ] **Step 8: Commit**

```bash
gofmt -w . && go test ./...
git add internal/web/
git commit -m "feat: mute toggle and volume slider on the dashboard"
```

---

### Task 7: Wiring, the helper subcommand, and the docs

**Files:**
- Modify: `cmd/shutdowner/main.go`, `README.md`
- Test: `cmd/shutdowner/main_test.go` (extend)

**Interfaces:**
- Consumes: `volume.New`, `volume.NewFake`, `volume.RunHelper`, `volume.HelperFlag` from Tasks 1-4; `web.Options.Volume` from Task 5.
- Produces: the `--audio-helper` and `--fake-volume` command-line behaviour.

- [ ] **Step 1: Write the failing test**

Add to `cmd/shutdowner/main_test.go`:

```go
func TestAudioHelperFlagIsDetectedBeforeFlagParsing(t *testing.T) {
	// The helper flag is an internal calling convention, not a registered
	// flag, so flag.Parse would reject it. It must be recognised from the raw
	// argument list first.
	if !isAudioHelper([]string{"shutdowner.exe", volume.HelperFlag, "get"}) {
		t.Error("isAudioHelper() = false for a helper invocation")
	}
	if isAudioHelper([]string{"shutdowner.exe", "--console"}) {
		t.Error("isAudioHelper() = true for an ordinary invocation")
	}
	if isAudioHelper([]string{"shutdowner.exe"}) {
		t.Error("isAudioHelper() = true for no arguments at all")
	}
}

func TestAudioHelperArgsAreEverythingAfterTheFlag(t *testing.T) {
	got := audioHelperArgs([]string{"shutdowner.exe", volume.HelperFlag, "set", "45", "false"})
	want := []string{"set", "45", "false"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("audioHelperArgs() = %v, want %v", got, want)
	}
}
```

Add `"reflect"` and `"shutdowner/internal/volume"` to that file's imports.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/shutdowner/ -run AudioHelper -v`
Expected: FAIL — `undefined: isAudioHelper`.

- [ ] **Step 3: Add the helper dispatch**

In `cmd/shutdowner/main.go`, add the flag next to the existing ones:

```go
	flagFakeVolume = flag.Bool("fake-volume", false, "development: keep the volume in memory instead of touching the audio device")
```

Then add these two functions and the early dispatch. `HelperFlag` is deliberately not a registered flag — `flag.Parse` would reject an unknown one — so it is matched against the raw arguments before parsing:

```go
// isAudioHelper reports whether this process was spawned as the audio helper.
// The flag is an internal calling convention between the service and the copy
// of itself it launches into the user's session, not part of the public CLI.
func isAudioHelper(argv []string) bool {
	return len(argv) > 1 && argv[1] == volume.HelperFlag
}

// audioHelperArgs returns the operation arguments following the helper flag.
func audioHelperArgs(argv []string) []string {
	if !isAudioHelper(argv) {
		return nil
	}
	return argv[2:]
}
```

and at the very top of `main`, before `flag.Parse()`:

```go
func main() {
	// The helper runs before anything else: it loads no config, opens no log
	// file and binds no socket. It prints one line and exits.
	if isAudioHelper(os.Args) {
		fmt.Println(volume.RunHelper(audioHelperArgs(os.Args)))
		return
	}

	flag.Parse()
	...
```

- [ ] **Step 4: Wire the controller into the server**

In `runServer`, alongside the existing power controller:

```go
	var vol volume.Controller = volume.New()
	if *flagFakeVolume {
		logger.Warn("running with --fake-volume: the audio device will not be touched")
		vol = volume.NewFake()
	}
```

and pass it to `web.New`:

```go
		Volume:       vol,
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go test ./cmd/shutdowner/ -v
go test ./...
go test -race ./...
GOOS=windows GOARCH=amd64 go vet ./...
GOOS=windows GOARCH=amd64 go build ./...
gofmt -l .
make build-windows
```

Expected: all pass; `gofmt -l .` silent; `dist/shutdowner.exe` produced.

- [ ] **Step 6: Exercise it end to end on Linux**

```bash
go run ./cmd/shutdowner --init --config ./.env.vol
go run ./cmd/shutdowner --hash-password        # paste the printed line into .env.vol
go run ./cmd/shutdowner --console --fake-power --fake-volume --config ./.env.vol
```

In another shell, log in with `curl` keeping a cookie jar, scrape `data-csrf` from the dashboard HTML, then:

```bash
curl -s -b jar http://127.0.0.1:8080/api/volume                                  # expect level 50
curl -s -b jar -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
     -d '{"level":30}' http://127.0.0.1:8080/api/volume                          # expect level 30, muted false
curl -s -b jar -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
     -d '{"muted":true}' http://127.0.0.1:8080/api/volume                        # expect level 30, muted true
curl -s -b jar -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
     -d '{"level":70}' http://127.0.0.1:8080/api/volume                          # expect level 70, muted FALSE
curl -s -b jar http://127.0.0.1:8080/api/status | grep -o '"audioAvailable":[a-z]*'
```

The fourth call is the one that matters: it proves the unmute rule end to end, through the real handler, not just in a unit test. Then open the dashboard in a browser, drag the slider, and confirm exactly one request per drag in the console.

Delete `.env.vol` afterwards and confirm `git status` is clean of it. Paste the transcript into your report.

- [ ] **Step 7: Update the README**

Add `--fake-volume` to the command-line table. Add a short "Volume" subsection under how it behaves:

> The dashboard's mute button and slider control the master volume of the
> default playback device. Because Windows audio endpoints are per-session and
> the service runs in session 0, the service spawns a copy of itself into the
> signed-in user's session to do the work. When nobody is signed in the controls
> disable themselves, and re-enable on the next poll once someone signs in.

Then add these to the verification checklist:

```
10. The dashboard shows the PC's real volume when it loads.
11. The slider sets an absolute level; check the number against the Windows
    volume mixer.
12. Mute silences; unmute returns to exactly the previous level.
13. Dragging the slider while muted unmutes.
14. No console window flashes on the PC's screen when the slider moves.
15. With the screen locked but a user still signed in, the controls still work.
16. After signing out entirely the controls disable with a reason, and re-enable
    on the next poll after signing back in.
17. After a fast-user-switch, changes affect the newly active session.
18. Dragging the slider end to end completes promptly — the helper walks one
    device step at a time, so a device reporting an unusual number of steps
    would show up here as a slow response.
```

Items 15, 16 and 17 are the ones most likely to reveal problems; fast-user-switch is the least certain.

- [ ] **Step 8: Commit**

```bash
gofmt -w . && go test ./... && GOOS=windows GOARCH=amd64 go build ./...
git add cmd/ README.md
git commit -m "feat: wire up the volume controller and the audio helper subcommand"
```

---

## Done

`make build-windows` produces a single `.exe` that still has exactly three
dependencies. Everything above the syscalls is tested on Linux, including the
step arithmetic that would otherwise have been unverifiable. What remains for
the target machine is the vtable indices, the session hop, and the nine
checklist items above.
