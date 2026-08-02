//go:build !windows

package power

import "context"

type systemController struct{}

// New returns the controller for the current platform. Off Windows every action
// fails; use --fake-power for development.
func New() Controller { return systemController{} }

func (systemController) Execute(context.Context, Action, bool) error {
	return ErrUnsupported
}

func (systemController) Capabilities(context.Context) (Capabilities, error) {
	return Capabilities{}, ErrUnsupported
}
