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
}

// sslModes are the values lib/pq accepts.
var sslModes = map[string]bool{"disable": true, "require": true, "verify-ca": true, "verify-full": true}

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
	}, nil
}

// DSN returns the PostgreSQL connection string. Built as a URL so that
// passwords with spaces, quotes or '@' survive intact.
func (c *Config) DSN() string {
	if c.DatabaseURL != "" {
		return c.DatabaseURL
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.DBUser, c.DBPassword),
		Host:     net.JoinHostPort(c.DBHost, c.DBPort),
		Path:     "/" + c.DBName,
		RawQuery: url.Values{"sslmode": {c.DBSSLMode}}.Encode(),
	}
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
