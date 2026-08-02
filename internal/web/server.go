// Package web serves the login page, the dashboard and the JSON API.
package web

import (
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"

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
	tmpl         *template.Template
	static       http.Handler
	passwordHash string
	delaySeconds int
	delay        time.Duration
}

func New(o Options) (*Server, error) {
	if o.Sessions == nil || o.Limiter == nil || o.Actions == nil || o.Power == nil || o.Logger == nil {
		return nil, errors.New("web: Sessions, Limiter, Actions, Power and Logger are all required")
	}
	if o.PasswordHash == "" {
		return nil, errors.New("web: PasswordHash is required")
	}
	tmpl, err := parseTemplates()
	if err != nil {
		return nil, fmt.Errorf("web: parsing templates: %w", err)
	}
	static, err := staticHandler()
	if err != nil {
		return nil, fmt.Errorf("web: preparing static assets: %w", err)
	}
	return &Server{
		sessions:     o.Sessions,
		limiter:      o.Limiter,
		actions:      o.Actions,
		power:        o.Power,
		logger:       o.Logger,
		tmpl:         tmpl,
		static:       static,
		passwordHash: o.PasswordHash,
		delaySeconds: o.DelaySeconds,
		delay:        time.Duration(o.DelaySeconds) * time.Second,
	}, nil
}

// Routes builds the handler tree.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	// "/{$}" matches only the root path; a bare "/" would swallow every 404.
	mux.HandleFunc("GET /{$}", s.requireSession(s.handleDashboard))
	mux.HandleFunc("GET /login", s.handleLoginForm)
	// limitBody is the outermost wrapper on every POST, so no handler and no
	// middleware that touches the body can be reached without a cap.
	mux.HandleFunc("POST /login", limitBody(maxRequestBody, s.handleLoginSubmit))
	mux.HandleFunc("POST /logout", limitBody(maxRequestBody, s.requireSession(s.requireCSRF(s.handleLogout))))
	mux.HandleFunc("GET /api/status", s.requireSession(s.handleStatus))
	mux.HandleFunc("POST /api/action", limitBody(maxRequestBody, s.requireSession(s.requireCSRF(s.handleAction))))
	mux.HandleFunc("POST /api/abort", limitBody(maxRequestBody, s.requireSession(s.requireCSRF(s.handleAbort))))
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /favicon.ico", handleFavicon)
	mux.Handle("GET /static/", s.static)
	return securityHeaders(recoverPanic(s.logger, mux))
}

// handleHealth is the one unauthenticated route, so the tunnel has something to
// probe.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}
