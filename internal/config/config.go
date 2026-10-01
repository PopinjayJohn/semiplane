// Package config loads runtime configuration from the environment.
package config

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

// Config holds every runtime knob the server needs.
type Config struct {
	Addr            string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	ShutdownTimeout time.Duration
	HandlerTimeout  time.Duration
	DatabaseURL     string
	Environment     string
	// ContentRootBase is the directory campaigns' `os.Root` handles are
	// created beneath. A campaign's `content_root` is stored absolute, so this
	// is only read when a campaign is registered.
	ContentRootBase string
	// TrustedProxies are the networks whose forwarding headers are believed.
	// Empty means "believe none", which is the correct default for a
	// self-hosted server: the only proxy in a typical deployment is the one on
	// the same host, configured by the operator.
	TrustedProxies []string
	// InstanceName is the name this instance presents in its header and rail.
	// Empty falls back to the product name, so an unconfigured install still
	// renders a coherent page rather than a blank heading.
	InstanceName string
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

	// Separate from the server's WriteTimeout because they bound different
	// things. WriteTimeout covers writing a response body off the socket;
	// HandlerTimeout covers a handler that has been given the connection and
	// has not returned. A handler blocked on a database read is the case that
	// matters, and only this one catches it.
	handlerTimeout, err := durationFromEnv("SEMIPLANE_HANDLER_TIMEOUT", 25*time.Second)
	if err != nil {
		return Config{}, err
	}

	if handlerTimeout <= 0 {
		return Config{}, fmt.Errorf(
			"SEMIPLANE_HANDLER_TIMEOUT must be positive, got %s",
			handlerTimeout,
		)
	}

	contentRootBase, err := contentRootBaseFromEnv()
	if err != nil {
		return Config{}, err
	}

	return Config{
		Addr:            stringFromEnv("SEMIPLANE_ADDR", ":8080"),
		ReadTimeout:     readTimeout,
		WriteTimeout:    writeTimeout,
		ShutdownTimeout: shutdownTimeout,
		HandlerTimeout:  handlerTimeout,
		DatabaseURL:     stringFromEnv("SEMIPLANE_DATABASE_URL", "file:semiplane.db"),
		Environment:     stringFromEnv("SEMIPLANE_ENV", "development"),
		ContentRootBase: contentRootBase,
		TrustedProxies:  listFromEnv("SEMIPLANE_TRUSTED_PROXIES"),
		InstanceName:    stringFromEnv("SEMIPLANE_INSTANCE_NAME", ""),
	}, nil
}

// IsProduction reports whether the server is running in a production
// environment.
func (cfg Config) IsProduction() bool {
	return cfg.Environment == "production"
}

// contentRootBaseFromEnv resolves the directory campaign content roots live
// beneath, and requires it to be absolute.
//
// Absolute because a campaign's `content_root` is stored as an absolute path in
// the database (the schema fixes it), and resolving a relative base against the
// process's working directory at *registration* time would make that stored
// path mean something different after a restart from a different directory. A
// relative base is therefore rejected at startup rather than silently producing
// paths that resolve against whatever the supervisor chose.
func contentRootBaseFromEnv() (string, error) {
	base := stringFromEnv("SEMIPLANE_CONTENT_ROOT_BASE", "/var/lib/semiplane/vaults")

	if !strings.HasPrefix(base, "/") {
		return "", fmt.Errorf(
			"SEMIPLANE_CONTENT_ROOT_BASE must be an absolute path, got %q: campaign content "+
				"roots are stored absolute, so a relative base would resolve differently after a "+
				"restart from a different working directory",
			base,
		)
	}

	return base, nil
}

func stringFromEnv(key, fallback string) string {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		return fallback
	}

	return value
}

// listFromEnv splits a comma-separated value, trimming whitespace and dropping
// empty elements. Returns nil rather than an empty slice for an unset variable
// so that a caller can distinguish "configured as empty" from "not configured",
// which matters for TrustedProxies: both mean trust nothing, but only one is a
// deliberate choice.
func listFromEnv(key string) []string {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))

	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			values = append(values, trimmed)
		}
	}

	slices.Sort(values)

	return values
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
