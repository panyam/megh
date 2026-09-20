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
