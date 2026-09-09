package providers

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// The registry is populated by an explicit call list in cmd/root.go, never by
// package init(). A backend registered from its own init is invisible at the
// call site, and a binary that forgets the blank import composes an empty
// registry and reports nothing wrong. One visible list in one file is worth the
// two lines it costs.
var (
	mu        sync.RWMutex
	providers = map[string]Provider{}
)

// Register adds a backend under its Name(). Registering the same name twice
// panics: it is a wiring mistake at startup, not a runtime condition, and the
// alternative is a silent last-writer-wins.
func Register(p Provider) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := providers[p.Name()]; dup {
		panic("providers: duplicate registration for " + p.Name())
	}
	providers[p.Name()] = p
}

// For returns the named backend. The error names what IS registered, because
// the failure is almost always a typo or a backend that only exists on a newer
// binary, and "provider %q not implemented yet" told the user neither.
func For(name string) (Provider, error) {
	mu.RLock()
	defer mu.RUnlock()
	if p, ok := providers[name]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("unknown provider %q (available: %s)", name, strings.Join(names(), ", "))
}

// Names lists the registered backends, sorted, for help text and errors.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	return names()
}

// names is the unlocked body of Names; callers hold the lock.
func names() []string {
	out := make([]string, 0, len(providers))
	for n := range providers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
