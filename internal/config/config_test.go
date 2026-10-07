package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadFrom(t *testing.T, body string) (Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "megh.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, _, err := Load(p)
	return c, err
}

func TestMeshIsPerProviderAndOptional(t *testing.T) {
	c, err := loadFrom(t, "providers:\n  docker:\n    mesh: tailscale\n")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := c.Provider("docker").Mesh; got != "tailscale" {
		t.Errorf("docker mesh = %q, want tailscale", got)
	}
	if got := c.Provider("runpod").Mesh; got != "" {
		t.Errorf("an unset mesh must stay empty so the backend decides, got %q", got)
	}
}

// An unknown vendor is the whole reason `mesh:` names one instead of being a
// bool. Accepting it silently would leave a box off the mesh with the config
// saying otherwise.
func TestUnknownMeshVendorIsRejectedAtLoad(t *testing.T) {
	_, err := loadFrom(t, "providers:\n  docker:\n    mesh: netbird\n")
	if err == nil {
		t.Fatal("expected an error for an unsupported mesh vendor")
	}
	for _, want := range []string{"netbird", "docker", "tailscale"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name the bad value, the provider and what is supported; got: %v", err)
		}
	}
}

// No account is baked in: without a namespace from megh.yaml or the
// environment there is no default image, rather than someone else's.
func TestDefaultImageNeedsAConfiguredNamespace(t *testing.T) {
	t.Setenv("MEGH_GHCR_NAMESPACE", "")
	t.Setenv("MEGH_GHCR_USER", "")
	c := Default()
	if got := c.DefaultImage("slim"); got != "" {
		t.Errorf("no namespace configured, got default image %q", got)
	}
	if c.Registries[0].Username != "" {
		t.Errorf("no user configured, got %q", c.Registries[0].Username)
	}
	t.Setenv("MEGH_GHCR_NAMESPACE", "acme")
	c = Default()
	if got := c.DefaultImage("full"); got != "ghcr.io/acme/megh-full:latest" {
		t.Errorf("got %q", got)
	}
	if c.Registries[0].Username != "acme" {
		t.Errorf("user should default to the namespace, got %q", c.Registries[0].Username)
	}
}
