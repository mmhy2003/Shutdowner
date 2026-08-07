package web

import (
	"encoding/json"
	"errors"
	"net/http"

	"shutdowner/internal/volume"
)

// volumeRequest is a partial intent. The fields are pointers so that "not
// specified" is distinguishable from zero — a level of 0 is a real request.
type volumeRequest struct {
	Level *int  `json:"level"`
	Muted *bool `json:"muted"`
}

func (s *Server) handleVolumeGet(w http.ResponseWriter, r *http.Request) {
	state, err := s.volume.Get(r.Context())
	if err != nil {
		s.writeVolumeError(w, err, "reading the volume")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleVolumeSet(w http.ResponseWriter, r *http.Request) {
	var req volumeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "malformed request body")
		return
	}
	if req.Level == nil && req.Muted == nil {
		writeJSONError(w, http.StatusBadRequest, "specify a level, a mute flag, or both")
		return
	}

	// Read before writing so the unmute rule resolves against what the device
	// is actually doing, not against what a browser last saw.
	current, err := s.volume.Get(r.Context())
	if err != nil {
		s.writeVolumeError(w, err, "reading the volume")
		return
	}

	want := volume.Apply(current, req.Level, req.Muted)
	// The response is what the device settled on, not what was asked for: a
	// device with coarse steps rounds a request to the nearest one it has, and
	// the status poll never re-reads the level, so an echo of the request would
	// leave the dashboard showing a number the PC is not at until a reload.
	got, err := s.volume.Set(r.Context(), want)
	if err != nil {
		s.writeVolumeError(w, err, "setting the volume")
		return
	}

	s.logger.Info("volume changed", "level", want.Level, "muted", want.Muted, "ip", ClientIP(r))
	writeJSON(w, http.StatusOK, got)
}

// writeVolumeError maps a controller failure onto a status code.
//
// Nobody being signed in is a temporary, expected condition rather than a
// fault, so it is 503, it is logged at Info rather than Error, and its message
// is safe to show — it tells the user exactly what to do about it. It is logged
// at all because the Windows controller wraps the OS error behind ErrNoSession,
// and that detail separates an idle PC from a process missing SeTcbPrivilege.
//
// Anything else is unclassified, and its detail stays in the log rather than
// the response: a helper that fails to start wraps an *exec.Error whose text
// names the executable's absolute path. This matches handleAction, which masks
// its own 500 the same way while surfacing the classified cases it knows are
// safe.
func (s *Server) writeVolumeError(w http.ResponseWriter, err error, doing string) {
	if errors.Is(err, volume.ErrNoSession) || errors.Is(err, volume.ErrUnsupported) {
		// Not an error condition — an idle PC is expected — but the underlying
		// reason is worth having when someone is trying to work out why the
		// controls are greyed out.
		s.logger.Info(doing, "unavailable", err)
		writeJSONError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	s.logger.Error(doing, "error", err)
	writeJSONError(w, http.StatusInternalServerError, "could not reach the audio device")
}
