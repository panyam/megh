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
	c, err := loadFrom(t, "providers:\n  local:\n    mesh: tailscale\n")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := c.Provider("local").Mesh; got != "tailscale" {
		t.Errorf("local mesh = %q, want tailscale", got)
	}
	if got := c.Provider("runpod").Mesh; got != "" {
		t.Errorf("an unset mesh must stay empty so the backend decides, got %q", got)
	}
}

// An unknown vendor is the whole reason `mesh:` names one instead of being a
// bool. Accepting it silently would leave a box off the mesh with the config
// saying otherwise.
func TestUnknownMeshVendorIsRejectedAtLoad(t *testing.T) {
	_, err := loadFrom(t, "providers:\n  local:\n    mesh: netbird\n")
	if err == nil {
		t.Fatal("expected an error for an unsupported mesh vendor")
	}
	for _, want := range []string{"netbird", "local", "tailscale"} {
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

// providers.docker is the old name of providers.local; it still loads, with a
// note, and having both is refused rather than merged.
func TestDockerKeyLoadsAsLocal(t *testing.T) {
	c, err := loadFrom(t, "default_provider: docker\nproviders:\n  docker:\n    image: megh-local-slim:arm64\n")
	if err != nil {
		t.Fatal(err)
	}
	if c.Provider("local").Image != "megh-local-slim:arm64" || c.DefaultProvider != "local" {
		t.Errorf("got %+v / %q", c.Providers, c.DefaultProvider)
	}
	if _, ok := c.Providers["docker"]; ok {
		t.Error("the docker key survived beside local")
	}
	if len(c.Deprecations) == 0 || !strings.Contains(strings.Join(c.Deprecations, " "), "providers.local") {
		t.Errorf("no deprecation note: %v", c.Deprecations)
	}
	if _, err := loadFrom(t, "providers:\n  docker: {image: a}\n  local: {image: b}\n"); err == nil {
		t.Error("both providers.docker and providers.local were accepted")
	}
}

func TestUnknownEngineIsRejectedAtLoad(t *testing.T) {
	if _, err := loadFrom(t, "providers:\n  local: {engine: nerdctl}\n"); err == nil || !strings.Contains(err.Error(), "providers.local.engine") {
		t.Errorf("got %v", err)
	}
	if _, err := loadFrom(t, "providers:\n  local: {engine: podman}\n"); err != nil {
		t.Errorf("podman refused: %v", err)
	}
}
