package auth

import (
	"testing"
	"time"
)

func TestCSRFTokenValidatesAgainstItsOwnNonce(t *testing.T) {
	m := NewSessionManager(testSecret(), time.Hour)
	token, _ := m.Issue()
	nonce, _ := m.Verify(token)

	csrf := m.CSRFToken(nonce)
	if csrf == "" {
		t.Fatal("CSRFToken() returned an empty string")
	}
	if !m.ValidCSRF(nonce, csrf) {
		t.Error("ValidCSRF() = false for a token derived from the same nonce")
	}
}

func TestCSRFTokenIsBoundToTheNonce(t *testing.T) {
	m := NewSessionManager(testSecret(), time.Hour)
	csrf := m.CSRFToken("nonce-a")
	if m.ValidCSRF("nonce-b", csrf) {
		t.Error("ValidCSRF() = true for a token minted under a different nonce")
	}
}

func TestCSRFRejectsBadTokens(t *testing.T) {
	m := NewSessionManager(testSecret(), time.Hour)
	for _, token := range []string{"", "deadbeef", "0"} {
		if m.ValidCSRF("nonce-a", token) {
			t.Errorf("ValidCSRF(%q) = true, want false", token)
		}
	}
}

func TestCSRFIsBoundToTheSecret(t *testing.T) {
	a := NewSessionManager(testSecret(), time.Hour)
	b := NewSessionManager([]byte("ffffffffffffffffffffffffffffffff"), time.Hour)
	if b.ValidCSRF("nonce-a", a.CSRFToken("nonce-a")) {
		t.Error("ValidCSRF() = true across managers with different secrets")
	}
}
