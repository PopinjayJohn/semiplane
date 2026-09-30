package config_test

import (
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/config"
)

func TestLoadAppliesDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	if cfg.Addr != ":8080" {
		t.Errorf("Addr = %q, want %q", cfg.Addr, ":8080")
	}

	if cfg.ReadTimeout != 10*time.Second {
		t.Errorf("ReadTimeout = %v, want %v", cfg.ReadTimeout, 10*time.Second)
	}

	if cfg.IsProduction() {
		t.Error("IsProduction() = true, want false for default environment")
	}
}

func TestLoadReadsEnvironment(t *testing.T) {
	t.Setenv("SEMIPLANE_ADDR", ":9999")
	t.Setenv("SEMIPLANE_ENV", "production")
	t.Setenv("SEMIPLANE_READ_TIMEOUT", "3s")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	if cfg.Addr != ":9999" {
		t.Errorf("Addr = %q, want %q", cfg.Addr, ":9999")
	}

	if !cfg.IsProduction() {
		t.Error("IsProduction() = false, want true when SEMIPLANE_ENV=production")
	}

	if cfg.ReadTimeout != 3*time.Second {
		t.Errorf("ReadTimeout = %v, want %v", cfg.ReadTimeout, 3*time.Second)
	}
}

func TestLoadRejectsMalformedDuration(t *testing.T) {
	t.Setenv("SEMIPLANE_WRITE_TIMEOUT", "not-a-duration")

	if _, err := config.Load(); err == nil {
		t.Fatal("Load() error = nil, want error for malformed duration")
	}
}
