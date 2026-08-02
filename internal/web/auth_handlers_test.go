package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"shutdowner/internal/auth"
)

func postForm(t *testing.T, h http.Handler, path string, form url.Values, setup func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if setup != nil {
		setup(r)
	}
	return do(t, h, r)
}

func findCookie(res *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range res.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestLoginFormRenders(t *testing.T) {
	e := newTestEnv(t)
	res := do(t, e.handler, httptest.NewRequest(http.MethodGet, "/login", nil))

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	if !strings.Contains(res.Body.String(), `name="password"`) {
		t.Error("the login page has no password field")
	}
}

func TestLoginSucceeds(t *testing.T) {
	e := newTestEnv(t)
	res := postForm(t, e.handler, "/login", url.Values{"password": {testPassword}}, nil)

	if res.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", res.Code)
	}
	if loc := res.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want /", loc)
	}

	c := findCookie(res, SessionCookieName)
	if c == nil {
		t.Fatal("no session cookie was set")
	}
	if !c.HttpOnly {
		t.Error("the session cookie is not HttpOnly")
	}
	if !c.Secure {
		t.Error("the session cookie is not Secure")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", c.SameSite)
	}
	if c.Path != "/" {
		t.Errorf("Path = %q, want /", c.Path)
	}
	// The TTL in newTestEnv is one hour; MaxAge is the fifth required attribute.
	if c.MaxAge != 3600 {
		t.Errorf("MaxAge = %d, want 3600", c.MaxAge)
	}
	if _, err := e.srv.sessions.Verify(c.Value); err != nil {
		t.Errorf("the issued cookie does not verify: %v", err)
	}
}

func TestLoginFailsWithTheWrongPassword(t *testing.T) {
	e := newTestEnv(t)
	res := postForm(t, e.handler, "/login", url.Values{"password": {"wrong"}}, nil)

	if res.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", res.Code)
	}
	if findCookie(res, SessionCookieName) != nil {
		t.Error("a session cookie was set for a failed login")
	}
	if !strings.Contains(res.Body.String(), "Incorrect password") {
		t.Error("the failed login page does not say the password was wrong")
	}
}

func TestSixthFailedLoginIsRateLimited(t *testing.T) {
	e := newTestEnv(t)
	setIP := func(r *http.Request) { r.Header.Set("CF-Connecting-IP", "203.0.113.5") }

	for i := 1; i <= 5; i++ {
		res := postForm(t, e.handler, "/login", url.Values{"password": {"wrong"}}, setIP)
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i, res.Code)
		}
	}

	res := postForm(t, e.handler, "/login", url.Values{"password": {"wrong"}}, setIP)
	if res.Code != http.StatusTooManyRequests {
		t.Errorf("sixth attempt status = %d, want 429", res.Code)
	}
	if res.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After header on the rate-limited response")
	}
}

func TestRateLimitBlocksEvenTheCorrectPassword(t *testing.T) {
	e := newTestEnv(t)
	setIP := func(r *http.Request) { r.Header.Set("CF-Connecting-IP", "203.0.113.6") }
	for i := 0; i < 5; i++ {
		postForm(t, e.handler, "/login", url.Values{"password": {"wrong"}}, setIP)
	}

	res := postForm(t, e.handler, "/login", url.Values{"password": {testPassword}}, setIP)
	if res.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429 — the limiter must gate before verification", res.Code)
	}
}

func TestSuccessfulLoginClearsTheFailureCount(t *testing.T) {
	e := newTestEnv(t)
	setIP := func(r *http.Request) { r.Header.Set("CF-Connecting-IP", "203.0.113.7") }
	for i := 0; i < 4; i++ {
		postForm(t, e.handler, "/login", url.Values{"password": {"wrong"}}, setIP)
	}
	postForm(t, e.handler, "/login", url.Values{"password": {testPassword}}, setIP)

	for i := 0; i < 5; i++ {
		res := postForm(t, e.handler, "/login", url.Values{"password": {"wrong"}}, setIP)
		if res.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d was rate limited, so the counter was not reset on success", i+1)
		}
	}
}

func TestLoginPageRedirectsWhenAlreadySignedIn(t *testing.T) {
	e := newTestEnv(t)
	r := httptest.NewRequest(http.MethodGet, "/login", nil)
	r.AddCookie(e.sessionCookie(t))

	res := do(t, e.handler, r)
	if res.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", res.Code)
	}
}

func TestLogoutClearsTheCookie(t *testing.T) {
	e := newTestEnv(t)
	cookie := e.sessionCookie(t)
	token := e.csrfToken(t, cookie)

	res := postForm(t, e.handler, "/logout", url.Values{"csrf_token": {token}}, func(r *http.Request) {
		r.AddCookie(cookie)
	})

	if res.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", res.Code)
	}
	c := findCookie(res, SessionCookieName)
	if c == nil || c.MaxAge >= 0 {
		t.Error("logout did not expire the session cookie")
	}
}

func TestLogoutRequiresCSRF(t *testing.T) {
	e := newTestEnv(t)
	res := postForm(t, e.handler, "/logout", url.Values{}, func(r *http.Request) {
		r.AddCookie(e.sessionCookie(t))
	})
	if res.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", res.Code)
	}
}

func TestLoginIsExemptFromCSRF(t *testing.T) {
	e := newTestEnv(t)
	// No CSRF token anywhere: there is no session yet to derive one from.
	res := postForm(t, e.handler, "/login", url.Values{"password": {testPassword}}, nil)
	if res.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", res.Code)
	}
}

func TestRateLimitMessageIsSingularForOneMinute(t *testing.T) {
	e := newTestEnv(t)
	// A window under a minute makes the remaining time round to 1, which is the
	// only case where the plural is wrong.
	e.srv.limiter = auth.NewLimiter(1, auth.DefaultGlobalLimit, 30*time.Second)
	setIP := func(r *http.Request) { r.Header.Set("CF-Connecting-IP", "203.0.113.8") }

	postForm(t, e.handler, "/login", url.Values{"password": {"wrong"}}, setIP)
	res := postForm(t, e.handler, "/login", url.Values{"password": {"wrong"}}, setIP)

	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", res.Code)
	}
	if body := res.Body.String(); !strings.Contains(body, "in 1 minute.") || strings.Contains(body, "in 1 minutes.") {
		t.Error(`the rate-limit message says "1 minutes"`)
	}
}

func TestOversizedLoginBodyIsRejected(t *testing.T) {
	e := newTestEnv(t)

	form := url.Values{"password": {testPassword + strings.Repeat("x", 16<<10)}}
	res := postForm(t, e.handler, "/login", form, nil)

	// http.MaxBytesReader makes ParseForm fail, which the handler reports as a
	// failed login rather than buffering megabytes first.
	if res.Code == http.StatusFound {
		t.Error("an oversized login body was accepted")
	}
	if findCookie(res, SessionCookieName) != nil {
		t.Error("an oversized login body issued a session cookie")
	}
	// This is the assertion that distinguishes a capped body from an uncapped
	// one. Uncapped, ParseForm happily buffers 16 KiB (its own limit is 10 MB),
	// the password comparison is reached, and the answer is 401. Capped,
	// ParseForm fails first and the handler answers 400 — so a 401 here means
	// the cap is gone.
	if res.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 — the body cap must fail ParseForm before the password is compared", res.Code)
	}
}

func TestLoginFormClearsAnUnusableCookie(t *testing.T) {
	e := newTestEnv(t)

	c := e.sessionCookie(t)
	c.Value = c.Value[:len(c.Value)-1] + "X"
	r := httptest.NewRequest(http.MethodGet, "/login", nil)
	r.AddCookie(c)

	res := do(t, e.handler, r)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	cleared := findCookie(res, SessionCookieName)
	if cleared == nil || cleared.MaxAge >= 0 {
		t.Error("a cookie that fails verification was not cleared")
	}
}

func TestLoginFormSetsNoCookieWhenNoneWasSent(t *testing.T) {
	e := newTestEnv(t)
	res := do(t, e.handler, httptest.NewRequest(http.MethodGet, "/login", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	if findCookie(res, SessionCookieName) != nil {
		t.Error("a Set-Cookie was sent to a visitor who had no cookie")
	}
}
