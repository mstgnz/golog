package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds the application configuration.
type Config struct {
	// DatabaseURL, when set, is used as the DSN and the DB_* fields are ignored.
	DatabaseURL string
	DBHost      string
	DBPort      string
	DBUser      string
	DBPassword  string
	DBName      string
	DBSSLMode   string

	Port               int
	CORSAllowedOrigins []string
	AutoMigrate        bool

	// WriteToken guards POST /api/logs*; ReadToken guards reads and the
	// dashboard. Empty means open.
	WriteToken string
	ReadToken  string
	// SecureCookies forces the Secure flag on the dashboard session cookie,
	// for deployments where TLS ends at a proxy.
	SecureCookies bool
	// RetentionDays > 0 deletes logs older than that many days.
	RetentionDays int
}

// sslModes are the values lib/pq accepts.
var sslModes = map[string]bool{"disable": true, "require": true, "verify-ca": true, "verify-full": true}

// MinTokenLength keeps tokens out of brute-force range.
const MinTokenLength = 16

// maxRetentionDays is 100 years; anything larger is a typo.
const maxRetentionDays = 36500

// Load reads an optional .env file from the working directory, then the
// environment. Variables already set in the environment win over .env.
func Load() (*Config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read .env: %w", err)
	}
	return fromEnv(os.LookupEnv)
}

func fromEnv(lookup func(string) (string, bool)) (*Config, error) {
	get := func(key, def string) string {
		if v, ok := lookup(key); ok {
			return v
		}
		return def
	}

	port, err := strconv.Atoi(get("PORT", "8080"))
	if err != nil || port < 0 || port > 65535 {
		return nil, fmt.Errorf("PORT must be a number between 0 and 65535, got %q", get("PORT", ""))
	}

	autoMigrate, err := strconv.ParseBool(get("GOLOG_AUTO_MIGRATE", "true"))
	if err != nil {
		return nil, fmt.Errorf("GOLOG_AUTO_MIGRATE must be true or false, got %q", get("GOLOG_AUTO_MIGRATE", ""))
	}

	sslMode := get("DB_SSLMODE", "disable")
	if !sslModes[sslMode] {
		return nil, fmt.Errorf("DB_SSLMODE must be one of disable, require, verify-ca, verify-full, got %q", sslMode)
	}

	secureCookies, err := strconv.ParseBool(get("GOLOG_SECURE_COOKIES", "false"))
	if err != nil {
		return nil, fmt.Errorf("GOLOG_SECURE_COOKIES must be true or false, got %q", get("GOLOG_SECURE_COOKIES", ""))
	}

	retention, err := strconv.Atoi(get("GOLOG_RETENTION_DAYS", "0"))
	if err != nil || retention < 0 || retention > maxRetentionDays {
		return nil, fmt.Errorf("GOLOG_RETENTION_DAYS must be a number between 0 and %d, got %q", maxRetentionDays, get("GOLOG_RETENTION_DAYS", ""))
	}

	writeToken, readToken := get("GOLOG_WRITE_TOKEN", ""), get("GOLOG_READ_TOKEN", "")
	for name, tok := range map[string]string{"GOLOG_WRITE_TOKEN": writeToken, "GOLOG_READ_TOKEN": readToken} {
		if tok != "" && len(tok) < MinTokenLength {
			return nil, fmt.Errorf("%s must be at least %d characters", name, MinTokenLength)
		}
	}
	if writeToken != "" && writeToken == readToken {
		return nil, errors.New("GOLOG_WRITE_TOKEN and GOLOG_READ_TOKEN must differ: a writer must not be able to read")
	}

	return &Config{
		DatabaseURL:        get("DATABASE_URL", ""),
		DBHost:             get("DB_HOST", "localhost"),
		DBPort:             get("DB_PORT", "5432"),
		DBUser:             get("DB_USER", "postgres"),
		DBPassword:         get("DB_PASSWORD", "postgres"),
		DBName:             get("DB_NAME", "golog"),
		DBSSLMode:          sslMode,
		Port:               port,
		CORSAllowedOrigins: splitList(get("CORS_ALLOWED_ORIGINS", "")),
		AutoMigrate:        autoMigrate,
		WriteToken:         writeToken,
		ReadToken:          readToken,
		SecureCookies:      secureCookies,
		RetentionDays:      retention,
	}, nil
}

// DSN returns the PostgreSQL connection string. Built as a URL so that
// passwords with spaces, quotes or '@' survive intact.
func (c *Config) DSN() string {
	if c.DatabaseURL != "" {
		return c.DatabaseURL
	}
	q := url.Values{"sslmode": {c.DBSSLMode}}
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.DBUser, c.DBPassword),
		Path:   "/" + c.DBName,
	}
	if strings.HasPrefix(c.DBHost, "/") {
		// A unix socket directory cannot be a URL host; lib/pq reads it from ?host=.
		q.Set("host", c.DBHost)
		q.Set("port", c.DBPort)
	} else {
		u.Host = net.JoinHostPort(c.DBHost, c.DBPort)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
