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
