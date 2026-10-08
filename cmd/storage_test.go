package cmd

import (
	"strings"
	"testing"

	"github.com/panyam/megh/internal/providers"
)

// The hint printed after creating a volume must name the volume's own
// provider: following it on any other backend fails, because a volume only
// attaches to boxes on the provider that owns it.
func TestLaunchHintNamesTheVolumesProvider(t *testing.T) {
	for _, p := range []string{"runpod", "vultr", "hetzner"} {
		v := providers.Volume{Provider: p, ID: "vol-1", Name: "megh-work", DataCenter: "dc-1", Size: 100}
		got := launchHint(v)
		for _, want := range []string{"megh up <name>", "--provider " + p, "--volume vol-1", "--dc dc-1"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: hint %q lacks %q", p, got, want)
			}
		}
		for _, other := range []string{"runpod", "vultr", "hetzner"} {
			if other != p && strings.Contains(got, other) {
				t.Errorf("%s: hint %q names %s", p, got, other)
			}
		}
	}
}
