package config

import (
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	for _, k := range []string{"PORT", "POSTGRES_DSN", "REDIS_ADDR"} {
		t.Setenv(k, "")
	}

	cfg := Load()

	if cfg.Port != "8080" {
		t.Errorf("expected default port 8080, got %q", cfg.Port)
	}
	if cfg.PostgresDSN != "" {
		t.Errorf("expected empty postgres DSN without env, got %q", cfg.PostgresDSN)
	}
	if cfg.RedisAddr == "" {
		t.Error("expected non-empty default redis addr")
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("PORT", "9090")
	t.Setenv("POSTGRES_DSN", "postgres://custom")
	t.Setenv("REDIS_ADDR", "redis:7777")

	cfg := Load()

	if cfg.Port != "9090" {
		t.Errorf("expected port 9090, got %q", cfg.Port)
	}
	if cfg.PostgresDSN != "postgres://custom" {
		t.Errorf("unexpected dsn %q", cfg.PostgresDSN)
	}
	if cfg.RedisAddr != "redis:7777" {
		t.Errorf("unexpected redis addr %q", cfg.RedisAddr)
	}
}

func TestLoadKeepsNonZeroEnv(t *testing.T) {
	t.Setenv("PORT", "1234")

	cfg := Load()
	if cfg.Port != "1234" {
		t.Errorf("expected env port 1234, got %q", cfg.Port)
	}
}
