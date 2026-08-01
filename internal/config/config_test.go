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
