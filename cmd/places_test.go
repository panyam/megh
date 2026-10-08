package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/panyam/megh/internal/providers"
)

type placeOnly struct {
	providers.Provider
	m   map[string]string
	err error
}

func (p placeOnly) Places(context.Context) (map[string]string, error) { return p.m, p.err }

// placeNamer asks each provider once however many codes it labels, and a
// provider that cannot answer leaves its codes bare instead of failing.
func TestPlaceNamerAsksEachProviderOnceAndToleratesFailure(t *testing.T) {
	calls := map[string]int{}
	n := newPlaceNamer(context.Background(), func(name string) providers.Placer {
		calls[name]++
		switch name {
		case "vultr":
			return placeOnly{m: map[string]string{"ord": "Chicago, US"}}
		case "broken":
			return placeOnly{err: errors.New("HTTP 503")}
		}
		return nil
	})
	for i := 0; i < 3; i++ {
		if got := n.place("vultr", "ord"); got != "Chicago, US" {
			t.Fatalf("vultr ord = %q", got)
		}
	}
	if got := n.place("broken", "x1"); got != "" {
		t.Errorf("failing provider: %q", got)
	}
	if got := n.place("docker", "local"); got != "" {
		t.Errorf("no Placer: %q", got)
	}
	if calls["vultr"] != 1 {
		t.Errorf("asked vultr %d times, want once", calls["vultr"])
	}
}
