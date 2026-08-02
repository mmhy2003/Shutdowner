# Shutdowner Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A single Go binary that runs as a Windows service and serves a password-protected web UI for remotely shutting down, restarting, sleeping, and hibernating that PC.

**Architecture:** One process on the target PC. `internal/power` wraps the OS calls behind a `Controller` interface, `internal/action` owns an abortable countdown state machine, `internal/web` serves an embedded server-rendered UI with signed-cookie sessions. Cloudflare Tunnel fronts it; the app binds loopback only. The `Controller` seam plus a `Fake` implementation is what lets every layer above the syscalls be developed and tested on Linux.

**Tech Stack:** Go 1.25+, `html/template` + `embed.FS`, vanilla JS, `golang.org/x/crypto` (bcrypt), `golang.org/x/sys` (Win32 + service framework), `github.com/joho/godotenv`.

**Spec:** `docs/superpowers/specs/2026-08-01-remote-windows-power-control-design.md`

## Global Constraints

- **Module path:** `shutdowner`. All internal imports are `shutdowner/internal/...`.
- **Go version:** go.mod declares `go 1.25.0`. Development toolchain is go1.26.5. The floor is 1.25.0 rather than something more conservative because `golang.org/x/crypto` and `golang.org/x/sys` both declare `go 1.25.0` themselves, and Go requires the main module's floor to be at least as high as any dependency's. This only bites once a dependency is actually imported, so Task 1 builds at a lower floor and Task 2 does not.
- **Exactly three external dependencies:** `golang.org/x/crypto`, `golang.org/x/sys`, `github.com/joho/godotenv`. Do not add a fourth — log rotation and no-echo password entry are hand-rolled specifically to avoid one.
- **Never resolve paths from the working directory.** A LocalSystem service starts in `C:\Windows\System32`. `.env` and the log file resolve from `os.Executable()`.
- **Loopback-only bind** unless `--allow-public-bind` is passed. Startup fails otherwise.
- **bcrypt cost 12** in production. Tests use cost 4 — cost 12 takes ~100ms per call by design and would make the suite crawl.
- **Every task ends green:** `go test ./...` passes AND `GOOS=windows GOARCH=amd64 go build ./...` succeeds. The Windows build check is mandatory because most of the platform code cannot run here.
- **Commit after every task**, message in the form `feat: <what>` or `test: <what>`.
- **Run `gofmt -w .`** before every commit.

---

### Task 1: Module scaffolding and configuration

**Files:**
- Create: `go.mod`, `.gitignore`, `Makefile`, `.env.example`
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `config.Config{PasswordHash string, SessionSecret []byte, Listen string, Delay time.Duration, SessionTTL time.Duration, LogFile string}`; `config.Parse(env map[string]string, allowPublicBind bool) (*Config, error)`; `config.Load(path string, allowPublicBind bool) (*Config, error)`; `config.ExeDir() (string, error)`.

- [ ] **Step 1: Install the Go toolchain**

```bash
curl -fsSL https://go.dev/dl/go1.26.5.linux-amd64.tar.gz -o /tmp/go.tgz
rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tgz
export PATH=$PATH:/usr/local/go/bin
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
go version
```

Expected: `go version go1.26.5 linux/amd64`

- [ ] **Step 2: Initialise the module and pull dependencies**

```bash
cd /opt/shutdowner
go mod init shutdowner
go get github.com/joho/godotenv@v1.5.1
go get golang.org/x/crypto@latest
go get golang.org/x/sys@latest
```

Expected: `go.mod` and `go.sum` created, three `require` lines present.

- [ ] **Step 3: Write the failing test**

Create `internal/config/config_test.go`:

```go
package config

import (
	"strings"
	"testing"
	"time"
)

// A syntactically valid bcrypt hash. Its plaintext is irrelevant here: Parse
// only checks shape.
const testHash = "$2a$12$" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func validEnv() map[string]string {
	return map[string]string{
		"SHUTDOWNER_PASSWORD_HASH":  testHash,
		"SHUTDOWNER_SESSION_SECRET": strings.Repeat("ab", 32),
	}
}

func TestParseAppliesDefaults(t *testing.T) {
	cfg, err := Parse(validEnv(), false)
	if err != nil {
		t.Fatalf("Parse() error = %v, want nil", err)
	}
	if cfg.Listen != DefaultListen {
		t.Errorf("Listen = %q, want %q", cfg.Listen, DefaultListen)
	}
	if cfg.Delay != 45*time.Second {
		t.Errorf("Delay = %v, want 45s", cfg.Delay)
	}
	if cfg.SessionTTL != DefaultSessionTTL {
		t.Errorf("SessionTTL = %v, want %v", cfg.SessionTTL, DefaultSessionTTL)
	}
	if len(cfg.SessionSecret) != 32 {
		t.Errorf("SessionSecret length = %d, want 32", len(cfg.SessionSecret))
	}
	if cfg.LogFile != "" {
		t.Errorf("LogFile = %q, want empty (resolved by Load)", cfg.LogFile)
	}
}

func TestParseHonoursExplicitValues(t *testing.T) {
	env := validEnv()
	env["SHUTDOWNER_LISTEN"] = "127.0.0.1:9999"
	env["SHUTDOWNER_DELAY_SECONDS"] = "0"
	env["SHUTDOWNER_SESSION_TTL"] = "30m"
	env["SHUTDOWNER_LOG_FILE"] = "/var/log/s.log"

	cfg, err := Parse(env, false)
	if err != nil {
		t.Fatalf("Parse() error = %v, want nil", err)
	}
	if cfg.Listen != "127.0.0.1:9999" {
		t.Errorf("Listen = %q", cfg.Listen)
	}
	if cfg.Delay != 0 {
		t.Errorf("Delay = %v, want 0", cfg.Delay)
	}
	if cfg.SessionTTL != 30*time.Minute {
		t.Errorf("SessionTTL = %v, want 30m", cfg.SessionTTL)
	}
	if cfg.LogFile != "/var/log/s.log" {
		t.Errorf("LogFile = %q", cfg.LogFile)
	}
}

func TestParseRejectsBadInput(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(map[string]string)
		wantSub string
	}{
		{"missing hash", func(e map[string]string) { delete(e, "SHUTDOWNER_PASSWORD_HASH") }, "SHUTDOWNER_PASSWORD_HASH is required"},
		{"malformed hash", func(e map[string]string) { e["SHUTDOWNER_PASSWORD_HASH"] = "hunter2" }, "not a bcrypt hash"},
		{"missing secret", func(e map[string]string) { delete(e, "SHUTDOWNER_SESSION_SECRET") }, "SHUTDOWNER_SESSION_SECRET is required"},
		{"non-hex secret", func(e map[string]string) { e["SHUTDOWNER_SESSION_SECRET"] = "zzzz" }, "must be hex"},
		{"short secret", func(e map[string]string) { e["SHUTDOWNER_SESSION_SECRET"] = strings.Repeat("ab", 8) }, "at least 32 bytes"},
		{"public bind", func(e map[string]string) { e["SHUTDOWNER_LISTEN"] = "0.0.0.0:8080" }, "not a loopback address"},
		{"listen without port", func(e map[string]string) { e["SHUTDOWNER_LISTEN"] = "127.0.0.1" }, "must be host:port"},
		{"delay not a number", func(e map[string]string) { e["SHUTDOWNER_DELAY_SECONDS"] = "soon" }, "must be an integer"},
		{"delay too large", func(e map[string]string) { e["SHUTDOWNER_DELAY_SECONDS"] = "3601" }, "between 0 and 3600"},
		{"delay negative", func(e map[string]string) { e["SHUTDOWNER_DELAY_SECONDS"] = "-1" }, "between 0 and 3600"},
		{"ttl unparseable", func(e map[string]string) { e["SHUTDOWNER_SESSION_TTL"] = "forever" }, "must be a duration"},
		{"ttl not positive", func(e map[string]string) { e["SHUTDOWNER_SESSION_TTL"] = "0s" }, "must be positive"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			tt.mutate(env)
			_, err := Parse(env, false)
			if err == nil {
				t.Fatalf("Parse() error = nil, want error containing %q", tt.wantSub)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("Parse() error = %q, want it to contain %q", err, tt.wantSub)
			}
		})
	}
}

func TestParseAcceptsLoopbackForms(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080", "127.0.0.5:1"} {
		env := validEnv()
		env["SHUTDOWNER_LISTEN"] = addr
		if _, err := Parse(env, false); err != nil {
			t.Errorf("Parse(%q) error = %v, want nil", addr, err)
		}
	}
}

func TestParseAllowsPublicBindWhenOptedIn(t *testing.T) {
	env := validEnv()
	env["SHUTDOWNER_LISTEN"] = "0.0.0.0:8080"
	if _, err := Parse(env, true); err != nil {
		t.Errorf("Parse() with allowPublicBind error = %v, want nil", err)
	}
}
```

- [ ] **Step 4: Run the test to verify it fails**

Run: `go test ./internal/config/ -v`
Expected: FAIL — `undefined: Parse`, `undefined: DefaultListen`, `undefined: DefaultSessionTTL`.

- [ ] **Step 5: Write the implementation**

Create `internal/config/config.go`:

```go
// Package config loads and validates Shutdowner's .env configuration.
package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// Defaults applied when a key is absent.
const (
	DefaultListen       = "127.0.0.1:8080"
	DefaultDelaySeconds = 45
	DefaultSessionTTL   = 168 * time.Hour
	DefaultLogFileName  = "shutdowner.log"
)

// bcryptShape matches the modern bcrypt formats: a two-digit cost and a
// 53-character salt+digest in bcrypt's own base64 alphabet.
var bcryptShape = regexp.MustCompile(`^\$2[aby]\$\d{2}\$[./A-Za-z0-9]{53}$`)

type Config struct {
	PasswordHash  string
	SessionSecret []byte
	Listen        string
	Delay         time.Duration
	SessionTTL    time.Duration
	LogFile       string
}

// Parse validates an already-loaded environment. It is separate from Load so
// the validation rules can be tested without touching the filesystem.
func Parse(env map[string]string, allowPublicBind bool) (*Config, error) {
	c := &Config{}

	c.PasswordHash = env["SHUTDOWNER_PASSWORD_HASH"]
	if c.PasswordHash == "" {
		return nil, errors.New("SHUTDOWNER_PASSWORD_HASH is required; generate one with --hash-password")
	}
	if !bcryptShape.MatchString(c.PasswordHash) {
		return nil, errors.New("SHUTDOWNER_PASSWORD_HASH is not a bcrypt hash; generate one with --hash-password")
	}

	rawSecret := env["SHUTDOWNER_SESSION_SECRET"]
	if rawSecret == "" {
		return nil, errors.New("SHUTDOWNER_SESSION_SECRET is required; generate one with --init")
	}
	secret, err := hex.DecodeString(rawSecret)
	if err != nil {
		return nil, fmt.Errorf("SHUTDOWNER_SESSION_SECRET must be hex: %w", err)
	}
	if len(secret) < 32 {
		return nil, fmt.Errorf("SHUTDOWNER_SESSION_SECRET must decode to at least 32 bytes, got %d", len(secret))
	}
	c.SessionSecret = secret

	c.Listen = env["SHUTDOWNER_LISTEN"]
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	if err := validateListen(c.Listen, allowPublicBind); err != nil {
		return nil, err
	}

	c.Delay = DefaultDelaySeconds * time.Second
	if raw := env["SHUTDOWNER_DELAY_SECONDS"]; raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("SHUTDOWNER_DELAY_SECONDS must be an integer: %w", err)
		}
		if n < 0 || n > 3600 {
			return nil, fmt.Errorf("SHUTDOWNER_DELAY_SECONDS must be between 0 and 3600, got %d", n)
		}
		c.Delay = time.Duration(n) * time.Second
	}

	c.SessionTTL = DefaultSessionTTL
	if raw := env["SHUTDOWNER_SESSION_TTL"]; raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("SHUTDOWNER_SESSION_TTL must be a duration such as 168h: %w", err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("SHUTDOWNER_SESSION_TTL must be positive, got %s", d)
		}
		c.SessionTTL = d
	}

	c.LogFile = env["SHUTDOWNER_LOG_FILE"]
	return c, nil
}

func validateListen(addr string, allowPublicBind bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("SHUTDOWNER_LISTEN must be host:port: %w", err)
	}
	if allowPublicBind || host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("SHUTDOWNER_LISTEN host %q is not a loopback address; "+
			"pass --allow-public-bind only if exposing the app directly is intended", host)
	}
	return nil
}

// ExeDir returns the directory holding the running executable. A Windows
// service starts in C:\Windows\System32, so nothing may be resolved relative to
// the working directory.
func ExeDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe), nil
}

// Load reads a .env file and parses it. An empty path means "beside the
// executable". An unset SHUTDOWNER_LOG_FILE also resolves beside the executable.
func Load(path string, allowPublicBind bool) (*Config, error) {
	exeDir, err := ExeDir()
	if err != nil {
		return nil, fmt.Errorf("locating executable: %w", err)
	}
	if path == "" {
		path = filepath.Join(exeDir, ".env")
	}
	env, err := godotenv.Read(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	cfg, err := Parse(env, allowPublicBind)
	if err != nil {
		return nil, err
	}
	if cfg.LogFile == "" {
		cfg.LogFile = filepath.Join(exeDir, DefaultLogFileName)
	}
	return cfg, nil
}
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./internal/config/ -v`
Expected: PASS — all five test functions, including every subtest of `TestParseRejectsBadInput`.

- [ ] **Step 7: Create the scaffolding files**

Create `.gitignore`:

```
.env
.env.dev
dist/
*.log
*.log.[0-9]
```

Create `Makefile` (note: recipe lines must be TAB-indented):

```make
GO ?= go
BIN := dist/shutdowner.exe

.PHONY: test vet build-windows run-dev fmt clean

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...
	GOOS=windows GOARCH=amd64 $(GO) vet ./...

fmt:
	$(GO) fmt ./...

build-windows:
	mkdir -p dist
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) build -ldflags "-s -w" -o $(BIN) ./cmd/shutdowner

run-dev:
	$(GO) run ./cmd/shutdowner --console --fake-power --config ./.env.dev

clean:
	rm -rf dist
```

Create `.env.example`:

```
# Generate with: shutdowner.exe --hash-password
SHUTDOWNER_PASSWORD_HASH=

# Generate with: shutdowner.exe --init
SHUTDOWNER_SESSION_SECRET=

# Must be a loopback address unless --allow-public-bind is passed.
SHUTDOWNER_LISTEN=127.0.0.1:8080

# Seconds between confirming an action and executing it. 0-3600.
SHUTDOWNER_DELAY_SECONDS=45

# How long a login lasts.
SHUTDOWNER_SESSION_TTL=168h

# Defaults to shutdowner.log beside the executable.
SHUTDOWNER_LOG_FILE=
```

- [ ] **Step 8: Verify the whole tree builds for Windows**

```bash
gofmt -w .
go test ./...
GOOS=windows GOARCH=amd64 go build ./...
```

Expected: tests pass; both builds silent.

- [ ] **Step 9: Commit**

```bash
git add go.mod go.sum .gitignore Makefile .env.example internal/config/
git commit -m "feat: module scaffolding and .env configuration"
```

---

### Task 2: Password hashing and verification

**Files:**
- Create: `internal/auth/password.go`
- Test: `internal/auth/password_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `auth.ProductionCost = 12`; `auth.HashPassword(plain string) (string, error)`; `auth.HashPasswordCost(plain string, cost int) (string, error)`; `auth.VerifyPassword(hash, plain string) bool`.

- [ ] **Step 1: Write the failing test**

Create `internal/auth/password_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/auth/ -v`
Expected: FAIL — `undefined: HashPasswordCost`, `undefined: HashPassword`, `undefined: VerifyPassword`.

- [ ] **Step 3: Write the implementation**

Create `internal/auth/password.go`:

```go
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
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/auth/ -v`
Expected: PASS — four test functions.

- [ ] **Step 5: Commit**

```bash
gofmt -w . && go test ./... && GOOS=windows GOARCH=amd64 go build ./...
git add internal/auth/
git commit -m "feat: bcrypt password hashing and verification"
```

---

### Task 3: Signed session cookies

**Files:**
- Create: `internal/auth/session.go`
- Test: `internal/auth/session_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `auth.SessionManager`; `auth.NewSessionManager(secret []byte, ttl time.Duration) *SessionManager`; `(*SessionManager).Issue() (string, error)`; `(*SessionManager).Verify(token string) (nonce string, err error)`; `(*SessionManager).TTL() time.Duration`; `(*SessionManager).SetClock(func() time.Time)`; sentinel errors `auth.ErrMalformedToken`, `auth.ErrBadSignature`, `auth.ErrExpired`.

- [ ] **Step 1: Write the failing test**

Create `internal/auth/session_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/auth/ -run Session -v`
Expected: FAIL — `undefined: NewSessionManager`.

- [ ] **Step 3: Write the implementation**

Create `internal/auth/session.go`:

```go
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
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/auth/ -v`
Expected: PASS — password and session tests both green.

- [ ] **Step 5: Commit**

```bash
gofmt -w . && go test ./... && GOOS=windows GOARCH=amd64 go build ./...
git add internal/auth/
git commit -m "feat: stateless HMAC-signed session cookies"
```

---

### Task 4: CSRF tokens

**Files:**
- Create: `internal/auth/csrf.go`
- Test: `internal/auth/csrf_test.go`

**Interfaces:**
- Consumes: `auth.SessionManager` from Task 3.
- Produces: `(*SessionManager).CSRFToken(nonce string) string`; `(*SessionManager).ValidCSRF(nonce, token string) bool`.

- [ ] **Step 1: Write the failing test**

Create `internal/auth/csrf_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/auth/ -run CSRF -v`
Expected: FAIL — `m.CSRFToken undefined`.

- [ ] **Step 3: Write the implementation**

Create `internal/auth/csrf.go`:

```go
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
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/auth/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w . && go test ./... && GOOS=windows GOARCH=amd64 go build ./...
git add internal/auth/
git commit -m "feat: derive per-session CSRF tokens"
```

---

### Task 5: Login rate limiter

**Files:**
- Create: `internal/auth/ratelimit.go`
- Test: `internal/auth/ratelimit_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `auth.DefaultPerIPLimit = 5`, `auth.DefaultGlobalLimit = 20`, `auth.DefaultWindow = 15 * time.Minute`; `auth.NewLimiter(perIPLimit, globalLimit int, window time.Duration) *Limiter`; `(*Limiter).Allow(ip string) (bool, time.Duration)`; `(*Limiter).RecordFailure(ip string)`; `(*Limiter).Reset(ip string)`; `(*Limiter).SetClock(func() time.Time)`.

- [ ] **Step 1: Write the failing test**

Create `internal/auth/ratelimit_test.go`:

```go
package auth

import (
	"fmt"
	"testing"
	"time"
)

func newTestLimiter(perIP, global int) (*Limiter, *time.Time) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(perIP, global, 15*time.Minute)
	l.SetClock(func() time.Time { return now })
	return l, &now
}

func TestLimiterAllowsUpToThePerIPLimit(t *testing.T) {
	l, _ := newTestLimiter(5, 20)

	// Five failures are tolerated; the sixth attempt is what gets blocked.
	for i := 0; i < 5; i++ {
		if ok, _ := l.Allow("1.2.3.4"); !ok {
			t.Fatalf("attempt %d blocked, want allowed", i+1)
		}
		l.RecordFailure("1.2.3.4")
	}

	ok, retry := l.Allow("1.2.3.4")
	if ok {
		t.Error("sixth attempt allowed, want blocked")
	}
	if retry <= 0 || retry > 15*time.Minute {
		t.Errorf("retryAfter = %v, want a positive duration no greater than the window", retry)
	}
}

func TestLimiterIsPerIP(t *testing.T) {
	l, _ := newTestLimiter(5, 20)
	for i := 0; i < 5; i++ {
		l.RecordFailure("1.2.3.4")
	}
	if ok, _ := l.Allow("5.6.7.8"); !ok {
		t.Error("a different IP was blocked by another IP's failures")
	}
}

func TestLimiterForgetsAfterTheWindow(t *testing.T) {
	l, now := newTestLimiter(5, 20)
	for i := 0; i < 5; i++ {
		l.RecordFailure("1.2.3.4")
	}
	if ok, _ := l.Allow("1.2.3.4"); ok {
		t.Fatal("expected the IP to be blocked before the window elapses")
	}

	*now = now.Add(15*time.Minute + time.Second)
	if ok, _ := l.Allow("1.2.3.4"); !ok {
		t.Error("still blocked after the window elapsed, want allowed")
	}
}

func TestGlobalCeilingBlocksAFreshIP(t *testing.T) {
	l, _ := newTestLimiter(5, 20)
	for i := 0; i < 20; i++ {
		l.RecordFailure(fmt.Sprintf("10.0.0.%d", i))
	}
	ok, retry := l.Allow("192.168.1.1")
	if ok {
		t.Error("a previously unseen IP was allowed past the global ceiling")
	}
	if retry <= 0 {
		t.Errorf("retryAfter = %v, want positive", retry)
	}
}

func TestResetClearsAnIP(t *testing.T) {
	l, _ := newTestLimiter(5, 20)
	for i := 0; i < 5; i++ {
		l.RecordFailure("1.2.3.4")
	}
	l.Reset("1.2.3.4")
	if ok, _ := l.Allow("1.2.3.4"); !ok {
		t.Error("Allow() = false after Reset(), want true")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/auth/ -run Limiter -v`
Expected: FAIL — `undefined: NewLimiter`.

- [ ] **Step 3: Write the implementation**

Create `internal/auth/ratelimit.go`:

```go
package auth

import (
	"sync"
	"time"
)

const (
	DefaultPerIPLimit  = 5
	DefaultGlobalLimit = 20
	DefaultWindow      = 15 * time.Minute
)

// Limiter counts failed logins in a sliding window, per client IP and globally.
// The global ceiling is a deliberate trade: an attacker can lock the owner out
// for up to one window, and the fallback is physical access to the PC.
type Limiter struct {
	mu          sync.Mutex
	perIP       map[string][]time.Time
	global      []time.Time
	perIPLimit  int
	globalLimit int
	window      time.Duration
	now         func() time.Time
}

func NewLimiter(perIPLimit, globalLimit int, window time.Duration) *Limiter {
	return &Limiter{
		perIP:       make(map[string][]time.Time),
		perIPLimit:  perIPLimit,
		globalLimit: globalLimit,
		window:      window,
		now:         time.Now,
	}
}

// SetClock replaces the time source. Intended for tests.
func (l *Limiter) SetClock(now func() time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.now = now
}

// Allow reports whether a login attempt from ip may proceed. When it may not,
// the duration is how long until the oldest counted failure ages out.
func (l *Limiter) Allow(ip string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.pruneLocked(now)

	if hits := l.perIP[ip]; len(hits) >= l.perIPLimit {
		return false, l.window - now.Sub(hits[0])
	}
	if len(l.global) >= l.globalLimit {
		return false, l.window - now.Sub(l.global[0])
	}
	return true, 0
}

// RecordFailure counts one failed login against ip and the global ceiling.
func (l *Limiter) RecordFailure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.pruneLocked(now)
	l.perIP[ip] = append(l.perIP[ip], now)
	l.global = append(l.global, now)
}

// Reset clears an IP's history, called after a successful login.
func (l *Limiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.perIP, ip)
}

func (l *Limiter) pruneLocked(now time.Time) {
	cutoff := now.Add(-l.window)
	l.global = pruneBefore(l.global, cutoff)
	for ip, hits := range l.perIP {
		kept := pruneBefore(hits, cutoff)
		if len(kept) == 0 {
			delete(l.perIP, ip)
			continue
		}
		l.perIP[ip] = kept
	}
}

// pruneBefore drops leading entries at or before cutoff. The slices are always
// in ascending time order because entries are only ever appended.
func pruneBefore(times []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(times) && !times[i].After(cutoff) {
		i++
	}
	return times[i:]
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/auth/ -v`
Expected: PASS — all password, session, CSRF and limiter tests.

- [ ] **Step 5: Commit**

```bash
gofmt -w . && go test ./... && GOOS=windows GOARCH=amd64 go build ./...
git add internal/auth/
git commit -m "feat: sliding-window login rate limiter"
```

---

### Task 6: Power controller core

**Files:**
- Create: `internal/power/controller.go`, `internal/power/args.go`, `internal/power/fake.go`, `internal/power/unsupported.go`
- Test: `internal/power/power_test.go`, `internal/power/unsupported_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `power.Action` string type with `ActionShutdown`/`ActionRestart`/`ActionSleep`/`ActionHibernate`; `(Action).Valid() bool`; `(Action).Suspends() bool`; `power.Capabilities{Sleep, Hibernate bool}` with `(Capabilities).Allows(Action) bool`; `power.Controller` interface with `Execute(context.Context, Action, bool) error` and `Capabilities(context.Context) (Capabilities, error)`; `power.ErrUnsupported`; `power.New() Controller`; `power.Fake` with `NewFake()`, `SetCapabilities`, `SetError`, `Calls() []Call`; `power.Call{Action, Force}`; unexported `buildShutdownArgs(Action, bool) []string`.

- [ ] **Step 1: Write the failing test**

Create `internal/power/power_test.go`:

```go
package power

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestBuildShutdownArgs(t *testing.T) {
	tests := []struct {
		name   string
		action Action
		force  bool
		want   []string
	}{
		{"shutdown forced", ActionShutdown, true, []string{"/s", "/t", "0", "/f"}},
		{"shutdown graceful", ActionShutdown, false, []string{"/s", "/t", "0"}},
		{"restart forced", ActionRestart, true, []string{"/r", "/t", "0", "/f"}},
		{"restart graceful", ActionRestart, false, []string{"/r", "/t", "0"}},
		{"sleep is not a shutdown.exe action", ActionSleep, true, nil},
		{"hibernate is not a shutdown.exe action", ActionHibernate, false, nil},
		{"unknown action", Action("explode"), true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildShutdownArgs(tt.action, tt.force)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildShutdownArgs(%q, %t) = %v, want %v", tt.action, tt.force, got, tt.want)
			}
		})
	}
}

func TestActionValid(t *testing.T) {
	for _, a := range []Action{ActionShutdown, ActionRestart, ActionSleep, ActionHibernate} {
		if !a.Valid() {
			t.Errorf("Action(%q).Valid() = false, want true", a)
		}
	}
	for _, a := range []Action{"", "Shutdown", "poweroff", "explode"} {
		if a.Valid() {
			t.Errorf("Action(%q).Valid() = true, want false", a)
		}
	}
}

func TestActionSuspends(t *testing.T) {
	if !ActionSleep.Suspends() || !ActionHibernate.Suspends() {
		t.Error("sleep and hibernate must report Suspends() = true")
	}
	if ActionShutdown.Suspends() || ActionRestart.Suspends() {
		t.Error("shutdown and restart must report Suspends() = false")
	}
}

func TestCapabilitiesAllows(t *testing.T) {
	none := Capabilities{}
	// Shutdown and restart are never capability-gated.
	if !none.Allows(ActionShutdown) || !none.Allows(ActionRestart) {
		t.Error("shutdown and restart must be allowed regardless of capabilities")
	}
	if none.Allows(ActionSleep) || none.Allows(ActionHibernate) {
		t.Error("sleep and hibernate must be refused when unavailable")
	}

	both := Capabilities{Sleep: true, Hibernate: true}
	if !both.Allows(ActionSleep) || !both.Allows(ActionHibernate) {
		t.Error("sleep and hibernate must be allowed when available")
	}

	sleepOnly := Capabilities{Sleep: true}
	if !sleepOnly.Allows(ActionSleep) || sleepOnly.Allows(ActionHibernate) {
		t.Error("capabilities must be honoured independently")
	}
}

func TestFakeRecordsCalls(t *testing.T) {
	f := NewFake()
	ctx := context.Background()

	if err := f.Execute(ctx, ActionShutdown, true); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if err := f.Execute(ctx, ActionSleep, false); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}

	want := []Call{{Action: ActionShutdown, Force: true}, {Action: ActionSleep, Force: false}}
	if got := f.Calls(); !reflect.DeepEqual(got, want) {
		t.Errorf("Calls() = %v, want %v", got, want)
	}
}

func TestFakeDefaultsToFullCapabilities(t *testing.T) {
	caps, err := NewFake().Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities() error = %v", err)
	}
	if !caps.Sleep || !caps.Hibernate {
		t.Errorf("Capabilities() = %+v, want both true", caps)
	}
}

func TestFakeReturnsConfiguredError(t *testing.T) {
	f := NewFake()
	boom := errors.New("boom")
	f.SetError(boom)
	if err := f.Execute(context.Background(), ActionShutdown, true); !errors.Is(err, boom) {
		t.Errorf("Execute() error = %v, want boom", err)
	}
}

func TestFakeHonoursConfiguredCapabilities(t *testing.T) {
	f := NewFake()
	f.SetCapabilities(Capabilities{Sleep: true})
	caps, _ := f.Capabilities(context.Background())
	if !caps.Sleep || caps.Hibernate {
		t.Errorf("Capabilities() = %+v, want {Sleep:true Hibernate:false}", caps)
	}
}
```

Create `internal/power/unsupported_test.go`:

```go
//go:build !windows

package power

import (
	"context"
	"errors"
	"testing"
)

func TestNewOnNonWindowsRefusesEverything(t *testing.T) {
	c := New()
	if err := c.Execute(context.Background(), ActionShutdown, true); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Execute() error = %v, want ErrUnsupported", err)
	}
	caps, err := c.Capabilities(context.Background())
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("Capabilities() error = %v, want ErrUnsupported", err)
	}
	if caps.Sleep || caps.Hibernate {
		t.Errorf("Capabilities() = %+v, want both false", caps)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/power/ -v`
Expected: FAIL — `undefined: Action`, `undefined: buildShutdownArgs`, `undefined: NewFake`.

- [ ] **Step 3: Write the interface and types**

Create `internal/power/controller.go`:

```go
// Package power executes machine power state changes. Everything above this
// package talks to Controller, which is what allows the rest of Shutdowner to
// be developed and tested on a non-Windows machine.
package power

import (
	"context"
	"errors"
)

type Action string

const (
	ActionShutdown  Action = "shutdown"
	ActionRestart   Action = "restart"
	ActionSleep     Action = "sleep"
	ActionHibernate Action = "hibernate"
)

func (a Action) Valid() bool {
	switch a {
	case ActionShutdown, ActionRestart, ActionSleep, ActionHibernate:
		return true
	}
	return false
}

// Suspends reports whether the action puts the machine into a low power state
// rather than turning it off. Only suspending actions are capability-gated.
func (a Action) Suspends() bool {
	return a == ActionSleep || a == ActionHibernate
}

// Capabilities describes which suspend states this machine supports. Hibernate
// is commonly unavailable because it has been turned off with powercfg /h off.
type Capabilities struct {
	Sleep     bool `json:"sleep"`
	Hibernate bool `json:"hibernate"`
}

// Allows reports whether a is permitted. Shutdown and restart always are.
func (c Capabilities) Allows(a Action) bool {
	switch a {
	case ActionSleep:
		return c.Sleep
	case ActionHibernate:
		return c.Hibernate
	}
	return true
}

// ErrUnsupported is returned by the non-Windows build of the controller.
var ErrUnsupported = errors.New("power: not supported on this platform")

type Controller interface {
	Execute(ctx context.Context, a Action, force bool) error
	Capabilities(ctx context.Context) (Capabilities, error)
}
```

Create `internal/power/args.go`:

```go
package power

// buildShutdownArgs returns the shutdown.exe arguments for a, or nil when a is
// not an action shutdown.exe handles. Kept separate from the Windows-only code
// so the flag logic is unit-testable on any platform.
func buildShutdownArgs(a Action, force bool) []string {
	var args []string
	switch a {
	case ActionShutdown:
		args = []string{"/s", "/t", "0"}
	case ActionRestart:
		args = []string{"/r", "/t", "0"}
	default:
		return nil
	}
	if force {
		args = append(args, "/f")
	}
	return args
}
```

- [ ] **Step 4: Write the fake and the non-Windows controller**

Create `internal/power/fake.go` (no build tag — it backs both the tests and the `--fake-power` flag):

```go
package power

import (
	"context"
	"sync"
)

// Call records one Execute invocation.
type Call struct {
	Action Action
	Force  bool
}

// Fake is a Controller that records calls instead of touching the machine. It
// backs the unit tests and the --fake-power development flag.
type Fake struct {
	mu    sync.Mutex
	caps  Capabilities
	err   error
	calls []Call
}

// NewFake returns a Fake reporting every capability as available.
func NewFake() *Fake {
	return &Fake{caps: Capabilities{Sleep: true, Hibernate: true}}
}

func (f *Fake) SetCapabilities(c Capabilities) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.caps = c
}

// SetError makes every subsequent Execute return err.
func (f *Fake) SetError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *Fake) Execute(_ context.Context, a Action, force bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{Action: a, Force: force})
	return f.err
}

func (f *Fake) Capabilities(context.Context) (Capabilities, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.caps, nil
}

// Calls returns a copy of the recorded calls.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Call, len(f.calls))
	copy(out, f.calls)
	return out
}
```

Create `internal/power/unsupported.go`:

```go
//go:build !windows

package power

import "context"

type systemController struct{}

// New returns the controller for the current platform. Off Windows every action
// fails; use --fake-power for development.
func New() Controller { return systemController{} }

func (systemController) Execute(context.Context, Action, bool) error {
	return ErrUnsupported
}

func (systemController) Capabilities(context.Context) (Capabilities, error) {
	return Capabilities{}, ErrUnsupported
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/power/ -v`
Expected: PASS — every subtest of `TestBuildShutdownArgs` plus the fake and unsupported tests.

- [ ] **Step 6: Commit**

```bash
gofmt -w . && go test ./...
git add internal/power/
git commit -m "feat: power controller interface, argument builder and fake"
```

Note: `GOOS=windows GOARCH=amd64 go build ./...` succeeds here, despite there being no Windows `systemController` yet. On a Windows target `unsupported.go` is excluded by its build tag, leaving a `power` package that simply has no `New` — and since nothing calls `power.New()` until Task 16 wires it, there is no dangling reference to fail on. The normal both-builds-green constraint therefore applies to this task like any other.

---

### Task 7: Windows power implementation

**Files:**
- Create: `internal/power/windows.go`

**Interfaces:**
- Consumes: `Action`, `Capabilities`, `Controller`, `buildShutdownArgs` from Task 6.
- Produces: the `windows` build of `power.New() Controller`.

This task has no unit tests: it is a thin wrapper over syscalls that cannot execute here. Its verification is that it compiles and vets clean for Windows, and it is covered by items 4-7 of the manual checklist in Task 16.

- [ ] **Step 1: Confirm the Windows target currently has no controller**

Run: `GOOS=windows GOARCH=amd64 go doc shutdowner/internal/power New`
Expected: an error reporting no symbol `New` — the Windows build of the package compiles but exposes no constructor, which is exactly the gap this task fills.

- [ ] **Step 2: Write the implementation**

Create `internal/power/windows.go`:

```go
//go:build windows

package power

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	powrprof               = windows.NewLazySystemDLL("powrprof.dll")
	procSetSuspendState    = powrprof.NewProc("SetSuspendState")
	procGetPwrCapabilities = powrprof.NewProc("GetPwrCapabilities")
)

// systemPowerCapabilities mirrors the leading fields of the Win32
// SYSTEM_POWER_CAPABILITIES struct. GetPwrCapabilities takes no size argument
// and writes the entire struct, so the tail is padded generously rather than
// transcribed field by field. Only SystemS3, SystemS4 and HiberFilePresent are
// read; over-allocating the buffer is safe, under-allocating is not.
type systemPowerCapabilities struct {
	PowerButtonPresent byte
	SleepButtonPresent byte
	LidPresent         byte
	SystemS1           byte
	SystemS2           byte
	SystemS3           byte
	SystemS4           byte
	SystemS5           byte
	HiberFilePresent   byte
	_                  [512]byte
}

type systemController struct{}

func New() Controller { return systemController{} }

func (systemController) Execute(ctx context.Context, a Action, force bool) error {
	switch a {
	case ActionShutdown, ActionRestart:
		return runShutdownExe(ctx, a, force)
	case ActionSleep:
		return setSuspendState(false, force)
	case ActionHibernate:
		return setSuspendState(true, force)
	}
	return fmt.Errorf("power: unknown action %q", a)
}

// runShutdownExe drives shutdown.exe for the two actions it handles well. Its
// /f semantics are exactly what is wanted and are better tested than anything
// reimplemented over InitiateSystemShutdownEx would be.
func runShutdownExe(ctx context.Context, a Action, force bool) error {
	args := buildShutdownArgs(a, force)
	if args == nil {
		return fmt.Errorf("power: %q is not a shutdown.exe action", a)
	}
	cmd := exec.CommandContext(ctx, shutdownExePath(), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return fmt.Errorf("power: shutdown.exe %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return fmt.Errorf("power: shutdown.exe %s: %w", strings.Join(args, " "), err)
}

func shutdownExePath() string {
	if root := os.Getenv("SystemRoot"); root != "" {
		p := filepath.Join(root, "System32", "shutdown.exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "shutdown.exe"
}

// setSuspendState calls the API directly rather than going through
// "rundll32 powrprof.dll,SetSuspendState", because that form hibernates instead
// of sleeping whenever hibernation is enabled and so cannot distinguish the two.
//
// The call blocks until the machine resumes, so it returns when the sleep or
// hibernation ends, not when it begins.
func setSuspendState(hibernate, force bool) error {
	r, _, err := procSetSuspendState.Call(boolArg(hibernate), boolArg(force), 0)
	if r != 0 {
		return nil
	}
	return fmt.Errorf("power: SetSuspendState(hibernate=%t): %w", hibernate, syscallError(err))
}

func (systemController) Capabilities(context.Context) (Capabilities, error) {
	var caps systemPowerCapabilities
	r, _, err := procGetPwrCapabilities.Call(uintptr(unsafe.Pointer(&caps)))
	if r == 0 {
		return Capabilities{}, fmt.Errorf("power: GetPwrCapabilities: %w", syscallError(err))
	}
	return Capabilities{
		Sleep:     caps.SystemS3 != 0,
		Hibernate: caps.SystemS4 != 0 && caps.HiberFilePresent != 0,
	}, nil
}

func boolArg(b bool) uintptr {
	if b {
		return 1
	}
	return 0
}

// syscallError normalises the error LazyProc.Call returns. It is never nil, and
// carries errno 0 ("The operation completed successfully") when the call failed
// without setting one, which would otherwise read as a confusing success.
func syscallError(err error) error {
	var errno syscall.Errno
	if errors.As(err, &errno) && errno != 0 {
		return errno
	}
	return errors.New("the call failed without setting an error code")
}
```

- [ ] **Step 3: Verify the Windows build and vet**

```bash
GOOS=windows GOARCH=amd64 go build ./...
GOOS=windows GOARCH=amd64 go vet ./...
go test ./...
```

Expected: all three silent or passing. The `!windows` test from Task 6 still passes on Linux; `unsupported.go` and `windows.go` never compile together.

- [ ] **Step 4: Commit**

```bash
gofmt -w .
git add internal/power/windows.go
git commit -m "feat: Windows power controller via shutdown.exe and powrprof"
```

---

### Task 8: System information

**Files:**
- Create: `internal/sysinfo/sysinfo.go`, `internal/sysinfo/sysinfo_windows.go`, `internal/sysinfo/sysinfo_other.go`
- Test: `internal/sysinfo/sysinfo_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `sysinfo.Info{Hostname, OS string, UptimeSeconds int64, LocalTime string}` with JSON tags `hostname`, `os`, `uptimeSeconds`, `localTime`; `sysinfo.Collect() Info`.

- [ ] **Step 1: Write the failing test**

Create `internal/sysinfo/sysinfo_test.go`:

```go
package sysinfo

import (
	"testing"
	"time"
)

func TestCollectReturnsUsableValues(t *testing.T) {
	info := Collect()

	if info.Hostname == "" {
		t.Error("Hostname is empty")
	}
	if info.OS == "" {
		t.Error("OS is empty")
	}
	if info.UptimeSeconds < 0 {
		t.Errorf("UptimeSeconds = %d, want >= 0", info.UptimeSeconds)
	}
	if _, err := time.Parse(time.RFC3339, info.LocalTime); err != nil {
		t.Errorf("LocalTime = %q, which does not parse as RFC3339: %v", info.LocalTime, err)
	}
}

func TestUptimeAdvances(t *testing.T) {
	first := Collect().UptimeSeconds
	time.Sleep(10 * time.Millisecond)
	if second := Collect().UptimeSeconds; second < first {
		t.Errorf("uptime went backwards: %d then %d", first, second)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/sysinfo/ -v`
Expected: FAIL — `undefined: Collect`.

- [ ] **Step 3: Write the portable part**

Create `internal/sysinfo/sysinfo.go`:

```go
// Package sysinfo reports the facts the dashboard shows about the machine.
package sysinfo

import (
	"os"
	"time"
)

type Info struct {
	Hostname      string `json:"hostname"`
	OS            string `json:"os"`
	UptimeSeconds int64  `json:"uptimeSeconds"`
	LocalTime     string `json:"localTime"`
}

// Collect gathers the current machine facts. It never fails: a value that
// cannot be read is reported as "unknown" rather than breaking the dashboard.
func Collect() Info {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return Info{
		Hostname:      host,
		OS:            osVersion(),
		UptimeSeconds: int64(uptime() / time.Second),
		LocalTime:     time.Now().Format(time.RFC3339),
	}
}
```

- [ ] **Step 4: Write the two platform implementations**

Create `internal/sysinfo/sysinfo_windows.go`:

```go
//go:build windows

package sysinfo

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

var procGetTickCount64 = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetTickCount64")

// osVersion reads the version through RtlGetVersion rather than shelling out,
// which avoids parsing localized command output. The friendly name is a best
// effort; the build number beside it is exact.
func osVersion() string {
	v := windows.RtlGetVersion()
	name := "Windows"
	switch {
	case v.MajorVersion == 10 && v.BuildNumber >= 22000:
		name = "Windows 11"
	case v.MajorVersion == 10:
		name = "Windows 10"
	}
	return fmt.Sprintf("%s (build %d)", name, v.BuildNumber)
}

func uptime() time.Duration {
	ms, _, _ := procGetTickCount64.Call()
	return time.Duration(ms) * time.Millisecond
}
```

Create `internal/sysinfo/sysinfo_other.go`:

```go
//go:build !windows

package sysinfo

import (
	"runtime"
	"time"
)

var processStart = time.Now()

func osVersion() string { return runtime.GOOS }

// uptime reports time since process start off Windows. The dashboard needs a
// plausible number during development, not an accurate one.
func uptime() time.Duration { return time.Since(processStart) }
```

- [ ] **Step 5: Run the test to verify it passes**

```bash
go test ./internal/sysinfo/ -v
GOOS=windows GOARCH=amd64 go build ./...
```

Expected: PASS, and the Windows build silent.

- [ ] **Step 6: Commit**

```bash
gofmt -w . && go test ./...
git add internal/sysinfo/
git commit -m "feat: hostname, OS version and uptime reporting"
```

---

### Task 9: Action state machine

**Files:**
- Create: `internal/action/manager.go`
- Test: `internal/action/manager_test.go`

**Interfaces:**
- Consumes: `power.Controller`, `power.Action`, `power.Capabilities`, `power.Fake` from Task 6.
- Produces: `action.State` with `StateIdle`/`StatePending`/`StateExecuting`/`StateFailed`; `action.FailedRetention = 60 * time.Second`; `action.Pending{ID string, Action power.Action, Force bool, RemainingSeconds int}`; `action.Status{State State, Pending *Pending, Error string}`; `action.Timer` interface; `action.New(ctrl power.Controller, delay time.Duration, opts ...Option) *Manager`; options `WithClock`, `WithAfterFunc`, `WithIDFunc`; `(*Manager).Schedule(ctx, power.Action, bool) (Pending, error)`; `(*Manager).Abort(id string) error`; `(*Manager).Status() Status`; errors `ErrInvalidAction`, `ErrUnsupportedAction`, `ErrConflict`, `ErrNoPending`.

- [ ] **Step 1: Write the failing test**

Create `internal/action/manager_test.go`:

```go
package action

import (
	"context"
	"errors"
	"testing"
	"time"

	"shutdowner/internal/power"
)

// manualTimer replaces time.AfterFunc so tests fire the countdown immediately
// instead of waiting for it.
type manualTimer struct {
	fn      func()
	stopped bool
}

func (t *manualTimer) Stop() bool {
	t.stopped = true
	return true
}

type harness struct {
	mgr   *Manager
	fake  *power.Fake
	timer *manualTimer
	now   time.Time
	ids   int
}

func newHarness(t *testing.T, delay time.Duration) *harness {
	t.Helper()
	h := &harness{
		fake: power.NewFake(),
		now:  time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
	}
	h.mgr = New(h.fake, delay,
		WithClock(func() time.Time { return h.now }),
		WithAfterFunc(func(_ time.Duration, fn func()) Timer {
			h.timer = &manualTimer{fn: fn}
			return h.timer
		}),
		WithIDFunc(func() string {
			h.ids++
			return "id-" + string(rune('a'+h.ids-1))
		}),
	)
	return h
}

// fire runs the scheduled callback, standing in for the countdown elapsing.
func (h *harness) fire(t *testing.T) {
	t.Helper()
	if h.timer == nil {
		t.Fatal("no timer was scheduled")
	}
	h.timer.fn()
}

func TestScheduleFromIdle(t *testing.T) {
	h := newHarness(t, 45*time.Second)

	p, err := h.mgr.Schedule(context.Background(), power.ActionShutdown, true)
	if err != nil {
		t.Fatalf("Schedule() error = %v, want nil", err)
	}
	if p.ID != "id-a" {
		t.Errorf("ID = %q, want id-a", p.ID)
	}
	if p.Action != power.ActionShutdown || !p.Force {
		t.Errorf("Pending = %+v, want shutdown with force", p)
	}
	if p.RemainingSeconds != 45 {
		t.Errorf("RemainingSeconds = %d, want 45", p.RemainingSeconds)
	}
	if s := h.mgr.Status(); s.State != StatePending {
		t.Errorf("State = %q, want pending", s.State)
	}
	if len(h.fake.Calls()) != 0 {
		t.Error("the controller was called before the countdown elapsed")
	}
}

func TestScheduleRejectsASecondAction(t *testing.T) {
	h := newHarness(t, 45*time.Second)
	if _, err := h.mgr.Schedule(context.Background(), power.ActionShutdown, true); err != nil {
		t.Fatalf("first Schedule() error = %v", err)
	}
	if _, err := h.mgr.Schedule(context.Background(), power.ActionRestart, true); !errors.Is(err, ErrConflict) {
		t.Errorf("second Schedule() error = %v, want ErrConflict", err)
	}
}

func TestScheduleRejectsAnInvalidAction(t *testing.T) {
	h := newHarness(t, 45*time.Second)
	if _, err := h.mgr.Schedule(context.Background(), power.Action("explode"), false); !errors.Is(err, ErrInvalidAction) {
		t.Errorf("Schedule() error = %v, want ErrInvalidAction", err)
	}
}

func TestScheduleRejectsUnavailableSuspendActions(t *testing.T) {
	h := newHarness(t, 45*time.Second)
	h.fake.SetCapabilities(power.Capabilities{Sleep: true})

	if _, err := h.mgr.Schedule(context.Background(), power.ActionHibernate, false); !errors.Is(err, ErrUnsupportedAction) {
		t.Errorf("hibernate Schedule() error = %v, want ErrUnsupportedAction", err)
	}
	if _, err := h.mgr.Schedule(context.Background(), power.ActionSleep, false); err != nil {
		t.Errorf("sleep Schedule() error = %v, want nil", err)
	}
}

func TestShutdownIsNeverCapabilityGated(t *testing.T) {
	h := newHarness(t, 45*time.Second)
	h.fake.SetCapabilities(power.Capabilities{})

	if _, err := h.mgr.Schedule(context.Background(), power.ActionShutdown, true); err != nil {
		t.Errorf("Schedule(shutdown) error = %v, want nil even with no capabilities", err)
	}
}

func TestFiringExecutesTheAction(t *testing.T) {
	h := newHarness(t, 45*time.Second)
	if _, err := h.mgr.Schedule(context.Background(), power.ActionRestart, false); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}

	h.fire(t)

	calls := h.fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("len(Calls()) = %d, want 1", len(calls))
	}
	if calls[0].Action != power.ActionRestart || calls[0].Force {
		t.Errorf("Calls()[0] = %+v, want restart without force", calls[0])
	}
	if s := h.mgr.Status(); s.State != StateIdle {
		t.Errorf("State = %q, want idle after a successful action", s.State)
	}
}

func TestExecutionFailureIsReported(t *testing.T) {
	h := newHarness(t, 45*time.Second)
	h.fake.SetError(errors.New("shutdown.exe exited 1"))

	if _, err := h.mgr.Schedule(context.Background(), power.ActionShutdown, true); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	h.fire(t)

	s := h.mgr.Status()
	if s.State != StateFailed {
		t.Fatalf("State = %q, want failed", s.State)
	}
	if s.Error != "shutdown.exe exited 1" {
		t.Errorf("Error = %q, want the underlying message", s.Error)
	}
}

func TestFailedRevertsToIdle(t *testing.T) {
	h := newHarness(t, 45*time.Second)
	h.fake.SetError(errors.New("nope"))
	if _, err := h.mgr.Schedule(context.Background(), power.ActionShutdown, true); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	h.fire(t)

	h.now = h.now.Add(FailedRetention - time.Second)
	if s := h.mgr.Status(); s.State != StateFailed {
		t.Errorf("State = %q just before the retention elapses, want failed", s.State)
	}

	h.now = h.now.Add(2 * time.Second)
	if s := h.mgr.Status(); s.State != StateIdle {
		t.Errorf("State = %q after the retention elapsed, want idle", s.State)
	}
}

func TestScheduleIsAllowedFromFailed(t *testing.T) {
	h := newHarness(t, 45*time.Second)
	h.fake.SetError(errors.New("nope"))
	if _, err := h.mgr.Schedule(context.Background(), power.ActionShutdown, true); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	h.fire(t)
	if s := h.mgr.Status(); s.State != StateFailed {
		t.Fatalf("State = %q, want failed", s.State)
	}

	h.fake.SetError(nil)
	if _, err := h.mgr.Schedule(context.Background(), power.ActionRestart, true); err != nil {
		t.Errorf("Schedule() from failed error = %v, want nil", err)
	}
}

func TestAbortCancelsAPendingAction(t *testing.T) {
	h := newHarness(t, 45*time.Second)
	p, err := h.mgr.Schedule(context.Background(), power.ActionShutdown, true)
	if err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}

	if err := h.mgr.Abort(p.ID); err != nil {
		t.Fatalf("Abort() error = %v, want nil", err)
	}
	if !h.timer.stopped {
		t.Error("the timer was not stopped")
	}
	if s := h.mgr.Status(); s.State != StateIdle {
		t.Errorf("State = %q, want idle", s.State)
	}

	// Even if the timer had already been racing towards firing, a stale
	// callback must not execute the aborted action.
	h.fire(t)
	if len(h.fake.Calls()) != 0 {
		t.Error("an aborted action still executed")
	}
}

func TestAbortRejectsAStaleID(t *testing.T) {
	h := newHarness(t, 45*time.Second)
	if _, err := h.mgr.Schedule(context.Background(), power.ActionShutdown, true); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	if err := h.mgr.Abort("id-from-an-old-tab"); !errors.Is(err, ErrNoPending) {
		t.Errorf("Abort() error = %v, want ErrNoPending", err)
	}
	if s := h.mgr.Status(); s.State != StatePending {
		t.Errorf("State = %q, want the original action still pending", s.State)
	}
}

func TestAbortWhenIdle(t *testing.T) {
	h := newHarness(t, 45*time.Second)
	if err := h.mgr.Abort("anything"); !errors.Is(err, ErrNoPending) {
		t.Errorf("Abort() error = %v, want ErrNoPending", err)
	}
}

func TestRemainingSecondsCountsDown(t *testing.T) {
	h := newHarness(t, 45*time.Second)
	if _, err := h.mgr.Schedule(context.Background(), power.ActionShutdown, true); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}

	h.now = h.now.Add(30 * time.Second)
	s := h.mgr.Status()
	if s.Pending == nil {
		t.Fatal("Status().Pending = nil, want a pending action")
	}
	if s.Pending.RemainingSeconds != 15 {
		t.Errorf("RemainingSeconds = %d, want 15", s.Pending.RemainingSeconds)
	}

	h.now = h.now.Add(time.Minute)
	if got := h.mgr.Status().Pending.RemainingSeconds; got != 0 {
		t.Errorf("RemainingSeconds = %d past the deadline, want 0 rather than a negative number", got)
	}
}

func TestStatusOmitsPendingWhenIdle(t *testing.T) {
	h := newHarness(t, 45*time.Second)
	s := h.mgr.Status()
	if s.State != StateIdle {
		t.Errorf("State = %q, want idle", s.State)
	}
	if s.Pending != nil {
		t.Errorf("Pending = %+v, want nil", s.Pending)
	}
	if s.Error != "" {
		t.Errorf("Error = %q, want empty", s.Error)
	}
}

// blockingController holds Execute open so the Executing state is observable.
type blockingController struct {
	release chan struct{}
	entered chan struct{}
}

func (b *blockingController) Execute(context.Context, power.Action, bool) error {
	close(b.entered)
	<-b.release
	return nil
}

func (b *blockingController) Capabilities(context.Context) (power.Capabilities, error) {
	return power.Capabilities{Sleep: true, Hibernate: true}, nil
}

func TestScheduleRejectedWhileExecuting(t *testing.T) {
	ctrl := &blockingController{release: make(chan struct{}), entered: make(chan struct{})}
	var timer *manualTimer
	m := New(ctrl, time.Second, WithAfterFunc(func(_ time.Duration, fn func()) Timer {
		timer = &manualTimer{fn: fn}
		return timer
	}))

	if _, err := m.Schedule(context.Background(), power.ActionShutdown, true); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	go timer.fn()
	<-ctrl.entered

	if s := m.Status(); s.State != StateExecuting {
		t.Errorf("State = %q, want executing", s.State)
	}
	if _, err := m.Schedule(context.Background(), power.ActionRestart, true); !errors.Is(err, ErrConflict) {
		t.Errorf("Schedule() while executing error = %v, want ErrConflict", err)
	}
	close(ctrl.release)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/action/ -v`
Expected: FAIL — `undefined: New`, `undefined: Manager`.

- [ ] **Step 3: Write the implementation**

Create `internal/action/manager.go`:

```go
// Package action owns the countdown between confirming a power action and
// executing it, and the abort that cancels it.
package action

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"shutdowner/internal/power"
)

type State string

const (
	StateIdle      State = "idle"
	StatePending   State = "pending"
	StateExecuting State = "executing"
	StateFailed    State = "failed"
)

// FailedRetention is how long a failure is reported before the manager returns
// to idle on its own, so no dismissal endpoint is needed.
const FailedRetention = 60 * time.Second

var (
	ErrInvalidAction     = errors.New("action: unknown action")
	ErrUnsupportedAction = errors.New("action: not available on this system")
	ErrConflict          = errors.New("action: another action is already in progress")
	ErrNoPending         = errors.New("action: no matching pending action")
)

type Pending struct {
	ID     string       `json:"id"`
	Action power.Action `json:"action"`
	Force  bool         `json:"force"`
	// RemainingSeconds is relative rather than an absolute deadline so a client
	// with a skewed clock still renders an accurate countdown.
	RemainingSeconds int `json:"remainingSeconds"`
}

type Status struct {
	State   State    `json:"state"`
	Pending *Pending `json:"pending"`
	Error   string   `json:"error"`
}

// Timer is the part of time.Timer the manager needs, so tests can fire the
// countdown immediately instead of waiting for it.
type Timer interface{ Stop() bool }

type Option func(*Manager)

// WithClock replaces the time source. Intended for tests.
func WithClock(now func() time.Time) Option {
	return func(m *Manager) { m.now = now }
}

// WithAfterFunc replaces the countdown scheduler. Intended for tests.
func WithAfterFunc(f func(time.Duration, func()) Timer) Option {
	return func(m *Manager) { m.afterFunc = f }
}

// WithIDFunc replaces action ID generation. Intended for tests.
func WithIDFunc(f func() string) Option {
	return func(m *Manager) { m.newID = f }
}

// Manager holds the single in-flight action. The countdown lives here rather
// than in shutdown.exe /t because Windows cannot cancel a pending sleep or
// hibernate, so relying on shutdown /a would give Abort for only two of the
// four actions.
//
// An action pending when the process dies is lost rather than executed. Failing
// toward "the PC stays on" is the safe direction.
type Manager struct {
	ctrl  power.Controller
	delay time.Duration

	now       func() time.Time
	afterFunc func(time.Duration, func()) Timer
	newID     func() string

	mu       sync.Mutex
	state    State
	id       string
	action   power.Action
	force    bool
	firesAt  time.Time
	timer    Timer
	lastErr  string
	failedAt time.Time
}

func New(ctrl power.Controller, delay time.Duration, opts ...Option) *Manager {
	m := &Manager{
		ctrl:  ctrl,
		delay: delay,
		now:   time.Now,
		afterFunc: func(d time.Duration, fn func()) Timer {
			return time.AfterFunc(d, fn)
		},
		newID: randomID,
		state: StateIdle,
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Schedule starts the countdown for a. Capabilities are checked here rather
// than when the timer fires, so an unavailable action is refused immediately
// instead of failing silently a minute later.
func (m *Manager) Schedule(ctx context.Context, a power.Action, force bool) (Pending, error) {
	if !a.Valid() {
		return Pending{}, ErrInvalidAction
	}
	if a.Suspends() {
		caps, err := m.ctrl.Capabilities(ctx)
		if err != nil || !caps.Allows(a) {
			return Pending{}, ErrUnsupportedAction
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireFailedLocked()
	if m.state == StatePending || m.state == StateExecuting {
		return Pending{}, ErrConflict
	}

	now := m.now()
	m.state = StatePending
	m.id = m.newID()
	m.action = a
	m.force = force
	m.firesAt = now.Add(m.delay)
	m.lastErr = ""

	id := m.id
	m.timer = m.afterFunc(m.delay, func() { m.fire(id) })

	return m.pendingLocked(now), nil
}

// fire executes the action if it is still the one that was scheduled. The id
// check makes a stale timer callback a no-op after an abort.
func (m *Manager) fire(id string) {
	m.mu.Lock()
	if m.state != StatePending || m.id != id {
		m.mu.Unlock()
		return
	}
	m.state = StateExecuting
	a, force := m.action, m.force
	m.mu.Unlock()

	err := m.ctrl.Execute(context.Background(), a, force)

	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.state = StateFailed
		m.lastErr = err.Error()
		m.failedAt = m.now()
		return
	}
	// Sleep and hibernate reach here only once the machine has resumed, because
	// SetSuspendState blocks for the whole suspension. Shutdown and restart
	// never reach here at all: the process dies mid-call.
	m.state = StateIdle
	m.lastErr = ""
}

// Abort cancels the pending action when id matches it, so a stale browser tab
// cannot cancel something queued after its page was rendered.
func (m *Manager) Abort(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != StatePending || m.id != id {
		return ErrNoPending
	}
	if m.timer != nil {
		m.timer.Stop()
	}
	m.timer = nil
	m.state = StateIdle
	m.id = ""
	return nil
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireFailedLocked()

	s := Status{State: m.state}
	switch m.state {
	case StatePending:
		p := m.pendingLocked(m.now())
		s.Pending = &p
	case StateFailed:
		s.Error = m.lastErr
	}
	return s
}

func (m *Manager) pendingLocked(now time.Time) Pending {
	remaining := int(m.firesAt.Sub(now) / time.Second)
	if remaining < 0 {
		remaining = 0
	}
	return Pending{ID: m.id, Action: m.action, Force: m.force, RemainingSeconds: remaining}
}

func (m *Manager) expireFailedLocked() {
	if m.state == StateFailed && m.now().Sub(m.failedAt) >= FailedRetention {
		m.state = StateIdle
		m.lastErr = ""
	}
}

func randomID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// A predictable id is still safe: Abort also requires a valid session.
		return "fallback"
	}
	return hex.EncodeToString(b)
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
go test ./internal/action/ -v
go test -race ./internal/action/
```

Expected: PASS both times. The race detector matters here: `fire` runs on the timer goroutine while `Status` runs on request goroutines.

- [ ] **Step 5: Commit**

```bash
gofmt -w . && go test ./... && GOOS=windows GOARCH=amd64 go build ./...
git add internal/action/
git commit -m "feat: abortable countdown state machine"
```

---

### Task 10: Rotating log writer

**Files:**
- Create: `internal/logging/rotate.go`
- Test: `internal/logging/rotate_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `logging.NewRotatingWriter(path string, maxBytes int64, keep int) (*RotatingWriter, error)`; `(*RotatingWriter).Write([]byte) (int, error)`; `(*RotatingWriter).Close() error`; `logging.DefaultMaxBytes = 5 << 20`; `logging.DefaultKeep = 2`.

- [ ] **Step 1: Write the failing test**

Create `internal/logging/rotate_test.go`:

```go
package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriterCreatesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	w, err := NewRotatingWriter(path, 1024, 2)
	if err != nil {
		t.Fatalf("NewRotatingWriter() error = %v", err)
	}
	defer w.Close()

	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(b) != "hello\n" {
		t.Errorf("file contents = %q, want %q", b, "hello\n")
	}
}

func TestWriterAppendsToAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	if err := os.WriteFile(path, []byte("earlier\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := NewRotatingWriter(path, 1024, 2)
	if err != nil {
		t.Fatalf("NewRotatingWriter() error = %v", err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("later\n")); err != nil {
		t.Fatal(err)
	}

	b, _ := os.ReadFile(path)
	if string(b) != "earlier\nlater\n" {
		t.Errorf("file contents = %q, want the earlier content preserved", b)
	}
}

func TestWriterRotatesWhenFull(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := NewRotatingWriter(path, 20, 2)
	if err != nil {
		t.Fatalf("NewRotatingWriter() error = %v", err)
	}
	defer w.Close()

	// Each line is 10 bytes, so the third crosses the 20 byte limit.
	for i := 0; i < 3; i++ {
		if _, err := w.Write([]byte(fmt.Sprintf("line-%04d\n", i))); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected %s.1 to exist after rotation: %v", path, err)
	}
	current, _ := os.ReadFile(path)
	if !strings.Contains(string(current), "line-0002") {
		t.Errorf("current log = %q, want it to hold the newest line", current)
	}
	rotated, _ := os.ReadFile(path + ".1")
	if !strings.Contains(string(rotated), "line-0000") {
		t.Errorf("rotated log = %q, want it to hold the oldest lines", rotated)
	}
}

func TestWriterKeepsOnlyTheConfiguredGenerations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := NewRotatingWriter(path, 20, 2)
	if err != nil {
		t.Fatalf("NewRotatingWriter() error = %v", err)
	}
	defer w.Close()

	for i := 0; i < 12; i++ {
		if _, err := w.Write([]byte(fmt.Sprintf("line-%04d\n", i))); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}

	if _, err := os.Stat(path + ".2"); err != nil {
		t.Errorf("expected %s.2 to exist: %v", path, err)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Errorf("expected %s.3 not to exist with keep=2", path)
	}
}

func TestWriteLargerThanTheLimitStillSucceeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	w, err := NewRotatingWriter(path, 8, 1)
	if err != nil {
		t.Fatalf("NewRotatingWriter() error = %v", err)
	}
	defer w.Close()

	big := strings.Repeat("x", 100)
	n, err := w.Write([]byte(big))
	if err != nil {
		t.Fatalf("Write() error = %v, want nil", err)
	}
	if n != len(big) {
		t.Errorf("Write() = %d, want %d", n, len(big))
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/logging/ -v`
Expected: FAIL — `undefined: NewRotatingWriter`.

- [ ] **Step 3: Write the implementation**

Create `internal/logging/rotate.go`:

```go
// Package logging provides a size-rotating io.Writer. It is hand-rolled to keep
// the dependency count at three.
package logging

import (
	"fmt"
	"os"
	"sync"
)

const (
	DefaultMaxBytes int64 = 5 << 20 // 5 MB
	DefaultKeep           = 2
)

// RotatingWriter appends to path, rolling it over to path.1, path.2 and so on
// once it exceeds maxBytes. A Windows service has no console, so this file is
// the only place its output goes.
type RotatingWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	keep     int
	size     int64
	f        *os.File
}

func NewRotatingWriter(path string, maxBytes int64, keep int) (*RotatingWriter, error) {
	w := &RotatingWriter{path: path, maxBytes: maxBytes, keep: keep}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *RotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening log file %s: %w", w.path, err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("stat log file %s: %w", w.path, err)
	}
	w.f = f
	w.size = info.Size()
	return nil
}

func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// A single write larger than the limit is written whole rather than split;
	// rotating first keeps it in a file of its own.
	if w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *RotatingWriter) rotateLocked() error {
	if err := w.f.Close(); err != nil {
		return err
	}
	// Drop the oldest generation, then shift each remaining one down.
	_ = os.Remove(fmt.Sprintf("%s.%d", w.path, w.keep))
	for i := w.keep - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1))
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil {
		// Reopen so logging survives a rename failure rather than going silent.
		if oerr := w.open(); oerr != nil {
			return oerr
		}
		return err
	}
	return w.open()
}

func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/logging/ -v`
Expected: PASS — five test functions.

- [ ] **Step 5: Commit**

```bash
gofmt -w . && go test ./... && GOOS=windows GOARCH=amd64 go build ./...
git add internal/logging/
git commit -m "feat: size-rotating log writer"
```

---

### Task 11: Web server skeleton and middleware

**Files:**
- Create: `internal/web/server.go`, `internal/web/middleware.go`, `internal/web/json.go`
- Test: `internal/web/web_test.go`, `internal/web/middleware_test.go`

**Interfaces:**
- Consumes: `auth.SessionManager`, `auth.Limiter`, `auth.VerifyPassword` (Tasks 2-5); `action.Manager` (Task 9); `power.Controller`, `power.Fake` (Task 6).
- Produces: `web.Options{Sessions, Limiter, Actions, Power, Logger, PasswordHash, DelaySeconds}`; `web.New(Options) (*Server, error)`; `(*Server).Routes() http.Handler`; `web.SessionCookieName = "shutdowner_session"`; `web.ClientIP(*http.Request) string`; unexported `securityHeaders`, `recoverPanic`, `(*Server).requireSession`, `(*Server).requireCSRF`, `(*Server).setSessionCookie`, `(*Server).clearSessionCookie`, `nonceFrom`, `writeJSON`, `writeJSONError`; test helper `newTestEnv`.

- [ ] **Step 1: Write the failing test**

Create `internal/web/web_test.go` — shared helpers used by Tasks 11, 12 and 13:

```go
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
	// A zero delay is not used here: tests need a countdown long enough to
	// observe the pending state before it fires.
	actions := action.New(fake, 45*time.Second)
	limiter := auth.NewLimiter(auth.DefaultPerIPLimit, auth.DefaultGlobalLimit, auth.DefaultWindow)

	srv, err := New(Options{
		Sessions:     auth.NewSessionManager([]byte("0123456789abcdef0123456789abcdef"), time.Hour),
		Limiter:      limiter,
		Actions:      actions,
		Power:        fake,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		PasswordHash: hash,
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
```

Create `internal/web/middleware_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/web/ -v`
Expected: FAIL — `undefined: New`, `undefined: Options`, `undefined: ClientIP`.

- [ ] **Step 3: Write the server skeleton**

Create `internal/web/server.go`:

```go
// Package web serves the login page, the dashboard and the JSON API.
package web

import (
	"errors"
	"log/slog"
	"net/http"

	"shutdowner/internal/action"
	"shutdowner/internal/auth"
	"shutdowner/internal/power"
)

const SessionCookieName = "shutdowner_session"

type Options struct {
	Sessions     *auth.SessionManager
	Limiter      *auth.Limiter
	Actions      *action.Manager
	Power        power.Controller
	Logger       *slog.Logger
	PasswordHash string
	DelaySeconds int
}

type Server struct {
	sessions     *auth.SessionManager
	limiter      *auth.Limiter
	actions      *action.Manager
	power        power.Controller
	logger       *slog.Logger
	passwordHash string
	delaySeconds int
}

func New(o Options) (*Server, error) {
	if o.Sessions == nil || o.Limiter == nil || o.Actions == nil || o.Power == nil || o.Logger == nil {
		return nil, errors.New("web: Sessions, Limiter, Actions, Power and Logger are all required")
	}
	if o.PasswordHash == "" {
		return nil, errors.New("web: PasswordHash is required")
	}
	return &Server{
		sessions:     o.Sessions,
		limiter:      o.Limiter,
		actions:      o.Actions,
		power:        o.Power,
		logger:       o.Logger,
		passwordHash: o.PasswordHash,
		delaySeconds: o.DelaySeconds,
	}, nil
}

// Routes builds the handler tree. Tasks 12, 13 and 14 extend it.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	return securityHeaders(recoverPanic(s.logger, mux))
}

// handleHealth is the one unauthenticated route, so the tunnel has something to
// probe.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}
```

- [ ] **Step 4: Write the middleware and JSON helpers**

Create `internal/web/middleware.go`:

```go
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
```

Create `internal/web/json.go`:

```go
package web

import (
	"encoding/json"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/web/ -v`
Expected: PASS — health, security headers, panic recovery, ClientIP, and every requireSession/requireCSRF case.

- [ ] **Step 6: Commit**

```bash
gofmt -w . && go test ./... && GOOS=windows GOARCH=amd64 go build ./...
git add internal/web/
git commit -m "feat: web server skeleton, security middleware and session gates"
```

---

### Task 12: Login, logout and templates

**Files:**
- Create: `internal/web/templates.go`, `internal/web/templates/login.html`, `internal/web/templates/dashboard.html`
- Create: `internal/web/auth_handlers.go`
- Modify: `internal/web/server.go` (add the `tmpl` field, parse templates in `New`, extend `Routes`)
- Test: `internal/web/auth_handlers_test.go`

**Interfaces:**
- Consumes: everything from Task 11; `auth.VerifyPassword` (Task 2).
- Produces: `(*Server).handleLoginForm`, `(*Server).handleLoginSubmit`, `(*Server).handleLogout`, `(*Server).renderLogin`; `web.parseTemplates() (*template.Template, error)`. `dashboard.html` is created here but only rendered from Task 13.

- [ ] **Step 1: Write the failing test**

Create `internal/web/auth_handlers_test.go`:

```go
package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func postForm(t *testing.T, h http.Handler, path string, form url.Values, setup func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if setup != nil {
		setup(r)
	}
	return do(t, h, r)
}

func findCookie(res *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range res.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestLoginFormRenders(t *testing.T) {
	e := newTestEnv(t)
	res := do(t, e.handler, httptest.NewRequest(http.MethodGet, "/login", nil))

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	if !strings.Contains(res.Body.String(), `name="password"`) {
		t.Error("the login page has no password field")
	}
}

func TestLoginSucceeds(t *testing.T) {
	e := newTestEnv(t)
	res := postForm(t, e.handler, "/login", url.Values{"password": {testPassword}}, nil)

	if res.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", res.Code)
	}
	if loc := res.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want /", loc)
	}

	c := findCookie(res, SessionCookieName)
	if c == nil {
		t.Fatal("no session cookie was set")
	}
	if !c.HttpOnly {
		t.Error("the session cookie is not HttpOnly")
	}
	if !c.Secure {
		t.Error("the session cookie is not Secure")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", c.SameSite)
	}
	if c.Path != "/" {
		t.Errorf("Path = %q, want /", c.Path)
	}
	if _, err := e.srv.sessions.Verify(c.Value); err != nil {
		t.Errorf("the issued cookie does not verify: %v", err)
	}
}

func TestLoginFailsWithTheWrongPassword(t *testing.T) {
	e := newTestEnv(t)
	res := postForm(t, e.handler, "/login", url.Values{"password": {"wrong"}}, nil)

	if res.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", res.Code)
	}
	if findCookie(res, SessionCookieName) != nil {
		t.Error("a session cookie was set for a failed login")
	}
	if !strings.Contains(res.Body.String(), "Incorrect password") {
		t.Error("the failed login page does not say the password was wrong")
	}
}

func TestSixthFailedLoginIsRateLimited(t *testing.T) {
	e := newTestEnv(t)
	setIP := func(r *http.Request) { r.Header.Set("CF-Connecting-IP", "203.0.113.5") }

	for i := 1; i <= 5; i++ {
		res := postForm(t, e.handler, "/login", url.Values{"password": {"wrong"}}, setIP)
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i, res.Code)
		}
	}

	res := postForm(t, e.handler, "/login", url.Values{"password": {"wrong"}}, setIP)
	if res.Code != http.StatusTooManyRequests {
		t.Errorf("sixth attempt status = %d, want 429", res.Code)
	}
	if res.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After header on the rate-limited response")
	}
}

func TestRateLimitBlocksEvenTheCorrectPassword(t *testing.T) {
	e := newTestEnv(t)
	setIP := func(r *http.Request) { r.Header.Set("CF-Connecting-IP", "203.0.113.6") }
	for i := 0; i < 5; i++ {
		postForm(t, e.handler, "/login", url.Values{"password": {"wrong"}}, setIP)
	}

	res := postForm(t, e.handler, "/login", url.Values{"password": {testPassword}}, setIP)
	if res.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429 — the limiter must gate before verification", res.Code)
	}
}

func TestSuccessfulLoginClearsTheFailureCount(t *testing.T) {
	e := newTestEnv(t)
	setIP := func(r *http.Request) { r.Header.Set("CF-Connecting-IP", "203.0.113.7") }
	for i := 0; i < 4; i++ {
		postForm(t, e.handler, "/login", url.Values{"password": {"wrong"}}, setIP)
	}
	postForm(t, e.handler, "/login", url.Values{"password": {testPassword}}, setIP)

	for i := 0; i < 5; i++ {
		res := postForm(t, e.handler, "/login", url.Values{"password": {"wrong"}}, setIP)
		if res.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d was rate limited, so the counter was not reset on success", i+1)
		}
	}
}

func TestLoginPageRedirectsWhenAlreadySignedIn(t *testing.T) {
	e := newTestEnv(t)
	r := httptest.NewRequest(http.MethodGet, "/login", nil)
	r.AddCookie(e.sessionCookie(t))

	res := do(t, e.handler, r)
	if res.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", res.Code)
	}
}

func TestLogoutClearsTheCookie(t *testing.T) {
	e := newTestEnv(t)
	cookie := e.sessionCookie(t)
	token := e.csrfToken(t, cookie)

	res := postForm(t, e.handler, "/logout", url.Values{"csrf_token": {token}}, func(r *http.Request) {
		r.AddCookie(cookie)
	})

	if res.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", res.Code)
	}
	c := findCookie(res, SessionCookieName)
	if c == nil || c.MaxAge >= 0 {
		t.Error("logout did not expire the session cookie")
	}
}

func TestLogoutRequiresCSRF(t *testing.T) {
	e := newTestEnv(t)
	res := postForm(t, e.handler, "/logout", url.Values{}, func(r *http.Request) {
		r.AddCookie(e.sessionCookie(t))
	})
	if res.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", res.Code)
	}
}

func TestLoginIsExemptFromCSRF(t *testing.T) {
	e := newTestEnv(t)
	// No CSRF token anywhere: there is no session yet to derive one from.
	res := postForm(t, e.handler, "/login", url.Values{"password": {testPassword}}, nil)
	if res.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", res.Code)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/web/ -run Login -v`
Expected: FAIL — 404 responses, because `/login` is not routed yet.

- [ ] **Step 3: Write the templates**

Create `internal/web/templates/login.html`:

```html
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Shutdowner — Sign in</title>
  <link rel="stylesheet" href="/static/app.css">
</head>
<body class="login-page">
  <main class="card">
    <h1>Shutdowner</h1>
    {{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
    <form method="post" action="/login">
      <label for="password">Password</label>
      <input id="password" name="password" type="password" autocomplete="current-password" required autofocus>
      <button type="submit" class="primary">Sign in</button>
    </form>
  </main>
</body>
</html>
```

Create `internal/web/templates/dashboard.html`:

```html
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Shutdowner</title>
  <link rel="stylesheet" href="/static/app.css">
</head>
<body class="dashboard" data-csrf="{{.CSRFToken}}" data-delay="{{.DelaySeconds}}">
  <main class="card">
    <header class="status">
      <div class="titlebar">
        <h1 id="hostname">{{.Info.Hostname}}</h1>
        <span id="reachability" class="badge online">online</span>
      </div>
      <p id="os" class="muted">{{.Info.OS}}</p>
      <p class="muted"><span id="uptime"></span> · <span id="localtime"></span></p>
    </header>

    <section class="actions">
      <button class="action" data-action="shutdown">Shut down</button>
      <button class="action" data-action="restart">Restart</button>
      <button class="action" data-action="sleep"
        {{if not .Capabilities.Sleep}}disabled title="Sleep is not available on this system"{{end}}>Sleep</button>
      <button class="action" data-action="hibernate"
        {{if not .Capabilities.Hibernate}}disabled title="Hibernate is not enabled on this system"{{end}}>Hibernate</button>
    </section>

    <section class="pending hidden" id="pending">
      <p id="pending-text"></p>
      <button id="abort" class="primary">Abort</button>
    </section>

    <p class="error hidden" id="error" role="alert"></p>

    <form method="post" action="/logout" class="logout">
      <input type="hidden" name="csrf_token" value="{{.CSRFToken}}">
      <button type="submit" class="link">Sign out</button>
    </form>
  </main>

  <dialog id="confirm">
    <form method="dialog">
      <h2 id="confirm-title"></h2>
      <label class="checkbox"><input type="checkbox" id="graceful"> Close apps gracefully</label>
      <p class="hint">Leave unchecked to force apps closed. Unsaved work will be lost.</p>
      <menu>
        <button value="cancel">Cancel</button>
        <button value="confirm" class="danger">Confirm</button>
      </menu>
    </form>
  </dialog>

  <script src="/static/app.js"></script>
</body>
</html>
```

Create `internal/web/templates.go`:

```go
package web

import (
	"embed"
	"html/template"
)

//go:embed templates/*.html
var templateFS embed.FS

// parseTemplates loads every page template. Each page is standalone rather than
// composed from a shared layout: two pages do not justify the indirection.
func parseTemplates() (*template.Template, error) {
	return template.ParseFS(templateFS, "templates/*.html")
}
```

- [ ] **Step 4: Wire templates into the server**

Replace `internal/web/server.go` with:

```go
// Package web serves the login page, the dashboard and the JSON API.
package web

import (
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"

	"shutdowner/internal/action"
	"shutdowner/internal/auth"
	"shutdowner/internal/power"
)

const SessionCookieName = "shutdowner_session"

type Options struct {
	Sessions     *auth.SessionManager
	Limiter      *auth.Limiter
	Actions      *action.Manager
	Power        power.Controller
	Logger       *slog.Logger
	PasswordHash string
	DelaySeconds int
}

type Server struct {
	sessions     *auth.SessionManager
	limiter      *auth.Limiter
	actions      *action.Manager
	power        power.Controller
	logger       *slog.Logger
	tmpl         *template.Template
	passwordHash string
	delaySeconds int
}

func New(o Options) (*Server, error) {
	if o.Sessions == nil || o.Limiter == nil || o.Actions == nil || o.Power == nil || o.Logger == nil {
		return nil, errors.New("web: Sessions, Limiter, Actions, Power and Logger are all required")
	}
	if o.PasswordHash == "" {
		return nil, errors.New("web: PasswordHash is required")
	}
	tmpl, err := parseTemplates()
	if err != nil {
		return nil, fmt.Errorf("web: parsing templates: %w", err)
	}
	return &Server{
		sessions:     o.Sessions,
		limiter:      o.Limiter,
		actions:      o.Actions,
		power:        o.Power,
		logger:       o.Logger,
		tmpl:         tmpl,
		passwordHash: o.PasswordHash,
		delaySeconds: o.DelaySeconds,
	}, nil
}

// Routes builds the handler tree. Tasks 13 and 14 extend it.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLoginSubmit)
	mux.HandleFunc("POST /logout", s.requireSession(s.requireCSRF(s.handleLogout)))
	mux.HandleFunc("GET /healthz", s.handleHealth)
	return securityHeaders(recoverPanic(s.logger, mux))
}

// handleHealth is the one unauthenticated route, so the tunnel has something to
// probe.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}
```

- [ ] **Step 5: Write the handlers**

Create `internal/web/auth_handlers.go`:

```go
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
	}
	s.renderLogin(w, http.StatusOK, "")
}

// handleLoginSubmit is exempt from CSRF: no session exists yet to derive a
// token from, and forging a login for a single-user app gains the attacker a
// session as themselves.
func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	ip := ClientIP(r)

	// The limiter gates before verification, so a locked-out client cannot use
	// timing against the bcrypt comparison.
	if ok, retry := s.limiter.Allow(ip); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		s.logger.Warn("login rate limited", "ip", ip)
		s.renderLogin(w, http.StatusTooManyRequests,
			fmt.Sprintf("Too many failed attempts. Try again in %d minutes.", int(retry.Minutes())+1))
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
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./internal/web/ -v`
Expected: PASS — all Task 11 tests still green, plus the ten login and logout tests.

- [ ] **Step 7: Commit**

```bash
gofmt -w . && go test ./... && GOOS=windows GOARCH=amd64 go build ./...
git add internal/web/
git commit -m "feat: login page, logout and session issuance"
```

---

### Task 13: Dashboard and the JSON API

**Files:**
- Create: `internal/web/api_handlers.go`
- Modify: `internal/web/server.go` (extend `Routes`)
- Test: `internal/web/api_handlers_test.go`

**Interfaces:**
- Consumes: everything from Tasks 11-12; `action.Manager`, `action.Pending`, `action.State`, the four `action` sentinel errors (Task 9); `sysinfo.Collect` (Task 8); `power.Capabilities` (Task 6).
- Produces: `(*Server).handleDashboard`, `(*Server).handleStatus`, `(*Server).handleAction`, `(*Server).handleAbort`, `(*Server).capabilities`; JSON shapes `statusResponse`, `actionRequest{action, force}`, `abortRequest{id}`, `actionResponse{id, remainingSeconds}`.

- [ ] **Step 1: Write the failing test**

Create `internal/web/api_handlers_test.go`:

```go
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shutdowner/internal/power"
)

func postJSON(t *testing.T, e *testEnv, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	cookie := e.sessionCookie(t)
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", e.csrfToken(t, cookie))
	r.AddCookie(cookie)
	return do(t, e.handler, r)
}

func getJSON(t *testing.T, e *testEnv, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.AddCookie(e.sessionCookie(t))
	res := do(t, e.handler, r)

	var out map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding %s: %v (body %q)", path, err, res.Body.String())
	}
	return res, out
}

func TestStatusRequiresASession(t *testing.T) {
	e := newTestEnv(t)
	res := do(t, e.handler, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if res.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", res.Code)
	}
}

func TestStatusShapeWhenIdle(t *testing.T) {
	e := newTestEnv(t)
	res, body := getJSON(t, e, "/api/status")

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	for _, key := range []string{"hostname", "os", "uptimeSeconds", "localTime", "capabilities", "state"} {
		if _, ok := body[key]; !ok {
			t.Errorf("the response has no %q key: %v", key, body)
		}
	}
	if body["state"] != "idle" {
		t.Errorf("state = %v, want idle", body["state"])
	}
	if body["pending"] != nil {
		t.Errorf("pending = %v, want null when idle", body["pending"])
	}
}

func TestStatusReportsCapabilities(t *testing.T) {
	e := newTestEnv(t)
	e.fake.SetCapabilities(power.Capabilities{Sleep: true})

	_, body := getJSON(t, e, "/api/status")
	caps, ok := body["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities = %v, want an object", body["capabilities"])
	}
	if caps["sleep"] != true || caps["hibernate"] != false {
		t.Errorf("capabilities = %v, want sleep true and hibernate false", caps)
	}
}

func TestActionSchedulesWithoutExecutingYet(t *testing.T) {
	e := newTestEnv(t)
	res := postJSON(t, e, "/api/action", `{"action":"shutdown","force":true}`)

	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %q)", res.Code, res.Body.String())
	}
	var out struct {
		ID               string `json:"id"`
		RemainingSeconds int    `json:"remainingSeconds"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if out.ID == "" {
		t.Error("no action id returned")
	}
	if out.RemainingSeconds != 45 {
		t.Errorf("remainingSeconds = %d, want 45", out.RemainingSeconds)
	}
	if len(e.fake.Calls()) != 0 {
		t.Error("the action executed immediately instead of after the countdown")
	}
}

func TestSecondActionConflicts(t *testing.T) {
	e := newTestEnv(t)
	postJSON(t, e, "/api/action", `{"action":"shutdown","force":true}`)

	res := postJSON(t, e, "/api/action", `{"action":"restart","force":true}`)
	if res.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", res.Code)
	}
}

func TestUnavailableActionIsRejected(t *testing.T) {
	e := newTestEnv(t)
	e.fake.SetCapabilities(power.Capabilities{Sleep: true})

	res := postJSON(t, e, "/api/action", `{"action":"hibernate","force":false}`)
	if res.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.Code)
	}
}

func TestUnknownActionIsRejected(t *testing.T) {
	e := newTestEnv(t)
	res := postJSON(t, e, "/api/action", `{"action":"explode","force":true}`)
	if res.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.Code)
	}
}

func TestMalformedActionBodyIsRejected(t *testing.T) {
	e := newTestEnv(t)
	res := postJSON(t, e, "/api/action", `not json`)
	if res.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.Code)
	}
}

func TestActionRequiresCSRF(t *testing.T) {
	e := newTestEnv(t)
	r := httptest.NewRequest(http.MethodPost, "/api/action", strings.NewReader(`{"action":"shutdown"}`))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(e.sessionCookie(t))

	if res := do(t, e.handler, r); res.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", res.Code)
	}
}

func TestAbortCancelsThePendingAction(t *testing.T) {
	e := newTestEnv(t)
	sched := postJSON(t, e, "/api/action", `{"action":"shutdown","force":true}`)
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(sched.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	res := postJSON(t, e, "/api/abort", `{"id":"`+out.ID+`"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", res.Code, res.Body.String())
	}
	if s := e.actions.Status(); s.State != "idle" {
		t.Errorf("state = %q, want idle", s.State)
	}
}

func TestAbortWithAStaleIDConflicts(t *testing.T) {
	e := newTestEnv(t)
	postJSON(t, e, "/api/action", `{"action":"shutdown","force":true}`)

	res := postJSON(t, e, "/api/abort", `{"id":"from-an-old-tab"}`)
	if res.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", res.Code)
	}
	if s := e.actions.Status(); s.State != "pending" {
		t.Errorf("state = %q, want the original action still pending", s.State)
	}
}

func TestDashboardRedirectsWithoutASession(t *testing.T) {
	e := newTestEnv(t)
	res := do(t, e.handler, httptest.NewRequest(http.MethodGet, "/", nil))
	if res.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", res.Code)
	}
	if loc := res.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}

func TestDashboardRenders(t *testing.T) {
	e := newTestEnv(t)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(e.sessionCookie(t))
	res := do(t, e.handler, r)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	body := res.Body.String()
	for _, want := range []string{`data-action="shutdown"`, `data-action="restart"`, `data-action="sleep"`, `data-action="hibernate"`, `id="abort"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the dashboard does not contain %q", want)
		}
	}
}

func TestDashboardDisablesUnavailableActions(t *testing.T) {
	e := newTestEnv(t)
	e.fake.SetCapabilities(power.Capabilities{Sleep: true})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(e.sessionCookie(t))
	body := do(t, e.handler, r).Body.String()

	if !strings.Contains(body, "Hibernate is not enabled on this system") {
		t.Error("the hibernate button is not disabled with a reason when hibernation is unavailable")
	}
}

func TestCapabilitiesErrorDoesNotBreakTheDashboard(t *testing.T) {
	e := newTestEnv(t)
	// power.New() off Windows returns ErrUnsupported from Capabilities. The
	// dashboard must degrade to "nothing available" rather than 500.
	e.srv.power = power.New()

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(e.sessionCookie(t))
	if res := do(t, e.handler, r); res.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", res.Code)
	}

	if caps := e.srv.capabilities(context.Background()); caps.Sleep || caps.Hibernate {
		t.Errorf("capabilities = %+v, want both false on a controller error", caps)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/web/ -run 'Status|Action|Abort|Dashboard|Capabilities' -v`
Expected: FAIL — 404 for `/`, `/api/status`, `/api/action`, `/api/abort`.

- [ ] **Step 3: Write the handlers**

Create `internal/web/api_handlers.go`:

```go
package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"shutdowner/internal/action"
	"shutdowner/internal/power"
	"shutdowner/internal/sysinfo"
)

type dashboardPage struct {
	Info         sysinfo.Info
	Capabilities power.Capabilities
	CSRFToken    string
	DelaySeconds int
}

type statusResponse struct {
	sysinfo.Info
	Capabilities power.Capabilities `json:"capabilities"`
	State        action.State       `json:"state"`
	Pending      *action.Pending    `json:"pending"`
	Error        string             `json:"error"`
}

type actionRequest struct {
	Action power.Action `json:"action"`
	Force  bool         `json:"force"`
}

type actionResponse struct {
	ID               string `json:"id"`
	RemainingSeconds int    `json:"remainingSeconds"`
}

type abortRequest struct {
	ID string `json:"id"`
}

// capabilities never fails the request. A controller error means the optional
// actions are reported unavailable rather than the dashboard breaking, which
// also keeps a transient GetPwrCapabilities failure from taking the UI down.
func (s *Server) capabilities(ctx context.Context) power.Capabilities {
	caps, err := s.power.Capabilities(ctx)
	if err != nil {
		s.logger.Warn("reading power capabilities", "error", err)
		return power.Capabilities{}
	}
	return caps
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	page := dashboardPage{
		Info:         sysinfo.Collect(),
		Capabilities: s.capabilities(r.Context()),
		CSRFToken:    s.sessions.CSRFToken(nonceFrom(r.Context())),
		DelaySeconds: s.delaySeconds,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "dashboard.html", page); err != nil {
		s.logger.Error("rendering the dashboard", "error", err)
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st := s.actions.Status()
	writeJSON(w, http.StatusOK, statusResponse{
		Info:         sysinfo.Collect(),
		Capabilities: s.capabilities(r.Context()),
		State:        st.State,
		Pending:      st.Pending,
		Error:        st.Error,
	})
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	var req actionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "malformed request body")
		return
	}

	pending, err := s.actions.Schedule(r.Context(), req.Action, req.Force)
	if err != nil {
		switch {
		case errors.Is(err, action.ErrInvalidAction), errors.Is(err, action.ErrUnsupportedAction):
			writeJSONError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, action.ErrConflict):
			writeJSONError(w, http.StatusConflict, err.Error())
		default:
			s.logger.Error("scheduling an action", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "could not schedule the action")
		}
		return
	}

	s.logger.Info("action scheduled",
		"action", pending.Action, "force", pending.Force,
		"delaySeconds", pending.RemainingSeconds, "ip", ClientIP(r))
	writeJSON(w, http.StatusAccepted, actionResponse{
		ID:               pending.ID,
		RemainingSeconds: pending.RemainingSeconds,
	})
}

func (s *Server) handleAbort(w http.ResponseWriter, r *http.Request) {
	var req abortRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "malformed request body")
		return
	}
	if err := s.actions.Abort(req.ID); err != nil {
		writeJSONError(w, http.StatusConflict, err.Error())
		return
	}
	s.logger.Info("action aborted", "id", req.ID, "ip", ClientIP(r))
	writeJSON(w, http.StatusOK, map[string]string{"status": "aborted"})
}
```

- [ ] **Step 4: Extend the routes**

In `internal/web/server.go`, replace the `Routes` method with:

```go
// Routes builds the handler tree. Task 14 adds the static assets.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	// "/{$}" matches only the root path; a bare "/" would swallow every 404.
	mux.HandleFunc("GET /{$}", s.requireSession(s.handleDashboard))
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLoginSubmit)
	mux.HandleFunc("POST /logout", s.requireSession(s.requireCSRF(s.handleLogout)))
	mux.HandleFunc("GET /api/status", s.requireSession(s.handleStatus))
	mux.HandleFunc("POST /api/action", s.requireSession(s.requireCSRF(s.handleAction)))
	mux.HandleFunc("POST /api/abort", s.requireSession(s.requireCSRF(s.handleAbort)))
	mux.HandleFunc("GET /healthz", s.handleHealth)
	return securityHeaders(recoverPanic(s.logger, mux))
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/web/ -v`
Expected: PASS — every test from Tasks 11, 12 and 13.

- [ ] **Step 6: Commit**

```bash
gofmt -w . && go test ./... && GOOS=windows GOARCH=amd64 go build ./...
git add internal/web/
git commit -m "feat: dashboard page and the status, action and abort API"
```

---

### Task 14: Frontend assets

**Files:**
- Create: `internal/web/static.go`, `internal/web/static/app.css`, `internal/web/static/app.js`
- Modify: `internal/web/server.go` (add the static route)
- Test: `internal/web/static_test.go`

**Interfaces:**
- Consumes: the JSON API from Task 13; `data-csrf` and `data-delay` on `<body>` from Task 12's dashboard template.
- Produces: `web.staticHandler() (http.Handler, error)`; the `/static/` route.

- [ ] **Step 1: Write the failing test**

Create `internal/web/static_test.go`:

```go
package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStaticAssetsAreServed(t *testing.T) {
	e := newTestEnv(t)
	tests := []struct {
		path        string
		contentType string
		mustContain string
	}{
		{"/static/app.css", "text/css", ".action"},
		{"/static/app.js", "javascript", "/api/status"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			res := do(t, e.handler, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if res.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", res.Code)
			}
			if ct := res.Header().Get("Content-Type"); !strings.Contains(ct, tt.contentType) {
				t.Errorf("Content-Type = %q, want it to contain %q", ct, tt.contentType)
			}
			if !strings.Contains(res.Body.String(), tt.mustContain) {
				t.Errorf("%s does not contain %q", tt.path, tt.mustContain)
			}
		})
	}
}

func TestStaticAssetsNeedNoSession(t *testing.T) {
	e := newTestEnv(t)
	// Deliberately no cookie: the CSP references these files by URL, so gating
	// them behind auth would break the login page.
	res := do(t, e.handler, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))
	if res.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", res.Code)
	}
}

func TestMissingStaticAssetIs404(t *testing.T) {
	e := newTestEnv(t)
	res := do(t, e.handler, httptest.NewRequest(http.MethodGet, "/static/nope.css", nil))
	if res.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.Code)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/web/ -run Static -v`
Expected: FAIL — 404 for `/static/app.css`.

- [ ] **Step 3: Write the stylesheet**

Create `internal/web/static/app.css`:

```css
:root {
  color-scheme: light dark;
  --bg: #f4f5f7;
  --card: #ffffff;
  --fg: #16181d;
  --muted: #6b7280;
  --line: #e2e5ea;
  --accent: #2563eb;
  --danger: #b91c1c;
  --ok: #15803d;
}

@media (prefers-color-scheme: dark) {
  :root {
    --bg: #0f1115;
    --card: #171a21;
    --fg: #e8eaee;
    --muted: #9099a8;
    --line: #262b34;
    --accent: #60a5fa;
    --danger: #f87171;
    --ok: #4ade80;
  }
}

* { box-sizing: border-box; }

body {
  margin: 0;
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 1rem;
  background: var(--bg);
  color: var(--fg);
  font: 16px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif;
}

.card {
  width: 100%;
  max-width: 26rem;
  background: var(--card);
  border: 1px solid var(--line);
  border-radius: 14px;
  padding: 1.5rem;
}

h1 { font-size: 1.25rem; margin: 0; }
h2 { font-size: 1.05rem; margin: 0 0 0.75rem; }
p { margin: 0.25rem 0; }
.muted { color: var(--muted); font-size: 0.875rem; }
.hidden { display: none !important; }

.titlebar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.5rem;
}

.badge {
  font-size: 0.75rem;
  padding: 0.15rem 0.5rem;
  border-radius: 999px;
  border: 1px solid var(--line);
  color: var(--muted);
}
.badge.online { color: var(--ok); }
.badge.offline { color: var(--danger); }

.status { border-bottom: 1px solid var(--line); padding-bottom: 1rem; }

.actions {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 0.6rem;
  margin: 1rem 0;
}

/* Single column on narrow phones so every target stays comfortably tappable. */
@media (max-width: 24rem) {
  .actions { grid-template-columns: 1fr; }
}

button {
  font: inherit;
  color: var(--fg);
  background: transparent;
  border: 1px solid var(--line);
  border-radius: 10px;
  padding: 0.85rem 1rem;
  min-height: 3rem;
  cursor: pointer;
}
button:hover:not(:disabled) { border-color: var(--accent); }
button:disabled { opacity: 0.45; cursor: not-allowed; }

button.primary { background: var(--accent); border-color: var(--accent); color: #fff; }
button.danger { color: var(--danger); border-color: var(--danger); }
button.link {
  border: none;
  padding: 0;
  min-height: 0;
  color: var(--muted);
  text-decoration: underline;
}

.pending {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  border: 1px solid var(--accent);
  border-radius: 10px;
  padding: 0.75rem 1rem;
}

.error { color: var(--danger); font-size: 0.9rem; }
.logout { margin: 1rem 0 0; text-align: center; }

label { display: block; font-size: 0.875rem; color: var(--muted); margin-bottom: 0.35rem; }
label.checkbox { display: flex; align-items: center; gap: 0.5rem; color: var(--fg); font-size: 1rem; }

input[type="password"] {
  width: 100%;
  font: inherit;
  color: var(--fg);
  background: var(--bg);
  border: 1px solid var(--line);
  border-radius: 10px;
  padding: 0.75rem;
  margin-bottom: 1rem;
}

.login-page form button { width: 100%; }

dialog {
  border: 1px solid var(--line);
  border-radius: 14px;
  background: var(--card);
  color: var(--fg);
  padding: 1.25rem;
  max-width: 22rem;
}
dialog::backdrop { background: rgb(0 0 0 / 0.5); }
dialog .hint { color: var(--muted); font-size: 0.8rem; margin: 0.5rem 0 1rem; }
dialog menu { display: flex; justify-content: flex-end; gap: 0.5rem; margin: 0; padding: 0; }
```

- [ ] **Step 4: Write the client script**

Create `internal/web/static/app.js`:

```js
(function () {
  "use strict";

  var body = document.body;
  var csrf = body.dataset.csrf;
  var defaultDelay = parseInt(body.dataset.delay, 10) || 0;

  var LABELS = {
    shutdown: "Shut down",
    restart: "Restart",
    sleep: "Sleep",
    hibernate: "Hibernate"
  };

  function el(id) { return document.getElementById(id); }

  var state = {
    pending: null,     // { id, action, firesAtMs }
    firedAction: null, // the action whose countdown reached zero
    failedPolls: 0
  };

  async function post(path, payload) {
    var res = await fetch(path, {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
      body: JSON.stringify(payload)
    });
    if (res.status === 401) {
      window.location.href = "/login";
      throw new Error("signed out");
    }
    var data = {};
    try { data = await res.json(); } catch (e) { /* empty body is fine */ }
    if (!res.ok) throw new Error(data.error || "Request failed (" + res.status + ")");
    return data;
  }

  function showError(message) {
    var node = el("error");
    node.textContent = message || "";
    node.classList.toggle("hidden", !message);
  }

  function setReachability(cls, text) {
    var badge = el("reachability");
    badge.className = "badge " + cls;
    badge.textContent = text;
  }

  function formatUptime(seconds) {
    var d = Math.floor(seconds / 86400);
    var h = Math.floor((seconds % 86400) / 3600);
    var m = Math.floor((seconds % 3600) / 60);
    if (d > 0) return "up " + d + "d " + h + "h";
    if (h > 0) return "up " + h + "h " + m + "m";
    return "up " + m + "m";
  }

  function formatClock(iso) {
    var t = new Date(iso);
    if (isNaN(t.getTime())) return "";
    return t.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) + " local";
  }

  // The countdown is rendered from a locally computed deadline so it ticks
  // smoothly between the 3 second polls.
  function render() {
    var box = el("pending");
    if (!state.pending) {
      box.classList.add("hidden");
      return;
    }
    box.classList.remove("hidden");
    var left = Math.max(0, Math.round((state.pending.firesAtMs - Date.now()) / 1000));
    var label = LABELS[state.pending.action] || state.pending.action;
    el("pending-text").textContent = label + " in " + left + "s";
    if (left === 0) state.firedAction = state.pending.action;
  }

  function applyStatus(data) {
    state.failedPolls = 0;
    setReachability("online", "online");

    el("hostname").textContent = data.hostname;
    el("os").textContent = data.os;
    el("uptime").textContent = formatUptime(data.uptimeSeconds);
    el("localtime").textContent = formatClock(data.localTime);

    document.querySelectorAll(".action").forEach(function (b) {
      if (b.dataset.action === "sleep") b.disabled = !data.capabilities.sleep;
      if (b.dataset.action === "hibernate") b.disabled = !data.capabilities.hibernate;
    });

    if (data.state === "pending" && data.pending) {
      // Adopting the server's pending action is what makes an action scheduled
      // or aborted on another device show up here.
      state.pending = {
        id: data.pending.id,
        action: data.pending.action,
        firesAtMs: Date.now() + data.pending.remainingSeconds * 1000
      };
    } else {
      state.pending = null;
    }

    showError(data.state === "failed" ? data.error : "");
    render();
  }

  async function poll() {
    try {
      var res = await fetch("/api/status", { headers: { Accept: "application/json" } });
      if (res.status === 401) {
        window.location.href = "/login";
        return;
      }
      if (!res.ok) throw new Error("status " + res.status);
      applyStatus(await res.json());
    } catch (e) {
      state.failedPolls += 1;
      if (state.failedPolls < 2) return;
      // Once a shutdown or restart has fired, an unreachable PC is the
      // confirmation rather than an error.
      if (state.firedAction === "shutdown" || state.firedAction === "restart") {
        setReachability("offline", "PC is offline");
        state.pending = null;
        el("pending").classList.add("hidden");
      } else {
        setReachability("offline", "Cannot reach PC");
      }
    }
  }

  var dialog = el("confirm");
  var chosenAction = null;

  document.querySelectorAll(".action").forEach(function (button) {
    button.addEventListener("click", function () {
      chosenAction = button.dataset.action;
      var label = LABELS[chosenAction] || chosenAction;
      var suffix = defaultDelay > 0 ? " this PC in " + defaultDelay + "s?" : " this PC?";
      el("confirm-title").textContent = label + suffix;
      el("graceful").checked = false;
      dialog.showModal();
    });
  });

  dialog.addEventListener("close", async function () {
    if (dialog.returnValue !== "confirm" || !chosenAction) return;
    // Unchecked "close apps gracefully" means force, which is the default.
    var force = !el("graceful").checked;
    try {
      showError("");
      var data = await post("/api/action", { action: chosenAction, force: force });
      state.firedAction = null;
      state.pending = {
        id: data.id,
        action: chosenAction,
        firesAtMs: Date.now() + data.remainingSeconds * 1000
      };
      render();
    } catch (e) {
      showError(e.message);
    }
  });

  el("abort").addEventListener("click", async function () {
    if (!state.pending) return;
    try {
      await post("/api/abort", { id: state.pending.id });
      state.pending = null;
      state.firedAction = null;
      showError("");
      render();
    } catch (e) {
      showError(e.message);
    }
  });

  setInterval(render, 250);
  setInterval(poll, 3000);
  poll();
})();
```

- [ ] **Step 5: Serve the assets**

Create `internal/web/static.go`:

```go
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var staticFS embed.FS

// staticHandler serves the embedded CSS and JS. They are deliberately not
// behind the session gate: the login page references them too.
func staticHandler() (http.Handler, error) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}
	return http.StripPrefix("/static/", http.FileServerFS(sub)), nil
}
```

Replace `internal/web/server.go` with its final form:

```go
// Package web serves the login page, the dashboard and the JSON API.
package web

import (
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"

	"shutdowner/internal/action"
	"shutdowner/internal/auth"
	"shutdowner/internal/power"
)

const SessionCookieName = "shutdowner_session"

type Options struct {
	Sessions     *auth.SessionManager
	Limiter      *auth.Limiter
	Actions      *action.Manager
	Power        power.Controller
	Logger       *slog.Logger
	PasswordHash string
	DelaySeconds int
}

type Server struct {
	sessions     *auth.SessionManager
	limiter      *auth.Limiter
	actions      *action.Manager
	power        power.Controller
	logger       *slog.Logger
	tmpl         *template.Template
	static       http.Handler
	passwordHash string
	delaySeconds int
}

func New(o Options) (*Server, error) {
	if o.Sessions == nil || o.Limiter == nil || o.Actions == nil || o.Power == nil || o.Logger == nil {
		return nil, errors.New("web: Sessions, Limiter, Actions, Power and Logger are all required")
	}
	if o.PasswordHash == "" {
		return nil, errors.New("web: PasswordHash is required")
	}
	tmpl, err := parseTemplates()
	if err != nil {
		return nil, fmt.Errorf("web: parsing templates: %w", err)
	}
	static, err := staticHandler()
	if err != nil {
		return nil, fmt.Errorf("web: preparing static assets: %w", err)
	}
	return &Server{
		sessions:     o.Sessions,
		limiter:      o.Limiter,
		actions:      o.Actions,
		power:        o.Power,
		logger:       o.Logger,
		tmpl:         tmpl,
		static:       static,
		passwordHash: o.PasswordHash,
		delaySeconds: o.DelaySeconds,
	}, nil
}

// Routes builds the handler tree.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	// "/{$}" matches only the root path; a bare "/" would swallow every 404.
	mux.HandleFunc("GET /{$}", s.requireSession(s.handleDashboard))
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLoginSubmit)
	mux.HandleFunc("POST /logout", s.requireSession(s.requireCSRF(s.handleLogout)))
	mux.HandleFunc("GET /api/status", s.requireSession(s.handleStatus))
	mux.HandleFunc("POST /api/action", s.requireSession(s.requireCSRF(s.handleAction)))
	mux.HandleFunc("POST /api/abort", s.requireSession(s.requireCSRF(s.handleAbort)))
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.Handle("GET /static/", s.static)
	return securityHeaders(recoverPanic(s.logger, mux))
}

// handleHealth is the one unauthenticated route, so the tunnel has something to
// probe.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./internal/web/ -v`
Expected: PASS — every web test including the three static asset tests.

- [ ] **Step 7: Commit**

```bash
gofmt -w . && go test ./... && GOOS=windows GOARCH=amd64 go build ./...
git add internal/web/
git commit -m "feat: dashboard stylesheet and client script"
```

---

### Task 15: Windows service wrapper

**Files:**
- Create: `internal/winsvc/winsvc.go`, `internal/winsvc/winsvc_windows.go`, `internal/winsvc/winsvc_other.go`
- Test: `internal/winsvc/winsvc_other_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `winsvc.ServiceName = "Shutdowner"`; `winsvc.ErrWindowsOnly`; `winsvc.Run(logger *slog.Logger, start func() error, stop func() error) error`; `winsvc.Install(exePath string, args ...string) error`; `winsvc.Uninstall() error`; `winsvc.ReportError(msg string)`.

The package deliberately owns the whole service-control-manager interaction, including deciding whether this process is running under the SCM. That keeps `cmd/shutdowner` free of build tags: it calls `winsvc.Run` unconditionally and gets foreground execution off Windows.

A `!windows` file is mandatory here, not optional. A package whose every file carries `//go:build windows` fails on Linux with "build constraints exclude all Go files in ...".

- [ ] **Step 1: Write the failing test**

Create `internal/winsvc/winsvc_other_test.go`:

```go
//go:build !windows

package winsvc

import (
	"errors"
	"io"
	"log/slog"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRunCallsStartInTheForeground(t *testing.T) {
	called := false
	err := Run(discardLogger(), func() error {
		called = true
		return nil
	}, func() error { return nil })

	if err != nil {
		t.Errorf("Run() error = %v, want nil", err)
	}
	if !called {
		t.Error("Run() did not call start")
	}
}

func TestRunPropagatesTheStartError(t *testing.T) {
	boom := errors.New("bind: address already in use")
	err := Run(discardLogger(), func() error { return boom }, func() error { return nil })
	if !errors.Is(err, boom) {
		t.Errorf("Run() error = %v, want the start error", err)
	}
}

func TestInstallAndUninstallAreWindowsOnly(t *testing.T) {
	if err := Install("/tmp/shutdowner"); !errors.Is(err, ErrWindowsOnly) {
		t.Errorf("Install() error = %v, want ErrWindowsOnly", err)
	}
	if err := Uninstall(); !errors.Is(err, ErrWindowsOnly) {
		t.Errorf("Uninstall() error = %v, want ErrWindowsOnly", err)
	}
}

func TestReportErrorIsSafeOffWindows(t *testing.T) {
	// Must not panic; there is no Windows event log to write to.
	ReportError("something went wrong")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/winsvc/ -v`
Expected: FAIL — no non-test Go files, then `undefined: Run` once a file exists.

- [ ] **Step 3: Write the shared declarations**

Create `internal/winsvc/winsvc.go` (no build tag — shared by both platforms):

```go
// Package winsvc hosts Shutdowner under the Windows service control manager and
// installs or removes the service. It owns the "am I a service?" decision so
// that cmd/shutdowner needs no build tags of its own.
package winsvc

import "errors"

const (
	ServiceName = "Shutdowner"
	displayName = "Shutdowner remote power control"
	description = "Serves the Shutdowner web UI for remote shutdown, restart, sleep and hibernate."
)

// ErrWindowsOnly is returned by Install and Uninstall on other platforms.
var ErrWindowsOnly = errors.New("winsvc: service management is only available on Windows")
```

- [ ] **Step 4: Write the non-Windows implementation**

Create `internal/winsvc/winsvc_other.go`:

```go
//go:build !windows

package winsvc

import "log/slog"

// Run executes start in the foreground. There is no service control manager to
// report to off Windows.
func Run(_ *slog.Logger, start func() error, _ func() error) error {
	return start()
}

func Install(string, ...string) error { return ErrWindowsOnly }

func Uninstall() error { return ErrWindowsOnly }

// ReportError is a no-op off Windows.
func ReportError(string) {}
```

- [ ] **Step 5: Write the Windows implementation**

Create `internal/winsvc/winsvc_windows.go`:

```go
//go:build windows

package winsvc

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

// Run hosts start under the service control manager when this process was
// launched by it, and runs it in the foreground otherwise, so the same binary
// works as a service and from a console.
func Run(logger *slog.Logger, start func() error, stop func() error) error {
	isSvc, err := svc.IsWindowsService()
	if err != nil {
		return fmt.Errorf("determining the service context: %w", err)
	}
	if !isSvc {
		return start()
	}
	return svc.Run(ServiceName, &handler{logger: logger, start: start, stop: stop})
}

type handler struct {
	logger *slog.Logger
	start  func() error
	stop   func() error
}

func (h *handler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown

	status <- svc.Status{State: svc.StartPending}
	errc := make(chan error, 1)
	go func() { errc <- h.start() }()
	status <- svc.Status{State: svc.Running, Accepts: accepted}

	for {
		select {
		case err := <-errc:
			// The server stopped on its own, which for a service is a failure
			// unless something asked it to.
			if err != nil {
				h.logger.Error("server stopped unexpectedly", "error", err)
				status <- svc.Status{State: svc.StopPending}
				return true, 1
			}
			status <- svc.Status{State: svc.StopPending}
			return false, 0

		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				// svc.Shutdown also arrives when this app shuts the machine
				// down, so the graceful path runs either way.
				status <- svc.Status{State: svc.StopPending}
				if err := h.stop(); err != nil {
					h.logger.Error("stopping the server", "error", err)
				}
				return false, 0
			}
		}
	}
}

func Install(exePath string, args ...string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connecting to the service manager (run this from an elevated prompt): %w", err)
	}
	defer m.Disconnect()

	if s, err := m.OpenService(ServiceName); err == nil {
		s.Close()
		return fmt.Errorf("service %q already exists; run --uninstall-service first", ServiceName)
	}

	s, err := m.CreateService(ServiceName, exePath, mgr.Config{
		DisplayName: displayName,
		Description: description,
		StartType:   mgr.StartAutomatic,
	}, args...)
	if err != nil {
		return fmt.Errorf("creating the service: %w", err)
	}
	defer s.Close()

	// Restart on failure, so a crash does not silently leave the PC unreachable.
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}, uint32(24*time.Hour/time.Second)); err != nil {
		return fmt.Errorf("setting recovery actions: %w", err)
	}

	// The event log source is how startup failures become visible: a service
	// that dies before it can open its log file has nowhere else to report.
	if err := eventlog.InstallAsEventCreate(ServiceName, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil &&
		!strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("registering the event log source: %w", err)
	}

	if err := s.Start(); err != nil {
		return fmt.Errorf("starting the service: %w", err)
	}
	return nil
}

func Uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connecting to the service manager (run this from an elevated prompt): %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(ServiceName)
	if err != nil {
		return fmt.Errorf("service %q is not installed", ServiceName)
	}
	defer s.Close()

	// Best effort: a service that will not stop can still be marked for
	// deletion and disappears at the next reboot.
	_, _ = s.Control(svc.Stop)
	waitForStop(s)

	if err := s.Delete(); err != nil {
		return fmt.Errorf("deleting the service: %w", err)
	}
	_ = eventlog.Remove(ServiceName)
	return nil
}

func waitForStop(s *mgr.Service) {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		status, err := s.Query()
		if err != nil || status.State == svc.Stopped {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// ReportError writes to the Windows event log. It is best effort: it is called
// on startup failure, which is exactly when the normal log file may be
// unavailable.
func ReportError(msg string) {
	l, err := eventlog.Open(ServiceName)
	if err != nil {
		return
	}
	defer l.Close()
	_ = l.Error(1, msg)
}
```

- [ ] **Step 6: Run the test to verify it passes**

```bash
go test ./internal/winsvc/ -v
GOOS=windows GOARCH=amd64 go build ./...
GOOS=windows GOARCH=amd64 go vet ./...
```

Expected: PASS on Linux, and both Windows commands silent.

Note: `svc.ErrAlreadyStopped` does not exist in `golang.org/x/sys` v0.47.0 — verified by grepping the module cache. `Uninstall` therefore treats the stop as best effort rather than branching on that error, which is why `errors` is not imported here.

- [ ] **Step 7: Commit**

```bash
gofmt -w . && go test ./...
git add internal/winsvc/
git commit -m "feat: Windows service host, installer and event log reporting"
```

---

### Task 16: The binary, install docs and manual checklist

**Files:**
- Create: `cmd/shutdowner/main.go`, `cmd/shutdowner/app.go`, `cmd/shutdowner/secret_windows.go`, `cmd/shutdowner/secret_other.go`
- Modify: `README.md`
- Test: `cmd/shutdowner/main_test.go`

**Interfaces:**
- Consumes: `config.Load`, `config.ExeDir` (Task 1); `auth.HashPassword`, `auth.NewSessionManager`, `auth.NewLimiter`, `auth.Default*` (Tasks 2-5); `power.New`, `power.NewFake` (Tasks 6-7); `action.New` (Task 9); `logging.NewRotatingWriter`, `logging.DefaultMaxBytes`, `logging.DefaultKeep` (Task 10); `web.New`, `web.Options`, `(*web.Server).Routes` (Tasks 11-14); `winsvc.Run`, `winsvc.Install`, `winsvc.Uninstall`, `winsvc.ReportError` (Task 15).
- Produces: the `shutdowner` binary and its flags.

- [ ] **Step 1: Write the failing test**

Create `cmd/shutdowner/main_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/joho/godotenv"

	"shutdowner/internal/config"
)

var secretPattern = regexp.MustCompile(`SHUTDOWNER_SESSION_SECRET=([0-9a-f]{64})`)

func TestWriteStarterEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := writeStarterEnv(path); err != nil {
		t.Fatalf("writeStarterEnv() error = %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !secretPattern.Match(b) {
		t.Errorf("the generated .env has no 64-character hex secret:\n%s", b)
	}
	if !strings.Contains(string(b), "SHUTDOWNER_PASSWORD_HASH=") {
		t.Error("the generated .env has no SHUTDOWNER_PASSWORD_HASH line")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions = %o, want 600 — the file holds a session secret", perm)
	}
}

func TestWriteStarterEnvRefusesToOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := writeStarterEnv(path); err != nil {
		t.Fatalf("first writeStarterEnv() error = %v", err)
	}
	err := writeStarterEnv(path)
	if err == nil {
		t.Fatal("writeStarterEnv() overwrote an existing file, want an error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %q, want it to mention that the file already exists", err)
	}
}

func TestStarterEnvSecretsAreDistinct(t *testing.T) {
	read := func() string {
		path := filepath.Join(t.TempDir(), ".env")
		if err := writeStarterEnv(path); err != nil {
			t.Fatalf("writeStarterEnv() error = %v", err)
		}
		b, _ := os.ReadFile(path)
		return string(secretPattern.FindSubmatch(b)[1])
	}
	if read() == read() {
		t.Error("two generated .env files share a session secret")
	}
}

func TestGeneratedEnvParsesOnceAHashIsAdded(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := writeStarterEnv(path); err != nil {
		t.Fatalf("writeStarterEnv() error = %v", err)
	}

	env, err := godotenv.Read(path)
	if err != nil {
		t.Fatalf("godotenv.Read() error = %v", err)
	}
	env["SHUTDOWNER_PASSWORD_HASH"] = "$2a$12$" + strings.Repeat("a", 53)

	if _, err := config.Parse(env, false); err != nil {
		t.Errorf("config.Parse() on the generated .env error = %v, want nil", err)
	}
}

func TestNewLoggerWritesToTheConfiguredFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	logger, closeLog, err := newLogger(path, false)
	if err != nil {
		t.Fatalf("newLogger() error = %v", err)
	}
	logger.Info("hello", "key", "value")
	closeLog()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(b), "hello") {
		t.Errorf("log file = %q, want it to contain the logged message", b)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/shutdowner/ -v`
Expected: FAIL — `undefined: writeStarterEnv`, `undefined: newLogger`.

- [ ] **Step 3: Write the app lifecycle**

Create `cmd/shutdowner/app.go`:

```go
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// App is the runnable server, expressed as a start/stop pair so winsvc can
// drive it either in the foreground or under the service control manager.
type App struct {
	http   *http.Server
	logger *slog.Logger
}

// Start blocks until the server stops. A shutdown requested through Stop
// returns nil rather than ErrServerClosed, so the service reports success.
func (a *App) Start() error {
	a.logger.Info("listening", "addr", a.http.Addr)
	if err := a.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (a *App) Stop() error {
	a.logger.Info("stopping")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return a.http.Shutdown(ctx)
}
```

- [ ] **Step 4: Write the entrypoint**

Create `cmd/shutdowner/main.go`:

```go
// Command shutdowner serves a password-protected web UI for remotely shutting
// down, restarting, sleeping and hibernating the machine it runs on.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"shutdowner/internal/action"
	"shutdowner/internal/auth"
	"shutdowner/internal/config"
	"shutdowner/internal/logging"
	"shutdowner/internal/power"
	"shutdowner/internal/web"
	"shutdowner/internal/winsvc"
)

var (
	flagConfig          = flag.String("config", "", "path to .env (default: beside the executable)")
	flagConsole         = flag.Bool("console", false, "log to stdout instead of the log file")
	flagFakePower       = flag.Bool("fake-power", false, "development: log actions instead of executing them")
	flagAllowPublicBind = flag.Bool("allow-public-bind", false, "permit a non-loopback listen address")
	flagInit            = flag.Bool("init", false, "write a starter .env and exit; refuses to overwrite")
	flagHashPassword    = flag.Bool("hash-password", false, "prompt for a password, print its hash, and exit")
	flagInstall         = flag.Bool("install-service", false, "install the Windows service and exit")
	flagUninstall       = flag.Bool("uninstall-service", false, "remove the Windows service and exit")
)

const minPasswordLength = 12

const starterEnv = `# Generate with: shutdowner.exe --hash-password
SHUTDOWNER_PASSWORD_HASH=

# Generated by --init. Changing it signs every device out.
SHUTDOWNER_SESSION_SECRET=%s

# Must be a loopback address unless --allow-public-bind is passed.
SHUTDOWNER_LISTEN=127.0.0.1:8080

# Seconds between confirming an action and executing it. 0-3600.
SHUTDOWNER_DELAY_SECONDS=45

# How long a login lasts.
SHUTDOWNER_SESSION_TTL=168h

# Defaults to shutdowner.log beside the executable.
SHUTDOWNER_LOG_FILE=
`

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "shutdowner:", err)
		// A service that dies at startup has no console, so the event log is
		// the only place this becomes visible.
		winsvc.ReportError("shutdowner failed to start: " + err.Error())
		os.Exit(1)
	}
}

func run() error {
	switch {
	case *flagInit:
		return runInit()
	case *flagHashPassword:
		return runHashPassword()
	case *flagInstall:
		return runInstall()
	case *flagUninstall:
		return runUninstall()
	}
	return runServer()
}

func configPath() (string, error) {
	if *flagConfig != "" {
		return *flagConfig, nil
	}
	dir, err := config.ExeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ".env"), nil
}

func runInit() error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := writeStarterEnv(path); err != nil {
		return err
	}
	fmt.Printf("Wrote %s\nNext: run --hash-password and paste the result into SHUTDOWNER_PASSWORD_HASH.\n", path)
	return nil
}

// writeStarterEnv creates a .env with a fresh session secret. It refuses to
// overwrite, because regenerating the secret silently would sign every device
// out with no explanation.
func writeStarterEnv(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists; delete it first if you mean to regenerate it", path)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return fmt.Errorf("generating a session secret: %w", err)
	}
	content := fmt.Sprintf(starterEnv, hex.EncodeToString(secret))
	return os.WriteFile(path, []byte(content), 0o600)
}

func runHashPassword() error {
	first, err := readSecret("Password: ")
	if err != nil {
		return err
	}
	if len(first) < minPasswordLength {
		return fmt.Errorf("the password must be at least %d characters: it is the only thing between the internet and your power button", minPasswordLength)
	}
	second, err := readSecret("Repeat:   ")
	if err != nil {
		return err
	}
	if first != second {
		return errors.New("the two passwords do not match")
	}

	hash, err := auth.HashPassword(first)
	if err != nil {
		return fmt.Errorf("hashing the password: %w", err)
	}
	fmt.Printf("\nPaste this line into your .env:\n\nSHUTDOWNER_PASSWORD_HASH=%s\n", hash)
	return nil
}

func runInstall() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := winsvc.Install(exe); err != nil {
		return err
	}
	fmt.Printf("Installed and started the %s service.\n", winsvc.ServiceName)
	return nil
}

func runUninstall() error {
	if err := winsvc.Uninstall(); err != nil {
		return err
	}
	fmt.Printf("Removed the %s service.\n", winsvc.ServiceName)
	return nil
}

func runServer() error {
	cfg, err := config.Load(*flagConfig, *flagAllowPublicBind)
	if err != nil {
		return err
	}

	logger, closeLog, err := newLogger(cfg.LogFile, *flagConsole)
	if err != nil {
		return err
	}
	defer closeLog()

	var ctrl power.Controller = power.New()
	if *flagFakePower {
		logger.Warn("running with --fake-power: no real power action will be taken")
		ctrl = power.NewFake()
	}

	srv, err := web.New(web.Options{
		Sessions:     auth.NewSessionManager(cfg.SessionSecret, cfg.SessionTTL),
		Limiter:      auth.NewLimiter(auth.DefaultPerIPLimit, auth.DefaultGlobalLimit, auth.DefaultWindow),
		Actions:      action.New(ctrl, cfg.Delay),
		Power:        ctrl,
		Logger:       logger,
		PasswordHash: cfg.PasswordHash,
		DelaySeconds: int(cfg.Delay / time.Second),
	})
	if err != nil {
		return err
	}

	app := &App{
		logger: logger,
		http: &http.Server{
			Addr:    cfg.Listen,
			Handler: srv.Routes(),
			// The tunnel is the only client, but a slow-header attack would
			// still tie up connections without this.
			ReadHeaderTimeout: 10 * time.Second,
		},
	}

	logger.Info("starting",
		"listen", cfg.Listen,
		"delaySeconds", int(cfg.Delay/time.Second),
		"sessionTTL", cfg.SessionTTL.String())

	return winsvc.Run(logger, app.Start, app.Stop)
}

// newLogger returns a logger and a close function. A service has no console, so
// off --console everything goes to the rotating log file.
func newLogger(path string, console bool) (*slog.Logger, func(), error) {
	if console {
		h := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
		return slog.New(h), func() {}, nil
	}
	w, err := logging.NewRotatingWriter(path, logging.DefaultMaxBytes, logging.DefaultKeep)
	if err != nil {
		return nil, nil, err
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})
	return slog.New(h), func() { _ = w.Close() }, nil
}
```

- [ ] **Step 5: Write the two password readers**

Create `cmd/shutdowner/secret_windows.go`:

```go
//go:build windows

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// readSecret disables console echo through the Win32 console API rather than
// pulling in golang.org/x/term, which would be a fourth dependency.
func readSecret(prompt string) (string, error) {
	fmt.Print(prompt)

	h := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err == nil {
		_ = windows.SetConsoleMode(h, mode&^windows.ENABLE_ECHO_INPUT)
		defer func() { _ = windows.SetConsoleMode(h, mode) }()
	}

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	fmt.Println()
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
```

Create `cmd/shutdowner/secret_other.go`:

```go
//go:build !windows

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// readSecret echoes off Windows. --hash-password is a Windows operation; this
// exists only so the command is usable during development.
func readSecret(prompt string) (string, error) {
	fmt.Print(prompt + "(input is visible on this platform) ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
```

- [ ] **Step 6: Run the test to verify it passes**

```bash
go test ./... -v
GOOS=windows GOARCH=amd64 go build ./...
GOOS=windows GOARCH=amd64 go vet ./...
make build-windows
```

Expected: every package passes; `dist/shutdowner.exe` is produced.

- [ ] **Step 7: Exercise the whole app on Linux**

```bash
go run ./cmd/shutdowner --init --config ./.env.dev
go run ./cmd/shutdowner --hash-password
# paste the printed line into .env.dev, replacing the empty SHUTDOWNER_PASSWORD_HASH
go run ./cmd/shutdowner --console --fake-power --config ./.env.dev
```

Then in another shell:

```bash
curl -i http://127.0.0.1:8080/healthz
curl -i http://127.0.0.1:8080/          # expect 302 to /login
```

Open `http://127.0.0.1:8080/` in a browser, sign in, start a shutdown, watch the countdown, and press Abort. With `--fake-power` nothing real happens; the console log records the scheduled action. This is the end-to-end check that everything except the syscalls works before an `.exe` reaches Windows.

- [ ] **Step 8: Write the README**

Replace `README.md` with:

````markdown
# Shutdowner

Shut down, restart, sleep or hibernate a Windows PC from a web page, over the
internet, behind a password.

A single Go binary runs as a Windows service on the target PC and serves a small
web UI. Cloudflare Tunnel puts it on a subdomain without opening any port on
your router: the app itself only ever listens on `127.0.0.1`.

Waking the machine is out of scope — the app runs on the PC it controls, so it
cannot start one that is already off.

## Build

```
make build-windows      # produces dist/shutdowner.exe
make test               # runs the suite on any platform
```

## Install on the Windows PC

1. Copy `shutdowner.exe` to `C:\Program Files\Shutdowner\`.
2. Open an **elevated** Command Prompt in that directory.
3. `shutdowner.exe --init` — writes `.env` with a fresh session secret.
4. `shutdowner.exe --hash-password` — type a long password twice, then paste the
   printed line into `.env` as `SHUTDOWNER_PASSWORD_HASH`.
5. `shutdowner.exe --install-service` — installs, sets auto-start and
   restart-on-failure, and starts the service.
6. Install [cloudflared](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/),
   create a tunnel, and route your subdomain to `http://127.0.0.1:8080`.

To remove it: `shutdowner.exe --uninstall-service`.

## Configuration

`.env` lives beside the executable. Paths are resolved from the executable's
location, never the working directory, because a service starts in
`C:\Windows\System32`.

| Key | Default | Meaning |
|---|---|---|
| `SHUTDOWNER_PASSWORD_HASH` | — | Required. bcrypt hash from `--hash-password`. |
| `SHUTDOWNER_SESSION_SECRET` | — | Required. 32 random bytes, hex. Changing it signs every device out. |
| `SHUTDOWNER_LISTEN` | `127.0.0.1:8080` | Must be loopback unless `--allow-public-bind`. |
| `SHUTDOWNER_DELAY_SECONDS` | `45` | Countdown before an action fires. 0-3600. |
| `SHUTDOWNER_SESSION_TTL` | `168h` | How long a login lasts. |
| `SHUTDOWNER_LOG_FILE` | `shutdowner.log` beside the exe | Rotates at 5 MB, keeps 2. |

## Command line

```
shutdowner.exe                      run; detects service vs console context
shutdowner.exe --init               write a starter .env; refuses to overwrite
shutdowner.exe --hash-password      prompt for a password, print its hash
shutdowner.exe --install-service    install and start the service
shutdowner.exe --uninstall-service  stop and remove the service
shutdowner.exe --console            run in the foreground, log to stdout
shutdowner.exe --config PATH        alternate .env location
shutdowner.exe --allow-public-bind  permit a non-loopback listen address
shutdowner.exe --fake-power         development only: log actions, do not execute
```

## How it behaves

- Clicking an action opens a confirm dialog, then starts a countdown you can
  abort. The countdown is owned by the app, not by `shutdown /t`, because
  Windows cannot cancel a pending sleep or hibernate — this way Abort works
  identically for all four actions.
- "Close apps gracefully" is **unchecked** by default, meaning `/f` is passed.
  Without it, an app with unsaved work blocks the shutdown and the PC silently
  stays on. Unsaved work is lost when forcing.
- Hibernate is disabled in the UI when the machine does not support it, which is
  common after `powercfg /h off`.
- After a shutdown fires, the page shows "PC is offline" rather than a
  connection error. Being unreachable is the confirmation.

## Security notes

- The password is stored hashed. That does not protect against someone who can
  already read `.env` — they own the machine — but it does protect against the
  leak paths that actually happen: screenshots, backups, an accidental commit.
- Sessions are stateless signed cookies, so they survive the reboots this app
  exists to perform.
- Five failed logins per IP per 15 minutes, and 20 globally, then HTTP 429.
- The listener refuses a non-loopback address unless you explicitly opt in.

## Verifying a real install

Automated tests cover everything except the Win32 calls and the service
wrapper, which cannot run off Windows. After installing, walk this list:

1. `--install-service`, reboot the PC, and confirm the service is running before
   anyone logs in.
2. Reach the subdomain from a phone on cellular with Wi-Fi off.
3. Six wrong passwords in a row produce a rate-limit message.
4. Each of the four actions completes.
5. Abort cancels each of the four actions.
6. With `powercfg /h off`, the hibernate button is disabled and the API rejects
   the action.
7. With an unsaved Notepad open: a forced shutdown completes; a graceful one is
   blocked by Windows and the PC is still up afterwards.
8. `shutdowner.log` is written, and rotates once it passes 5 MB.
````

- [ ] **Step 9: Final verification**

```bash
gofmt -l .          # must print nothing
go vet ./...
GOOS=windows GOARCH=amd64 go vet ./...
go test -race ./...
make build-windows
```

Expected: no output from `gofmt -l`, both vets clean, all tests pass under the race detector, and `dist/shutdowner.exe` builds.

- [ ] **Step 10: Commit**

```bash
git add cmd/ README.md
git commit -m "feat: shutdowner binary, CLI commands and install documentation"
```

---

## Done

At this point the app is complete: `make build-windows` produces a single
`.exe`, `go test ./...` covers every layer above the syscalls, and the README
carries the install steps and the manual checklist for the parts a Linux machine
cannot exercise.
