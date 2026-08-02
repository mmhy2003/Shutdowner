package web

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

// cspPolicy forbids inline script, so client JS is a served file rather than a
// <script> block. connect-src is what permits the status-polling fetch under
// default-src 'none'.
const cspPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; " +
	"img-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'"

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", cspPolicy)
		next.ServeHTTP(w, r)
	})
}

func recoverPanic(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				logger.Error("panic serving request",
					"path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ClientIP prefers Cloudflare's header. It is trustworthy only because the
// listener is loopback-only, which makes cloudflared the sole possible source
// of a request.
func ClientIP(r *http.Request) string {
	if v := r.Header.Get("CF-Connecting-IP"); v != "" {
		return v
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type ctxKey int

const nonceKey ctxKey = iota

func nonceFrom(ctx context.Context) string {
	v, _ := ctx.Value(nonceKey).(string)
	return v
}

// requireSession redirects browsers to the login page and answers API paths
// with 401, so a fetch never has to distinguish a login page from JSON.
func (s *Server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(SessionCookieName); err == nil {
			if nonce, verr := s.sessions.Verify(c.Value); verr == nil {
				next(w, r.WithContext(context.WithValue(r.Context(), nonceKey, nonce)))
				return
			}
		}
		s.clearSessionCookie(w)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSONError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		http.Redirect(w, r, "/login", http.StatusFound)
	}
}

// requireCSRF accepts the token from a header or a form field so both the JSON
// endpoints and the script-free logout form work. It must be nested inside
// requireSession, which is what puts the nonce in the context.
func (s *Server) requireCSRF(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-CSRF-Token")
		if token == "" {
			token = r.FormValue("csrf_token")
		}
		if !s.sessions.ValidCSRF(nonceFrom(r.Context()), token) {
			writeJSONError(w, http.StatusForbidden, "invalid CSRF token")
			return
		}
		next(w, r)
	}
}

// Secure is set unconditionally. Browsers treat http://localhost as a secure
// context, so this does not break local development behind the tunnel or
// without it.
func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(s.sessions.TTL() / time.Second),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}
