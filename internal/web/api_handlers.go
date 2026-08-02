package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"shutdowner/internal/action"
	"shutdowner/internal/power"
	"shutdowner/internal/sysinfo"
)

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
	Missed       *action.Missed     `json:"missed"`
	Error        string             `json:"error"`
}

type actionRequest struct {
	Action power.Action `json:"action"`
	Force  bool         `json:"force"`
	// A pointer so that an absent field is distinguishable from an explicit
	// zero, which is legal and means "at the next tick".
	DelaySeconds *int   `json:"delaySeconds"`
	At           string `json:"at"`
}

type actionResponse struct {
	ID               string `json:"id"`
	RemainingSeconds int    `json:"remainingSeconds"`
	FiresAtLocal     string `json:"firesAtLocal"`
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
		Missed:       st.Missed,
		Error:        st.Error,
	})
}

// handleAction relies on the limitBody middleware for its body cap; an
// oversized body surfaces here as a decode error.
func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	var req actionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "malformed request body")
		return
	}

	firesAt, err := action.ResolveWhen(time.Now(), req.DelaySeconds, req.At, s.delay)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	pending, err := s.actions.Schedule(r.Context(), req.Action, req.Force, firesAt)
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
		"remainingSeconds", pending.RemainingSeconds, "firesAt", pending.FiresAtLocal, "ip", ClientIP(r))
	writeJSON(w, http.StatusAccepted, actionResponse{
		ID:               pending.ID,
		RemainingSeconds: pending.RemainingSeconds,
		FiresAtLocal:     pending.FiresAtLocal,
	})
}

func (s *Server) handleAbort(w http.ResponseWriter, r *http.Request) {
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

// handleDismiss clears a missed action. It takes no body and never conflicts:
// dismissing nothing is a success, so two tabs racing produce no error anybody
// has to explain.
func (s *Server) handleDismiss(w http.ResponseWriter, r *http.Request) {
	s.actions.Dismiss()
	s.logger.Info("missed action dismissed", "ip", ClientIP(r))
	writeJSON(w, http.StatusOK, map[string]string{"status": "dismissed"})
}
