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

// result is the single line of JSON the helper prints. Audio failures travel in
// Error rather than in the exit code, so a helper that could not initialise COM
// stays distinguishable from one that could not be launched at all.
type result struct {
	Level int    `json:"level"`
	Muted bool   `json:"muted"`
	Error string `json:"error,omitempty"`
}

// EncodeResult renders the helper's one output line. When opErr is non-nil the
// state is dropped, so a failure can never be mistaken for a reading.
func EncodeResult(s State, opErr error) string {
	r := result{Level: s.Level, Muted: s.Muted}
	if opErr != nil {
		r = result{Error: opErr.Error()}
	}
	b, err := json.Marshal(r)
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
	var r result
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
