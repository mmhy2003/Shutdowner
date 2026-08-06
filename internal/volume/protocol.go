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

// result is a successful reading. Level and Muted carry no omitempty: a level
// of 0 is silence, which is a real reading and must survive the round trip.
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
// alongside a zeroed state: the wire format itself guarantees a failed read
// cannot be mistaken for a successful reading of silence, instead of relying on
// the decoder checking the fields in the right order.
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
