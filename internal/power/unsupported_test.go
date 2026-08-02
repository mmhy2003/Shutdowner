//go:build !windows

package power

import (
	"context"
	"errors"
	"testing"
)

func TestNewOnNonWindowsRefusesEverything(t *testing.T) {
	c := New()
	if err := c.Execute(context.Background(), ActionShutdown, true); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Execute() error = %v, want ErrUnsupported", err)
	}
	caps, err := c.Capabilities(context.Background())
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("Capabilities() error = %v, want ErrUnsupported", err)
	}
	if caps.Sleep || caps.Hibernate {
		t.Errorf("Capabilities() = %+v, want both false", caps)
	}
}
