//go:build !windows

package volume

import "context"

type systemController struct{}

// New returns the controller for the current platform. Off Windows there is no
// audio endpoint to reach, so the dashboard degrades to disabled controls
// rather than erroring; use --fake-volume for development.
func New() Controller { return systemController{} }

func (systemController) Get(context.Context) (State, error) { return State{}, ErrUnsupported }

func (systemController) Set(context.Context, State) (State, error) { return State{}, ErrUnsupported }

func (systemController) Available() bool { return false }
