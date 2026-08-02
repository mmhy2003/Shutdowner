package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/joho/godotenv"

	"shutdowner/internal/config"
)

var secretPattern = regexp.MustCompile(`SHUTDOWNER_SESSION_SECRET='([0-9a-f]{64})'`)

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
		t.Errorf("permissions = %o, want 600 — note this is only enforced on Unix; see the README on Windows ACLs", perm)
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

// withFlags sets the package-level flags for one test and restores them, since
// they are process-wide state shared with every other test in this package.
func withFlags(t *testing.T, cfg string, allowPublicBind bool) {
	t.Helper()
	oldConfig, oldBind := *flagConfig, *flagAllowPublicBind
	t.Cleanup(func() {
		*flagConfig, *flagAllowPublicBind = oldConfig, oldBind
	})
	*flagConfig, *flagAllowPublicBind = cfg, allowPublicBind
}

func TestServiceArgsAreEmptyWithNoFlags(t *testing.T) {
	withFlags(t, "", false)
	args, err := serviceArgs()
	if err != nil {
		t.Fatalf("serviceArgs() error = %v", err)
	}
	if len(args) != 0 {
		t.Errorf("args = %v, want none when no flags were passed", args)
	}
}

func TestServiceArgsMakeConfigAbsolute(t *testing.T) {
	// A relative path is exactly the case that breaks: the service starts in
	// C:\Windows\System32, so it would resolve against the wrong directory.
	withFlags(t, filepath.Join("cfg", ".env"), false)

	args, err := serviceArgs()
	if err != nil {
		t.Fatalf("serviceArgs() error = %v", err)
	}
	if len(args) != 2 || args[0] != "--config" {
		t.Fatalf("args = %v, want [--config <path>]", args)
	}
	if !filepath.IsAbs(args[1]) {
		t.Errorf("--config = %q, want an absolute path", args[1])
	}
	want, err := filepath.Abs(filepath.Join("cfg", ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if args[1] != want {
		t.Errorf("--config = %q, want %q", args[1], want)
	}
}

func TestServiceArgsIncludeAllowPublicBindOnlyWhenSet(t *testing.T) {
	withFlags(t, "", false)
	args, err := serviceArgs()
	if err != nil {
		t.Fatalf("serviceArgs() error = %v", err)
	}
	if slices.Contains(args, "--allow-public-bind") {
		t.Errorf("args = %v, want no --allow-public-bind when the flag is unset", args)
	}

	withFlags(t, "", true)
	args, err = serviceArgs()
	if err != nil {
		t.Fatalf("serviceArgs() error = %v", err)
	}
	if !slices.Contains(args, "--allow-public-bind") {
		t.Errorf("args = %v, want --allow-public-bind forwarded when the flag is set", args)
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
