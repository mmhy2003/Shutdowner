package auth

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func testSecret() []byte { return []byte("0123456789abcdef0123456789abcdef") }

func TestSessionRoundTrip(t *testing.T) {
	m := NewSessionManager(testSecret(), time.Hour)
	token, err := m.Issue()
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	nonce, err := m.Verify(token)
	if err != nil {
		t.Fatalf("Verify() error = %v, want nil", err)
	}
	if nonce == "" {
		t.Error("Verify() returned an empty nonce")
	}
}

func TestEachSessionHasADistinctNonce(t *testing.T) {
	m := NewSessionManager(testSecret(), time.Hour)
	t1, _ := m.Issue()
	t2, _ := m.Issue()
	n1, _ := m.Verify(t1)
	n2, _ := m.Verify(t2)
	if n1 == n2 {
		t.Error("two sessions share a nonce, so the nonce is not random")
	}
}

func TestVerifyRejectsTamperedPayload(t *testing.T) {
	m := NewSessionManager(testSecret(), time.Hour)
	token, _ := m.Issue()
	body, sig, _ := strings.Cut(token, ".")
	tampered := body[:len(body)-1] + "X" + "." + sig

	if _, err := m.Verify(tampered); !errors.Is(err, ErrBadSignature) {
		t.Errorf("Verify(tampered) error = %v, want ErrBadSignature", err)
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	issuer := NewSessionManager(testSecret(), time.Hour)
	token, _ := issuer.Issue()

	other := NewSessionManager([]byte("ffffffffffffffffffffffffffffffff"), time.Hour)
	if _, err := other.Verify(token); !errors.Is(err, ErrBadSignature) {
		t.Errorf("Verify() with a different secret error = %v, want ErrBadSignature", err)
	}
}

func TestVerifyRejectsExpiredSession(t *testing.T) {
	m := NewSessionManager(testSecret(), time.Hour)
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	m.SetClock(func() time.Time { return now })

	token, _ := m.Issue()
	if _, err := m.Verify(token); err != nil {
		t.Fatalf("Verify() immediately after Issue error = %v, want nil", err)
	}

	now = now.Add(time.Hour + time.Second)
	if _, err := m.Verify(token); !errors.Is(err, ErrExpired) {
		t.Errorf("Verify() after expiry error = %v, want ErrExpired", err)
	}
}

func TestVerifyRejectsMalformedTokens(t *testing.T) {
	m := NewSessionManager(testSecret(), time.Hour)
	for _, token := range []string{"", "nodot", ".", "abc.", ".abc", "!!!.???", "YWJj.!!!"} {
		if _, err := m.Verify(token); !errors.Is(err, ErrMalformedToken) && !errors.Is(err, ErrBadSignature) {
			t.Errorf("Verify(%q) error = %v, want ErrMalformedToken or ErrBadSignature", token, err)
		}
	}
}

func TestTTLIsReported(t *testing.T) {
	m := NewSessionManager(testSecret(), 42*time.Minute)
	if m.TTL() != 42*time.Minute {
		t.Errorf("TTL() = %v, want 42m", m.TTL())
	}
}
