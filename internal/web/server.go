// Package web serves the login page, the dashboard and the JSON API.
package web

import (
	"errors"
	"log/slog"
	"net/http"

	"shutdowner/internal/action"
	"shutdowner/internal/auth"
	"shutdowner/internal/power"
)

const SessionCookieName = "shutdowner_session"

type Options struct {
	Sessions     *auth.SessionManager
	Limiter      *auth.Limiter
	Actions      *action.Manager
	Power        power.Controller
	Logger       *slog.Logger
	PasswordHash string
	DelaySeconds int
}

type Server struct {
	sessions     *auth.SessionManager
	limiter      *auth.Limiter
	actions      *action.Manager
	power        power.Controller
	logger       *slog.Logger
	passwordHash string
	delaySeconds int
}

func New(o Options) (*Server, error) {
	if o.Sessions == nil || o.Limiter == nil || o.Actions == nil || o.Power == nil || o.Logger == nil {
		return nil, errors.New("web: Sessions, Limiter, Actions, Power and Logger are all required")
	}
	if o.PasswordHash == "" {
		return nil, errors.New("web: PasswordHash is required")
	}
	return &Server{
		sessions:     o.Sessions,
		limiter:      o.Limiter,
		actions:      o.Actions,
		power:        o.Power,
		logger:       o.Logger,
		passwordHash: o.PasswordHash,
		delaySeconds: o.DelaySeconds,
	}, nil
}

// Routes builds the handler tree. Tasks 12, 13 and 14 extend it.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	return securityHeaders(recoverPanic(s.logger, mux))
}

// handleHealth is the one unauthenticated route, so the tunnel has something to
// probe.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}
