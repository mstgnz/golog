package config

import (
	"os"
	"testing"
)

func TestGetEnv(t *testing.T) {
	// Test with existing environment variable
	testKey := "TEST_ENV_VAR"
	testValue := "test_value"
	os.Setenv(testKey, testValue)
	defer os.Unsetenv(testKey)

	result := getEnv(testKey, "default_value")
	if result != testValue {
		t.Errorf("getEnv(%s) = %s; want %s", testKey, result, testValue)
	}

	// Test with non-existing environment variable
	nonExistingKey := "NON_EXISTING_VAR"
	defaultValue := "default_value"
	result = getEnv(nonExistingKey, defaultValue)
	if result != defaultValue {
		t.Errorf("getEnv(%s) = %s; want %s", nonExistingKey, result, defaultValue)
	}
}

func TestLoad(t *testing.T) {
	// Save current environment
	oldEnv := map[string]string{
		"DB_HOST":     os.Getenv("DB_HOST"),
		"DB_PORT":     os.Getenv("DB_PORT"),
		"DB_USER":     os.Getenv("DB_USER"),
		"DB_PASSWORD": os.Getenv("DB_PASSWORD"),
		"DB_NAME":     os.Getenv("DB_NAME"),
		"PORT":        os.Getenv("PORT"),
	}

	// Restore environment after test
	defer func() {
		for k, v := range oldEnv {
			if v != "" {
				os.Setenv(k, v)
			} else {
				os.Unsetenv(k)
			}
		}
	}()

	// Set test environment variables
	os.Setenv("DB_HOST", "test-host")
	os.Setenv("DB_PORT", "5433")
	os.Setenv("DB_USER", "test-user")
	os.Setenv("DB_PASSWORD", "test-password")
	os.Setenv("DB_NAME", "test-db")
	os.Setenv("PORT", "9090")

	// Load config
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	// Check values
	if cfg.DBHost != "test-host" {
		t.Errorf("cfg.DBHost = %s; want test-host", cfg.DBHost)
	}
	if cfg.DBPort != "5433" {
		t.Errorf("cfg.DBPort = %s; want 5433", cfg.DBPort)
	}
	if cfg.DBUser != "test-user" {
		t.Errorf("cfg.DBUser = %s; want test-user", cfg.DBUser)
	}
	if cfg.DBPassword != "test-password" {
		t.Errorf("cfg.DBPassword = %s; want test-password", cfg.DBPassword)
	}
	if cfg.DBName != "test-db" {
		t.Errorf("cfg.DBName = %s; want test-db", cfg.DBName)
	}
	if cfg.Port != 9090 {
		t.Errorf("cfg.Port = %d; want 9090", cfg.Port)
	}

	// Test with invalid port
	os.Setenv("PORT", "invalid")
	_, err = Load()
	if err == nil {
		t.Error("Load() with invalid PORT should return error")
	}
}
