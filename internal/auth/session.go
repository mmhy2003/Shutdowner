package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var (
	ErrMalformedToken = errors.New("auth: malformed session token")
	ErrBadSignature   = errors.New("auth: session signature does not verify")
	ErrExpired        = errors.New("auth: session expired")
)

// sessionPayload is the signed body of a session cookie. It carries no user
// identity because there is exactly one user.
type sessionPayload struct {
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
	Nonce     string `json:"n"`
}

// SessionManager issues and verifies stateless signed session cookies. Nothing
// is stored server-side, so sessions survive the reboots this app exists to
// perform. Rotating the secret invalidates every session.
type SessionManager struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

func NewSessionManager(secret []byte, ttl time.Duration) *SessionManager {
	return &SessionManager{secret: secret, ttl: ttl, now: time.Now}
}

// SetClock replaces the time source. Intended for tests.
func (m *SessionManager) SetClock(now func() time.Time) { m.now = now }

func (m *SessionManager) TTL() time.Duration { return m.ttl }

// Issue mints a signed token valid for the configured TTL.
func (m *SessionManager) Issue() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	now := m.now()
	body, err := json.Marshal(sessionPayload{
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Add(m.ttl).Unix(),
		Nonce:     hex.EncodeToString(raw),
	})
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(body)
	sig := base64.RawURLEncoding.EncodeToString(m.sign([]byte(encoded)))
	return encoded + "." + sig, nil
}

// Verify returns the session nonce when the token is well-formed, correctly
// signed and unexpired. The signature is checked before the payload is parsed.
func (m *SessionManager) Verify(token string) (string, error) {
	encoded, sig, ok := strings.Cut(token, ".")
	if !ok || encoded == "" || sig == "" {
		return "", ErrMalformedToken
	}
	gotSig, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return "", ErrMalformedToken
	}
	if !hmac.Equal(gotSig, m.sign([]byte(encoded))) {
		return "", ErrBadSignature
	}
	body, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", ErrMalformedToken
	}
	var p sessionPayload
	if err := json.Unmarshal(body, &p); err != nil || p.Nonce == "" {
		return "", ErrMalformedToken
	}
	if m.now().Unix() >= p.ExpiresAt {
		return "", ErrExpired
	}
	return p.Nonce, nil
}

func (m *SessionManager) sign(b []byte) []byte {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write(b)
	return mac.Sum(nil)
}
