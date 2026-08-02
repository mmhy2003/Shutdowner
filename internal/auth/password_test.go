package auth

import (
	"strings"
	"testing"
)

// testCost keeps the suite fast. bcrypt at ProductionCost takes ~100ms a call
// by design, which is the point of it in production and intolerable in tests.
const testCost = 4

func TestVerifyPasswordAcceptsTheRightPassword(t *testing.T) {
	hash, err := HashPasswordCost("correct horse battery staple", testCost)
	if err != nil {
		t.Fatalf("HashPasswordCost() error = %v", err)
	}
	if !VerifyPassword(hash, "correct horse battery staple") {
		t.Error("VerifyPassword() = false for the correct password, want true")
	}
}

func TestVerifyPasswordRejectsWrongInput(t *testing.T) {
	hash, err := HashPasswordCost("correct horse battery staple", testCost)
	if err != nil {
		t.Fatalf("HashPasswordCost() error = %v", err)
	}
	tests := []struct {
		name  string
		hash  string
		plain string
	}{
		{"wrong password", hash, "incorrect horse battery staple"},
		{"empty password", hash, ""},
		{"case differs", hash, "Correct Horse Battery Staple"},
		{"garbage hash", "not-a-hash", "correct horse battery staple"},
		{"empty hash", "", "correct horse battery staple"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if VerifyPassword(tt.hash, tt.plain) {
				t.Error("VerifyPassword() = true, want false")
			}
		})
	}
}

func TestHashPasswordUsesProductionCost(t *testing.T) {
	hash, err := HashPassword("a-real-password")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if !strings.HasPrefix(hash, "$2a$12$") {
		t.Errorf("HashPassword() = %q, want it to start with $2a$12$", hash)
	}
	if len(hash) != 60 {
		t.Errorf("len(hash) = %d, want 60", len(hash))
	}
	if !VerifyPassword(hash, "a-real-password") {
		t.Error("VerifyPassword() = false for a freshly hashed password")
	}
}

func TestHashesAreSalted(t *testing.T) {
	a, _ := HashPasswordCost("same", testCost)
	b, _ := HashPasswordCost("same", testCost)
	if a == b {
		t.Error("two hashes of the same password are identical, so the salt is not random")
	}
}
