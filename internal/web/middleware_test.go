package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	e := newTestEnv(t)
	res := do(t, e.handler, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	want := map[string]string{
		"X-Frame-Options":        "DENY",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
	}
	for k, v := range want {
		if got := res.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}

	csp := res.Header().Get("Content-Security-Policy")
	// connect-src is what allows the status-polling fetch under default-src 'none'.
	for _, directive := range []string{"default-src 'none'", "script-src 'self'", "connect-src 'self'", "form-action 'self'"} {
		if !strings.Contains(csp, directive) {
			t.Errorf("CSP = %q, want it to contain %q", csp, directive)
		}
	}
}

func TestRecoverPanicReturns500(t *testing.T) {
	e := newTestEnv(t)
	h := recoverPanic(e.srv.logger, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	res := do(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	if res.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", res.Code)
	}
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		cfHeader   string
		remoteAddr string
		want       string
	}{
		{"cloudflare header wins", "203.0.113.7", "127.0.0.1:54321", "203.0.113.7"},
		{"falls back to remote addr", "", "203.0.113.9:54321", "203.0.113.9"},
		{"remote addr without a port", "", "203.0.113.9", "203.0.113.9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remoteAddr
			if tt.cfHeader != "" {
				r.Header.Set("CF-Connecting-IP", tt.cfHeader)
			}
			if got := ClientIP(r); got != tt.want {
				t.Errorf("ClientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRequireSessionRedirectsBrowsers(t *testing.T) {
	e := newTestEnv(t)
	h := e.srv.requireSession(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the wrapped handler ran without a session")
	})

	res := do(t, http.HandlerFunc(h), httptest.NewRequest(http.MethodGet, "/", nil))
	if res.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", res.Code)
	}
	if loc := res.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}

func TestRequireSessionAnswersAPIWith401(t *testing.T) {
	e := newTestEnv(t)
	h := e.srv.requireSession(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the wrapped handler ran without a session")
	})

	res := do(t, http.HandlerFunc(h), httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if res.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", res.Code)
	}
}

func TestRequireSessionPassesAValidSession(t *testing.T) {
	e := newTestEnv(t)
	called := false
	h := e.srv.requireSession(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if nonceFrom(r.Context()) == "" {
			t.Error("the session nonce was not placed in the request context")
		}
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(e.sessionCookie(t))
	do(t, http.HandlerFunc(h), r)

	if !called {
		t.Error("the wrapped handler did not run for a valid session")
	}
}

func TestRequireSessionRejectsATamperedCookie(t *testing.T) {
	e := newTestEnv(t)
	h := e.srv.requireSession(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the wrapped handler ran with a tampered cookie")
	})

	c := e.sessionCookie(t)
	c.Value = c.Value[:len(c.Value)-1] + "X"
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(c)

	if res := do(t, http.HandlerFunc(h), r); res.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", res.Code)
	}
}

func TestRequireCSRF(t *testing.T) {
	e := newTestEnv(t)
	cookie := e.sessionCookie(t)
	token := e.csrfToken(t, cookie)

	newRequest := func(setup func(*http.Request)) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/action", strings.NewReader("csrf_token="+token))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		if setup != nil {
			setup(r)
		}
		return r
	}

	tests := []struct {
		name  string
		setup func(*http.Request)
		want  int
	}{
		{"token in the header", func(r *http.Request) { r.Header.Set("X-CSRF-Token", token) }, http.StatusOK},
		{"token in the form field", nil, http.StatusOK},
		{"wrong header token", func(r *http.Request) { r.Header.Set("X-CSRF-Token", "nope") }, http.StatusForbidden},
		{"no token at all", func(r *http.Request) {
			// ContentLength must be cleared alongside the body, or FormValue
			// waits for bytes that never arrive.
			r.Body = http.NoBody
			r.ContentLength = 0
			r.Header.Del("Content-Type")
		}, http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := e.srv.requireSession(e.srv.requireCSRF(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			res := do(t, http.HandlerFunc(h), newRequest(tt.setup))
			if res.Code != tt.want {
				t.Errorf("status = %d, want %d", res.Code, tt.want)
			}
		})
	}
}
