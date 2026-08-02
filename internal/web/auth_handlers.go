package web

import (
	"fmt"
	"net/http"
	"strconv"

	"shutdowner/internal/auth"
)

type loginPage struct {
	Error string
}

func (s *Server) renderLogin(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.tmpl.ExecuteTemplate(w, "login.html", loginPage{Error: msg}); err != nil {
		s.logger.Error("rendering the login page", "error", err)
	}
}

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookieName); err == nil {
		if _, verr := s.sessions.Verify(c.Value); verr == nil {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		// Present but unusable: clear it rather than let the browser keep
		// re-sending a cookie that will never verify.
		s.clearSessionCookie(w)
	}
	s.renderLogin(w, http.StatusOK, "")
}

// handleLoginSubmit is exempt from CSRF: no session exists yet to derive a
// token from, and forging a login for a single-user app gains the attacker a
// session as themselves.
func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	ip := ClientIP(r)

	// The limiter gates before verification so that a locked-out client never
	// reaches the bcrypt comparison at all: at cost 12 that is ~100ms of CPU per
	// attempt, which is a denial-of-service lever if it can be driven freely.
	if ok, retry := s.limiter.Allow(ip); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		s.logger.Warn("login rate limited", "ip", ip)
		minutes := int(retry.Minutes()) + 1
		unit := "minutes"
		if minutes == 1 {
			unit = "minute"
		}
		s.renderLogin(w, http.StatusTooManyRequests,
			fmt.Sprintf("Too many failed attempts. Try again in %d %s.", minutes, unit))
		return
	}

	if err := r.ParseForm(); err != nil {
		s.renderLogin(w, http.StatusBadRequest, "Incorrect password.")
		return
	}

	if !auth.VerifyPassword(s.passwordHash, r.PostFormValue("password")) {
		s.limiter.RecordFailure(ip)
		s.logger.Warn("failed login", "ip", ip)
		s.renderLogin(w, http.StatusUnauthorized, "Incorrect password.")
		return
	}

	token, err := s.sessions.Issue()
	if err != nil {
		s.logger.Error("issuing a session", "error", err)
		s.renderLogin(w, http.StatusInternalServerError, "Could not start a session. Check the log.")
		return
	}
	s.limiter.Reset(ip)
	s.setSessionCookie(w, token)
	s.logger.Info("login succeeded", "ip", ip)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusFound)
}
