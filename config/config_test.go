package config

import (
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// chdir stands in for t.Chdir, which needs Go 1.24; go.mod targets 1.22.
func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

func envMap(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestFromEnvDefaults(t *testing.T) {
	cfg, err := fromEnv(envMap(nil))
	if err != nil {
		t.Fatalf("fromEnv() error: %v", err)
	}
	want := &Config{
		DBHost: "localhost", DBPort: "5432", DBUser: "postgres", DBPassword: "postgres",
		DBName: "golog", DBSSLMode: "disable", Port: 8080, AutoMigrate: true,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("fromEnv() = %+v, want %+v", cfg, want)
	}
	if got := cfg.DSN(); got != "postgres://postgres:postgres@localhost:5432/golog?sslmode=disable" {
		t.Fatalf("DSN() = %q", got)
	}
}

func TestFromEnvValues(t *testing.T) {
	cfg, err := fromEnv(envMap(map[string]string{
		"DB_HOST": "db.internal", "DB_PORT": "6543", "DB_USER": "app", "DB_PASSWORD": "secret",
		"DB_NAME": "logs", "DB_SSLMODE": "verify-full", "PORT": "9090",
		"CORS_ALLOWED_ORIGINS": " https://a.example ,, https://b.example ",
		"GOLOG_AUTO_MIGRATE":   "false",
	}))
	if err != nil {
		t.Fatalf("fromEnv() error: %v", err)
	}
	if cfg.Port != 9090 || cfg.AutoMigrate || cfg.DBSSLMode != "verify-full" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if want := []string{"https://a.example", "https://b.example"}; !reflect.DeepEqual(cfg.CORSAllowedOrigins, want) {
		t.Fatalf("CORSAllowedOrigins = %q, want %q", cfg.CORSAllowedOrigins, want)
	}
	if got := cfg.DSN(); got != "postgres://app:secret@db.internal:6543/logs?sslmode=verify-full" {
		t.Fatalf("DSN() = %q", got)
	}
}

func TestDSNEscapesPassword(t *testing.T) {
	password := `p@ss word/#?'"`
	cfg := &Config{DBHost: "h", DBPort: "5432", DBUser: "u", DBPassword: password, DBName: "d", DBSSLMode: "disable"}
	u, err := url.Parse(cfg.DSN())
	if err != nil {
		t.Fatalf("DSN() is not a valid URL: %v", err)
	}
	got, _ := u.User.Password()
	if got != password {
		t.Fatalf("password round trip = %q, want %q", got, password)
	}
	if u.Hostname() != "h" || u.Path != "/d" {
		t.Fatalf("host/path corrupted: %q %q", u.Hostname(), u.Path)
	}
}

func TestDSNIPv6Host(t *testing.T) {
	cfg := &Config{DBHost: "::1", DBPort: "5432", DBUser: "u", DBName: "d", DBSSLMode: "disable"}
	if got := cfg.DSN(); !strings.Contains(got, "@[::1]:5432/") {
		t.Fatalf("DSN() = %q, want bracketed IPv6 host", got)
	}
}

func TestFromEnvAuthAndRetention(t *testing.T) {
	cfg, err := fromEnv(envMap(map[string]string{
		"GOLOG_WRITE_TOKEN": "write-0123456789", "GOLOG_READ_TOKEN": "read-0123456789ab",
		"GOLOG_SECURE_COOKIES": "true", "GOLOG_RETENTION_DAYS": "30",
	}))
	if err != nil {
		t.Fatalf("fromEnv() error: %v", err)
	}
	if cfg.WriteToken != "write-0123456789" || cfg.ReadToken != "read-0123456789ab" || !cfg.SecureCookies || cfg.RetentionDays != 30 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestDSNUnixSocket(t *testing.T) {
	cfg := &Config{DBHost: "/var/run/postgresql", DBPort: "5433", DBUser: "u", DBPassword: "p w", DBName: "d", DBSSLMode: "disable"}
	u, err := url.Parse(cfg.DSN())
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != "" || q.Get("host") != "/var/run/postgresql" || q.Get("port") != "5433" || u.Path != "/d" {
		t.Fatalf("DSN() = %q, want the socket directory in ?host=", cfg.DSN())
	}
	if pw, _ := u.User.Password(); pw != "p w" {
		t.Fatalf("password = %q", pw)
	}
}

func TestDatabaseURLWins(t *testing.T) {
	cfg, err := fromEnv(envMap(map[string]string{
		"DATABASE_URL": "postgres://x:y@remote/db?sslmode=require",
		"DB_HOST":      "ignored",
	}))
	if err != nil {
		t.Fatalf("fromEnv() error: %v", err)
	}
	if got := cfg.DSN(); got != "postgres://x:y@remote/db?sslmode=require" {
		t.Fatalf("DSN() = %q", got)
	}
}

func TestFromEnvErrors(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "port not a number", env: map[string]string{"PORT": "abc"}, want: "PORT"},
		{name: "port out of range", env: map[string]string{"PORT": "70000"}, want: "PORT"},
		{name: "negative port", env: map[string]string{"PORT": "-1"}, want: "PORT"},
		{name: "bad sslmode", env: map[string]string{"DB_SSLMODE": "prefer"}, want: "DB_SSLMODE"},
		{name: "bad auto migrate", env: map[string]string{"GOLOG_AUTO_MIGRATE": "maybe"}, want: "GOLOG_AUTO_MIGRATE"},
		{name: "bad secure cookies", env: map[string]string{"GOLOG_SECURE_COOKIES": "yes please"}, want: "GOLOG_SECURE_COOKIES"},
		{name: "retention not a number", env: map[string]string{"GOLOG_RETENTION_DAYS": "30d"}, want: "GOLOG_RETENTION_DAYS"},
		{name: "negative retention", env: map[string]string{"GOLOG_RETENTION_DAYS": "-1"}, want: "GOLOG_RETENTION_DAYS"},
		{name: "absurd retention", env: map[string]string{"GOLOG_RETENTION_DAYS": "999999"}, want: "GOLOG_RETENTION_DAYS"},
		{name: "short write token", env: map[string]string{"GOLOG_WRITE_TOKEN": "short"}, want: "GOLOG_WRITE_TOKEN"},
		{name: "short read token", env: map[string]string{"GOLOG_READ_TOKEN": "0123456789abcde"}, want: "GOLOG_READ_TOKEN"},
		{name: "same token twice", env: map[string]string{"GOLOG_WRITE_TOKEN": "0123456789abcdef", "GOLOG_READ_TOKEN": "0123456789abcdef"}, want: "must differ"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := fromEnv(envMap(tc.env))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("fromEnv() error = %v, want mention of %s", err, tc.want)
			}
		})
	}
}

func TestLoadReadsDotEnvWithoutOverridingEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("DB_NAME=from_file\nDB_HOST=file-host\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	t.Setenv("DB_HOST", "env-host")
	// godotenv sets variables it loads; make sure DB_NAME does not leak into other tests.
	t.Setenv("DB_NAME", "")
	os.Unsetenv("DB_NAME")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.DBHost != "env-host" {
		t.Errorf("DBHost = %q, environment should win over .env", cfg.DBHost)
	}
	if cfg.DBName != "from_file" {
		t.Errorf("DBName = %q, want value from .env", cfg.DBName)
	}
}

func TestLoadWithoutDotEnv(t *testing.T) {
	chdir(t, t.TempDir())
	if _, err := Load(); err != nil {
		t.Fatalf("Load() without .env should succeed, got %v", err)
	}
}

func TestLoadMalformedDotEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("GOLOG_BROKEN=\"unterminated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	if _, err := Load(); err == nil {
		t.Fatal("Load() with a malformed .env should fail")
	}
}
