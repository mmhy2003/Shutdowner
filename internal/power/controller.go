// Package power executes machine power state changes. Everything above this
// package talks to Controller, which is what allows the rest of Shutdowner to
// be developed and tested on a non-Windows machine.
package power

import (
	"context"
	"errors"
)

type Action string

const (
	ActionShutdown  Action = "shutdown"
	ActionRestart   Action = "restart"
	ActionSleep     Action = "sleep"
	ActionHibernate Action = "hibernate"
)

func (a Action) Valid() bool {
	switch a {
	case ActionShutdown, ActionRestart, ActionSleep, ActionHibernate:
		return true
	}
	return false
}

// Suspends reports whether the action puts the machine into a low power state
// rather than turning it off. Only suspending actions are capability-gated.
func (a Action) Suspends() bool {
	return a == ActionSleep || a == ActionHibernate
}

// Capabilities describes which suspend states this machine supports. Hibernate
// is commonly unavailable because it has been turned off with powercfg /h off.
type Capabilities struct {
	Sleep     bool `json:"sleep"`
	Hibernate bool `json:"hibernate"`
}

// Allows reports whether a is permitted. Shutdown and restart always are.
func (c Capabilities) Allows(a Action) bool {
	switch a {
	case ActionSleep:
		return c.Sleep
	case ActionHibernate:
		return c.Hibernate
	}
	return true
}

// ErrUnsupported is returned by the non-Windows build of the controller.
var ErrUnsupported = errors.New("power: not supported on this platform")

type Controller interface {
	Execute(ctx context.Context, a Action, force bool) error
	Capabilities(ctx context.Context) (Capabilities, error)
}
