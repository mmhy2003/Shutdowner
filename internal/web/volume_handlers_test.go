package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shutdowner/internal/volume"
)

func getVolume(t *testing.T, e *testEnv) (*httptest.ResponseRecorder, volume.State) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/volume", nil)
	r.AddCookie(e.sessionCookie(t))
	res := do(t, e.handler, r)

	var got volume.State
	if res.Code == http.StatusOK {
		if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding: %v (body %q)", err, res.Body.String())
		}
	}
	return res, got
}

func TestVolumeGetReturnsTheCurrentState(t *testing.T) {
	e := newTestEnv(t)
	e.volume.SetState(volume.State{Level: 37, Muted: true})

	res, got := getVolume(t, e)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	if got != (volume.State{Level: 37, Muted: true}) {
		t.Errorf("state = %+v, want level 37 muted", got)
	}
}

func TestVolumeRoutesRequireASession(t *testing.T) {
	e := newTestEnv(t)
	for _, tt := range []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/volume", ""},
		{http.MethodPost, "/api/volume", `{"level":10}`},
	} {
		r := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
		if res := do(t, e.handler, r); res.Code != http.StatusUnauthorized {
			t.Errorf("%s %s status = %d, want 401", tt.method, tt.path, res.Code)
		}
	}
}

func TestVolumeSetRequiresCSRF(t *testing.T) {
	e := newTestEnv(t)
	r := httptest.NewRequest(http.MethodPost, "/api/volume", strings.NewReader(`{"level":10}`))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(e.sessionCookie(t))

	if res := do(t, e.handler, r); res.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", res.Code)
	}
}

func TestVolumeSetAppliesALevel(t *testing.T) {
	e := newTestEnv(t)
	e.volume.SetState(volume.State{Level: 10, Muted: true})

	res := postJSON(t, e, "/api/volume", `{"level":60}`)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", res.Code, res.Body.String())
	}

	var got volume.State
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	// The response is the resolved state, so the browser never has to
	// reimplement the unmute rule.
	if got != (volume.State{Level: 60, Muted: false}) {
		t.Errorf("state = %+v, want level 60 unmuted", got)
	}
	calls := e.volume.Calls()
	if len(calls) != 1 || calls[0].State != (volume.State{Level: 60}) {
		t.Errorf("Calls() = %+v, want one call setting level 60 unmuted", calls)
	}
}

// snappingController models a device with coarse steps: it rounds every
// requested level to the nearest 25. The Fake cannot stand in here, because a
// fake that stores exactly what it is given can never show the difference
// between what was asked for and what happened.
type snappingController struct{ state volume.State }

func (c *snappingController) Get(context.Context) (volume.State, error) { return c.state, nil }

func (c *snappingController) Set(_ context.Context, s volume.State) (volume.State, error) {
	c.state = volume.State{Level: ((s.Level + 12) / 25) * 25, Muted: s.Muted}
	return c.state, nil
}

func (c *snappingController) Available() bool { return true }

func TestVolumeSetReturnsWhatTheDeviceSettledOn(t *testing.T) {
	e := newTestEnv(t)
	e.srv.volume = &snappingController{}

	res := postJSON(t, e, "/api/volume", `{"level":45}`)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", res.Code, res.Body.String())
	}
	var got volume.State
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if got.Level != 50 {
		t.Errorf("level = %d, want 50 — the response must report what the device did, not what was asked", got.Level)
	}
}

func TestVolumeSetAppliesMuteWithoutTouchingTheLevel(t *testing.T) {
	e := newTestEnv(t)
	e.volume.SetState(volume.State{Level: 60})

	res := postJSON(t, e, "/api/volume", `{"muted":true}`)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	var got volume.State
	_ = json.Unmarshal(res.Body.Bytes(), &got)
	if got != (volume.State{Level: 60, Muted: true}) {
		t.Errorf("state = %+v, want level 60 muted", got)
	}
}

func TestVolumeSetHonoursAnExplicitMuteAlongsideALevel(t *testing.T) {
	e := newTestEnv(t)
	e.volume.SetState(volume.State{Level: 10})

	res := postJSON(t, e, "/api/volume", `{"level":60,"muted":true}`)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	var got volume.State
	_ = json.Unmarshal(res.Body.Bytes(), &got)
	// The caller said what it wanted; the implicit unmute must not override it.
	if got != (volume.State{Level: 60, Muted: true}) {
		t.Errorf("state = %+v, want level 60 muted", got)
	}
}

func TestVolumeSetRejectsAnEmptyIntent(t *testing.T) {
	e := newTestEnv(t)
	res := postJSON(t, e, "/api/volume", `{}`)
	if res.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.Code)
	}
	if len(e.volume.Calls()) != 0 {
		t.Error("an empty request still reached the controller")
	}
}

func TestVolumeSetRejectsMalformedJSON(t *testing.T) {
	e := newTestEnv(t)
	if res := postJSON(t, e, "/api/volume", `not json`); res.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.Code)
	}
}

func TestVolumeReportsNoSessionAsUnavailable(t *testing.T) {
	e := newTestEnv(t)
	e.volume.SetGetError(volume.ErrNoSession)
	e.volume.SetSetError(volume.ErrNoSession)

	if res, _ := getVolume(t, e); res.Code != http.StatusServiceUnavailable {
		t.Errorf("GET status = %d, want 503", res.Code)
	}
	if res := postJSON(t, e, "/api/volume", `{"level":10}`); res.Code != http.StatusServiceUnavailable {
		t.Errorf("POST status = %d, want 503", res.Code)
	}
}

func TestVolumeReportsOtherFailuresAsServerErrors(t *testing.T) {
	e := newTestEnv(t)
	e.volume.SetGetError(errors.New("the helper did not finish within 5s"))

	if res, _ := getVolume(t, e); res.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", res.Code)
	}
}

func TestStatusCarriesAudioAvailability(t *testing.T) {
	e := newTestEnv(t)

	_, body := getJSON(t, e, "/api/status")
	if body["audioAvailable"] != true {
		t.Errorf("audioAvailable = %v, want true", body["audioAvailable"])
	}

	e.volume.SetAvailable(false)
	_, body = getJSON(t, e, "/api/status")
	if body["audioAvailable"] != false {
		t.Errorf("audioAvailable = %v, want false", body["audioAvailable"])
	}
}

func TestOversizedVolumeBodyIsRejected(t *testing.T) {
	e := newTestEnv(t)
	huge := `{"level":50,"pad":"` + strings.Repeat("x", 16<<10) + `"}`
	if res := postJSON(t, e, "/api/volume", huge); res.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 — the body cap must apply here too", res.Code)
	}
}

func TestVolumeServerErrorsDoNotLeakInternalDetail(t *testing.T) {
	e := newTestEnv(t)
	// The shape a failed helper spawn produces: the wrapped error names the
	// executable's absolute path.
	e.volume.SetGetError(errors.New(`volume: could not start the helper: exec: "C:\Program Files\Shutdowner\shutdowner.exe": file does not exist`))

	res, _ := getVolume(t, e)
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", res.Code)
	}
	body := res.Body.String()
	if strings.Contains(body, "shutdowner.exe") || strings.Contains(body, "Program Files") {
		t.Errorf("response body = %q, want no executable path", body)
	}
	if !strings.Contains(body, "could not reach the audio device") {
		t.Errorf("response body = %q, want the generic message", body)
	}
}

func TestVolumeUnavailableStillExplainsItself(t *testing.T) {
	e := newTestEnv(t)
	e.volume.SetGetError(volume.ErrNoSession)

	res, _ := getVolume(t, e)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", res.Code)
	}
	// The 503 message is safe and actionable, so masking it would be a
	// regression in the other direction.
	if !strings.Contains(res.Body.String(), "signed in") {
		t.Errorf("response body = %q, want it to say nobody is signed in", res.Body.String())
	}
}

func TestVolumeUnavailableSurvivesWrapping(t *testing.T) {
	e := newTestEnv(t)
	// The shape the Windows controller now produces: ErrNoSession wrapped
	// around the OS error, so a missing SeTcbPrivilege is distinguishable from
	// an idle PC instead of both collapsing to one bare sentinel.
	e.volume.SetGetError(fmt.Errorf("%w: %v", volume.ErrNoSession, errors.New("Access is denied.")))

	res, _ := getVolume(t, e)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 — wrapping must not turn an idle PC into a 500", res.Code)
	}
	if !strings.Contains(res.Body.String(), "signed in") {
		t.Errorf("response body = %q, want the sentinel's own wording to survive", res.Body.String())
	}
}
