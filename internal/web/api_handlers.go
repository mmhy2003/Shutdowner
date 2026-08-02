package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"shutdowner/internal/action"
	"shutdowner/internal/power"
	"shutdowner/internal/sysinfo"
)

// maxRequestBody bounds the JSON these endpoints will read. Both payloads are
// a few dozen bytes; anything larger is a mistake or an attack, and decoding it
// would allocate without limit.
const maxRequestBody = 4 << 10 // 4 KiB

type dashboardPage struct {
	Info         sysinfo.Info
	Capabilities power.Capabilities
	CSRFToken    string
	DelaySeconds int
}

type statusResponse struct {
	sysinfo.Info
	Capabilities power.Capabilities `json:"capabilities"`
	State        action.State       `json:"state"`
	Pending      *action.Pending    `json:"pending"`
	Error        string             `json:"error"`
}

type actionRequest struct {
	Action power.Action `json:"action"`
	Force  bool         `json:"force"`
}

type actionResponse struct {
	ID               string `json:"id"`
	RemainingSeconds int    `json:"remainingSeconds"`
}

type abortRequest struct {
	ID string `json:"id"`
}

// capabilities never fails the request. A controller error means the optional
// actions are reported unavailable rather than the dashboard breaking, which
// also keeps a transient GetPwrCapabilities failure from taking the UI down.
func (s *Server) capabilities(ctx context.Context) power.Capabilities {
	caps, err := s.power.Capabilities(ctx)
	if err != nil {
		s.logger.Warn("reading power capabilities", "error", err)
		return power.Capabilities{}
	}
	return caps
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	page := dashboardPage{
		Info:         sysinfo.Collect(),
		Capabilities: s.capabilities(r.Context()),
		CSRFToken:    s.sessions.CSRFToken(nonceFrom(r.Context())),
		DelaySeconds: s.delaySeconds,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "dashboard.html", page); err != nil {
		s.logger.Error("rendering the dashboard", "error", err)
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st := s.actions.Status()
	writeJSON(w, http.StatusOK, statusResponse{
		Info:         sysinfo.Collect(),
		Capabilities: s.capabilities(r.Context()),
		State:        st.State,
		Pending:      st.Pending,
		Error:        st.Error,
	})
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	var req actionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "malformed request body")
		return
	}

	pending, err := s.actions.Schedule(r.Context(), req.Action, req.Force)
	if err != nil {
		switch {
		case errors.Is(err, action.ErrInvalidAction), errors.Is(err, action.ErrUnsupportedAction):
			writeJSONError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, action.ErrConflict):
			writeJSONError(w, http.StatusConflict, err.Error())
		default:
			s.logger.Error("scheduling an action", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "could not schedule the action")
		}
		return
	}

	s.logger.Info("action scheduled",
		"action", pending.Action, "force", pending.Force,
		"remainingSeconds", pending.RemainingSeconds, "ip", ClientIP(r))
	writeJSON(w, http.StatusAccepted, actionResponse{
		ID:               pending.ID,
		RemainingSeconds: pending.RemainingSeconds,
	})
}

func (s *Server) handleAbort(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	var req abortRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "malformed request body")
		return
	}
	if err := s.actions.Abort(req.ID); err != nil {
		writeJSONError(w, http.StatusConflict, err.Error())
		return
	}
	s.logger.Info("action aborted", "id", req.ID, "ip", ClientIP(r))
	writeJSON(w, http.StatusOK, map[string]string{"status": "aborted"})
}
