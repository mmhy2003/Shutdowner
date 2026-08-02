package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
