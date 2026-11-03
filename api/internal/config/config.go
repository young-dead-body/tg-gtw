package config

import (
	"os"
)

// Config holds runtime configuration loaded from environment variables.
type Config struct {
	// Port is the HTTP listen address for the API.
	Port string
	// PostgresDSN is the PostgreSQL connection string.
	PostgresDSN string
	// RedisAddr is the Redis host:port.
	RedisAddr string
}

// Load reads configuration from environment variables.
func Load() Config {
	return Config{
		Port:        getenv("PORT", "8080"),
		PostgresDSN: os.Getenv("POSTGRES_DSN"),
		RedisAddr:   getenv("REDIS_ADDR", "localhost:6379"),
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
