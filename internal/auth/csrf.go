package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

// CSRFToken derives a per-session token from the session nonce. Because it is
// derived rather than stored, it needs no server state and survives restarts
// exactly as the session does.
func (m *SessionManager) CSRFToken(nonce string) string {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(nonce))
	mac.Write([]byte("csrf"))
	return hex.EncodeToString(mac.Sum(nil))
}

// ValidCSRF reports whether token is the CSRF token for nonce.
func (m *SessionManager) ValidCSRF(nonce, token string) bool {
	want := m.CSRFToken(nonce)
	return subtle.ConstantTimeCompare([]byte(want), []byte(token)) == 1
}
