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
		return nil, errors.New("SHUTDOWNER_PASSWORD_HASH is not a bcrypt hash; generate one with " +
			"--hash-password and keep the single quotes around it, since an unquoted value has its " +
			"$ sequences stripped")
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
