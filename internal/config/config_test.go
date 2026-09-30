package config_test

import (
	"slices"
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

// TestLoadTrustsNoProxyByDefault is the assertion that a self-hosted server
// does not believe forwarding headers unless told to. The default being
// "believe none" is what makes a direct internet exposure safe; a default of
// "believe whatever arrives" would make every access-log client_ip forgeable.
func TestLoadTrustsNoProxyByDefault(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	if len(cfg.TrustedProxies) != 0 {
		t.Errorf("TrustedProxies = %v, want empty by default", cfg.TrustedProxies)
	}
}

func TestLoadReadsTrustedProxies(t *testing.T) {
	t.Setenv("SEMIPLANE_TRUSTED_PROXIES", " 10.0.0.0/8 , 192.168.1.1,, fd00::/8 ")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	want := []string{"10.0.0.0/8", "192.168.1.1", "fd00::/8"}
	if !slices.Equal(cfg.TrustedProxies, want) {
		t.Errorf("TrustedProxies = %v, want %v", cfg.TrustedProxies, want)
	}
}

// TestLoadRejectsRelativeContentRootBase covers the case the error message
// exists for: a relative base would be resolved against the process working
// directory at registration time, and a campaign's stored absolute content_root
// would then mean something different after a restart from elsewhere.
func TestLoadRejectsRelativeContentRootBase(t *testing.T) {
	t.Setenv("SEMIPLANE_CONTENT_ROOT_BASE", "vaults")

	if _, err := config.Load(); err == nil {
		t.Fatal("Load() error = nil, want error for a relative content root base")
	}
}

func TestLoadReadsContentRootBase(t *testing.T) {
	t.Setenv("SEMIPLANE_CONTENT_ROOT_BASE", "/srv/semiplane/vaults")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	if cfg.ContentRootBase != "/srv/semiplane/vaults" {
		t.Errorf("ContentRootBase = %q, want %q", cfg.ContentRootBase, "/srv/semiplane/vaults")
	}
}

func TestLoadRejectsNonPositiveHandlerTimeout(t *testing.T) {
	t.Setenv("SEMIPLANE_HANDLER_TIMEOUT", "0s")

	if _, err := config.Load(); err == nil {
		t.Fatal("Load() error = nil, want error for a zero handler timeout")
	}
}
