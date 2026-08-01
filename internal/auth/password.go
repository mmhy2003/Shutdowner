// Package auth handles password verification, signed sessions, CSRF tokens and
// login rate limiting.
package auth

import "golang.org/x/crypto/bcrypt"

// ProductionCost is the bcrypt cost for real password hashes. Roughly 100ms per
// verification, which caps brute-force throughput on its own.
const ProductionCost = 12

// HashPassword hashes plain at ProductionCost.
func HashPassword(plain string) (string, error) {
	return HashPasswordCost(plain, ProductionCost)
}

// HashPasswordCost hashes plain at an explicit cost. Exported so tests can use
// a cheap cost; production code should call HashPassword.
func HashPasswordCost(plain string, cost int) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), cost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// VerifyPassword reports whether plain matches hash. A malformed hash is a
// mismatch, never a panic.
func VerifyPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}
