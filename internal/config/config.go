// Package config loads runtime configuration from the environment.
package config

import (
	"fmt"
	"os"
	"time"
)

// Config holds every runtime knob the server needs.
type Config struct {
	Addr            string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	ShutdownTimeout time.Duration
	DatabaseURL     string
	Environment     string
}

// Load reads configuration from the process environment, applying defaults for
// any value that is not set.
func Load() (Config, error) {
	readTimeout, err := durationFromEnv("SEMIPLANE_READ_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}

	writeTimeout, err := durationFromEnv("SEMIPLANE_WRITE_TIMEOUT", 30*time.Second)
	if err != nil {
		return Config{}, err
	}

	shutdownTimeout, err := durationFromEnv("SEMIPLANE_SHUTDOWN_TIMEOUT", 15*time.Second)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Addr:            stringFromEnv("SEMIPLANE_ADDR", ":8080"),
		ReadTimeout:     readTimeout,
		WriteTimeout:    writeTimeout,
		ShutdownTimeout: shutdownTimeout,
		DatabaseURL:     stringFromEnv("SEMIPLANE_DATABASE_URL", "sqlite://semiplane.db"),
		Environment:     stringFromEnv("SEMIPLANE_ENV", "development"),
	}, nil
}

// IsProduction reports whether the server is running in a production
// environment.
func (cfg Config) IsProduction() bool {
	return cfg.Environment == "production"
}

func stringFromEnv(key, fallback string) string {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		return fallback
	}

	return value
}

func durationFromEnv(key string, fallback time.Duration) (time.Duration, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return fallback, nil
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}

	return parsed, nil
}
