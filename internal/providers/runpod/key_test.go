package runpod

import (
	"context"
	"testing"
)

// A server builds one provider per request from the key the browser sent; that
// key must win over whatever RUNPOD_API_KEY the process happens to have, and
// must not leak into a provider built without one.
func TestProviderKeyIsScopedToItsCalls(t *testing.T) {
	t.Setenv("RUNPOD_API_KEY", "from-env")
	ctx := context.Background()
	if got := keyFor(NewWithKey("from-request").with(ctx)); got != "from-request" {
		t.Errorf("own key: got %q", got)
	}
	if got := keyFor(New().with(ctx)); got != "from-env" {
		t.Errorf("no key falls back to env: got %q", got)
	}
	if got := keyFor(NewWithKey("").with(ctx)); got != "from-env" {
		t.Errorf("empty key behaves like New: got %q", got)
	}
}
