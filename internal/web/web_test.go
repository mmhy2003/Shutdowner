package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"shutdowner/internal/action"
	"shutdowner/internal/auth"
	"shutdowner/internal/power"
)

const testPassword = "test-password"

type testEnv struct {
	srv     *Server
	fake    *power.Fake
	actions *action.Manager
	limiter *auth.Limiter
	handler http.Handler
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	hash, err := auth.HashPasswordCost(testPassword, 4)
	if err != nil {
		t.Fatalf("HashPasswordCost() error = %v", err)
	}

	fake := power.NewFake()
	actions := action.New(fake)
	limiter := auth.NewLimiter(auth.DefaultPerIPLimit, auth.DefaultGlobalLimit, auth.DefaultWindow)

	srv, err := New(Options{
		Sessions:     auth.NewSessionManager([]byte("0123456789abcdef0123456789abcdef"), time.Hour),
		Limiter:      limiter,
		Actions:      actions,
		Power:        fake,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		PasswordHash: hash,
		// A zero delay is not used here: tests need a countdown long enough to
		// observe the pending state before it fires.
		DelaySeconds: 45,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return &testEnv{srv: srv, fake: fake, actions: actions, limiter: limiter, handler: srv.Routes()}
}

// sessionCookie logs in through the real handler and returns the resulting
// cookie. Available from Task 12 onward; Task 11 tests do not call it.
func (e *testEnv) sessionCookie(t *testing.T) *http.Cookie {
	t.Helper()
	token, err := e.srv.sessions.Issue()
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	return &http.Cookie{Name: SessionCookieName, Value: token}
}

// csrfToken returns the CSRF token matching a session cookie.
func (e *testEnv) csrfToken(t *testing.T, c *http.Cookie) string {
	t.Helper()
	nonce, err := e.srv.sessions.Verify(c.Value)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	return e.srv.sessions.CSRFToken(nonce)
}

// tamperCookieValue returns v with one character changed, guaranteed to fail
// verification.
//
// It mutates the first character of the payload rather than the last character
// of the signature, which is the obvious thing to reach for and is wrong: a
// 32-byte HMAC encodes to 43 base64 characters whose final one carries only
// four significant bits, and Go's decoder ignores the non-canonical padding
// bits. Replacing that character therefore decodes to the identical signature
// about 6% of the time, and the supposedly tampered cookie verifies fine.
func tamperCookieValue(v string) string {
	b := []byte(v)
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	return string(b)
}

func do(t *testing.T, h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHealthzNeedsNoSession(t *testing.T) {
	e := newTestEnv(t)
	res := do(t, e.handler, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if res.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", res.Code)
	}
}
