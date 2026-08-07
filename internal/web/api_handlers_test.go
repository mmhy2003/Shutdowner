package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"shutdowner/internal/power"
)

func postJSON(t *testing.T, e *testEnv, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	cookie := e.sessionCookie(t)
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", e.csrfToken(t, cookie))
	r.AddCookie(cookie)
	return do(t, e.handler, r)
}

func getJSON(t *testing.T, e *testEnv, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.AddCookie(e.sessionCookie(t))
	res := do(t, e.handler, r)

	var out map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding %s: %v (body %q)", path, err, res.Body.String())
	}
	return res, out
}

func TestStatusRequiresASession(t *testing.T) {
	e := newTestEnv(t)
	res := do(t, e.handler, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if res.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", res.Code)
	}
}

func TestStatusShapeWhenIdle(t *testing.T) {
	e := newTestEnv(t)
	res, body := getJSON(t, e, "/api/status")

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	for _, key := range []string{"hostname", "os", "uptimeSeconds", "localTime", "capabilities", "state"} {
		if _, ok := body[key]; !ok {
			t.Errorf("the response has no %q key: %v", key, body)
		}
	}
	if body["state"] != "idle" {
		t.Errorf("state = %v, want idle", body["state"])
	}
	if body["pending"] != nil {
		t.Errorf("pending = %v, want null when idle", body["pending"])
	}
}

func TestStatusReportsCapabilities(t *testing.T) {
	e := newTestEnv(t)
	e.fake.SetCapabilities(power.Capabilities{Sleep: true})

	_, body := getJSON(t, e, "/api/status")
	caps, ok := body["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities = %v, want an object", body["capabilities"])
	}
	if caps["sleep"] != true || caps["hibernate"] != false {
		t.Errorf("capabilities = %v, want sleep true and hibernate false", caps)
	}
}

// TestStatusReportsAMissedAction pins that a populated Missed record on the
// manager actually reaches the client: handleStatus hand-copies the field
// from action.Status rather than deriving it, and nothing previously failed
// if that copy were ever deleted — on the one feature whose entire purpose is
// telling the operator the PC did not do what they asked.
func TestStatusReportsAMissedAction(t *testing.T) {
	e := newTestEnv(t)

	// Driving the manager into StateMissed directly, rather than through
	// /api/action, avoids waiting on real time: a deadline already
	// MissedGrace in the past is missed on the very next Tick.
	past := time.Now().Add(-time.Hour)
	if _, err := e.actions.Schedule(context.Background(), power.ActionSleep, false, past); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	e.actions.Tick()
	if s := e.actions.Status(); s.State != "missed" {
		t.Fatalf("setup: State = %q, want missed", s.State)
	}

	_, body := getJSON(t, e, "/api/status")
	missed, ok := body["missed"].(map[string]any)
	if !ok {
		t.Fatalf("missed = %v, want an object", body["missed"])
	}
	if missed["action"] != "sleep" {
		t.Errorf("missed.action = %v, want sleep", missed["action"])
	}
	if wasDueAt, ok := missed["wasDueAt"].(string); !ok || wasDueAt == "" {
		t.Errorf("missed.wasDueAt = %v, want a non-empty string", missed["wasDueAt"])
	}
}

func TestActionSchedulesWithoutExecutingYet(t *testing.T) {
	e := newTestEnv(t)
	res := postJSON(t, e, "/api/action", `{"action":"shutdown","force":true}`)

	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %q)", res.Code, res.Body.String())
	}
	var out struct {
		ID               string `json:"id"`
		RemainingSeconds int    `json:"remainingSeconds"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if out.ID == "" {
		t.Error("no action id returned")
	}
	if out.RemainingSeconds != 45 {
		t.Errorf("remainingSeconds = %d, want 45", out.RemainingSeconds)
	}
	if len(e.fake.Calls()) != 0 {
		t.Error("the action executed immediately instead of after the countdown")
	}
}

func TestSecondActionConflicts(t *testing.T) {
	e := newTestEnv(t)
	postJSON(t, e, "/api/action", `{"action":"shutdown","force":true}`)

	res := postJSON(t, e, "/api/action", `{"action":"restart","force":true}`)
	if res.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", res.Code)
	}
}

func TestUnavailableActionIsRejected(t *testing.T) {
	e := newTestEnv(t)
	e.fake.SetCapabilities(power.Capabilities{Sleep: true})

	res := postJSON(t, e, "/api/action", `{"action":"hibernate","force":false}`)
	if res.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.Code)
	}
}

func TestUnknownActionIsRejected(t *testing.T) {
	e := newTestEnv(t)
	res := postJSON(t, e, "/api/action", `{"action":"explode","force":true}`)
	if res.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.Code)
	}
}

func TestMalformedActionBodyIsRejected(t *testing.T) {
	e := newTestEnv(t)
	res := postJSON(t, e, "/api/action", `not json`)
	if res.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.Code)
	}
}

func TestActionRequiresCSRF(t *testing.T) {
	e := newTestEnv(t)
	r := httptest.NewRequest(http.MethodPost, "/api/action", strings.NewReader(`{"action":"shutdown"}`))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(e.sessionCookie(t))

	if res := do(t, e.handler, r); res.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", res.Code)
	}
}

func TestAbortCancelsThePendingAction(t *testing.T) {
	e := newTestEnv(t)
	sched := postJSON(t, e, "/api/action", `{"action":"shutdown","force":true}`)
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(sched.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	res := postJSON(t, e, "/api/abort", `{"id":"`+out.ID+`"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", res.Code, res.Body.String())
	}
	if s := e.actions.Status(); s.State != "idle" {
		t.Errorf("state = %q, want idle", s.State)
	}
}

func TestAbortWithAStaleIDConflicts(t *testing.T) {
	e := newTestEnv(t)
	postJSON(t, e, "/api/action", `{"action":"shutdown","force":true}`)

	res := postJSON(t, e, "/api/abort", `{"id":"from-an-old-tab"}`)
	if res.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", res.Code)
	}
	if s := e.actions.Status(); s.State != "pending" {
		t.Errorf("state = %q, want the original action still pending", s.State)
	}
}

func TestDashboardRedirectsWithoutASession(t *testing.T) {
	e := newTestEnv(t)
	res := do(t, e.handler, httptest.NewRequest(http.MethodGet, "/", nil))
	if res.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", res.Code)
	}
	if loc := res.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}

func TestDashboardRenders(t *testing.T) {
	e := newTestEnv(t)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(e.sessionCookie(t))
	res := do(t, e.handler, r)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	body := res.Body.String()
	for _, want := range []string{`data-action="shutdown"`, `data-action="restart"`, `data-action="sleep"`, `data-action="hibernate"`, `id="abort"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the dashboard does not contain %q", want)
		}
	}
}

func TestDashboardDisablesUnavailableActions(t *testing.T) {
	e := newTestEnv(t)
	e.fake.SetCapabilities(power.Capabilities{Sleep: true})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(e.sessionCookie(t))
	body := do(t, e.handler, r).Body.String()

	if !strings.Contains(body, "Hibernate is not enabled on this system") {
		t.Error("the hibernate button is not disabled with a reason when hibernation is unavailable")
	}
}

func TestCapabilitiesErrorDoesNotBreakTheDashboard(t *testing.T) {
	e := newTestEnv(t)
	// The dashboard must degrade to "nothing available" rather than 500.
	//
	// This used to swap in power.New(), on the assumption that it was the
	// stub returning ErrUnsupported — which it is on every platform except the
	// one the app ships on. On Windows it was the real controller, so the test
	// asserted that the developer's own machine could neither sleep nor
	// hibernate, and failed on any machine that could do either.
	e.fake.SetCapabilitiesError(errors.New("GetPwrCapabilities failed"))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(e.sessionCookie(t))
	if res := do(t, e.handler, r); res.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", res.Code)
	}

	if caps := e.srv.capabilities(context.Background()); caps.Sleep || caps.Hibernate {
		t.Errorf("capabilities = %+v, want both false on a controller error", caps)
	}
}

func TestOversizedActionBodyIsRejected(t *testing.T) {
	e := newTestEnv(t)

	// Valid JSON, but padded far past the cap with an ignored field.
	huge := `{"action":"shutdown","force":true,"pad":"` + strings.Repeat("x", 16<<10) + `"}`
	res := postJSON(t, e, "/api/action", huge)

	if res.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.Code)
	}
	if len(e.fake.Calls()) != 0 {
		t.Error("an oversized request still scheduled an action")
	}
}

func TestOversizedAbortBodyIsRejected(t *testing.T) {
	e := newTestEnv(t)
	huge := `{"id":"` + strings.Repeat("x", 16<<10) + `"}`
	if res := postJSON(t, e, "/api/abort", huge); res.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.Code)
	}
}

func TestActionAcceptsARelativeSchedule(t *testing.T) {
	e := newTestEnv(t)
	res := postJSON(t, e, "/api/action", `{"action":"shutdown","force":true,"delaySeconds":7200}`)
	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", res.Code, res.Body)
	}
	var got struct {
		RemainingSeconds int `json:"remainingSeconds"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding the response: %v", err)
	}
	// Allow a second of slack for the clock moving during the request.
	if got.RemainingSeconds < 7199 || got.RemainingSeconds > 7200 {
		t.Errorf("remainingSeconds = %d, want about 7200", got.RemainingSeconds)
	}
}

func TestActionRejectsBadSchedules(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"both timing fields", `{"action":"shutdown","delaySeconds":60,"at":"2030-01-01T00:00"}`},
		{"a negative delay", `{"action":"shutdown","delaySeconds":-5}`},
		{"a delay past the horizon", `{"action":"shutdown","delaySeconds":604801}`},
		{"an unparseable at", `{"action":"shutdown","at":"tomorrow"}`},
		{"an at in the past", `{"action":"shutdown","at":"2000-01-01T00:00"}`},
		{"an at past the horizon", `{"action":"shutdown","at":"2099-01-01T00:00"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			res := postJSON(t, e, "/api/action", tt.body)
			if res.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400: %s", res.Code, res.Body)
			}
		})
	}
}

func TestActionWithNoTimingFieldsKeepsTheConfiguredDelay(t *testing.T) {
	e := newTestEnv(t)
	res := postJSON(t, e, "/api/action", `{"action":"shutdown","force":true}`)
	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", res.Code, res.Body)
	}
	var got struct {
		RemainingSeconds int `json:"remainingSeconds"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding the response: %v", err)
	}
	if got.RemainingSeconds < 44 || got.RemainingSeconds > 45 {
		t.Errorf("remainingSeconds = %d, want the configured 45", got.RemainingSeconds)
	}
}

func TestDismissIsIdempotent(t *testing.T) {
	e := newTestEnv(t)
	// Nothing has been missed, and it still succeeds.
	if res := postJSON(t, e, "/api/dismiss", `{}`); res.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: %s", res.Code, res.Body)
	}
}

func TestDismissRequiresCSRF(t *testing.T) {
	e := newTestEnv(t)
	r := httptest.NewRequest(http.MethodPost, "/api/dismiss", strings.NewReader(`{}`))
	r.AddCookie(e.sessionCookie(t))
	r.Header.Set("Content-Type", "application/json")
	if res := do(t, e.handler, r); res.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 without a CSRF token", res.Code)
	}
}

func TestDashboardRendersTheVolumeRow(t *testing.T) {
	e := newTestEnv(t)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(e.sessionCookie(t))
	body := do(t, e.handler, r).Body.String()

	for _, want := range []string{`id="volume-row"`, `id="volume-mute"`, `id="volume-slider"`, `id="volume-readout"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the dashboard does not contain %s", want)
		}
	}
	// Anchored to the slider: bare substring checks would pass on any element
	// that happened to carry these attributes.
	slider := regexp.MustCompile(`<input[^>]*id="volume-slider"[^>]*>`).FindString(body)
	if slider == "" {
		t.Fatal("the dashboard has no volume-slider input")
	}
	for _, attr := range []string{`min="0"`, `max="100"`, `type="range"`} {
		if !strings.Contains(slider, attr) {
			t.Errorf("the slider tag %q is missing %s", slider, attr)
		}
	}
	if !strings.Contains(slider, "disabled") {
		t.Error("the slider ships enabled; it must start disabled until the level has been read")
	}
}

func TestDashboardShipsVolumeUnknown(t *testing.T) {
	e := newTestEnv(t)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(e.sessionCookie(t))
	body := do(t, e.handler, r).Body.String()

	// The page is served before anything has read the device, so it must not
	// claim a level. The em dash is the honest placeholder.
	readout := regexp.MustCompile(`<span[^>]*id="volume-readout"[^>]*>([^<]*)</span>`).FindStringSubmatch(body)
	if readout == nil {
		t.Fatal("the dashboard has no volume-readout span")
	}
	if strings.Contains(readout[1], "%") {
		t.Errorf("readout renders %q, want no percentage before the level has been read", readout[1])
	}

	mute := regexp.MustCompile(`<button[^>]*id="volume-mute"[^>]*>`).FindString(body)
	if !strings.Contains(mute, "disabled") {
		t.Error("the mute button ships enabled; it must start disabled until the level has been read")
	}
}
