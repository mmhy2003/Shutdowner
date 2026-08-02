package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// App is the runnable server, expressed as a start/stop pair so winsvc can
// drive it either in the foreground or under the service control manager.
type App struct {
	http   *http.Server
	logger *slog.Logger
}

// Start blocks until the server stops. A shutdown requested through Stop
// returns nil rather than ErrServerClosed, so the service reports success.
func (a *App) Start() error {
	a.logger.Info("listening", "addr", a.http.Addr)
	if err := a.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (a *App) Stop() error {
	a.logger.Info("stopping")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return a.http.Shutdown(ctx)
}
