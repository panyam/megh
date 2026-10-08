package cmd

import (
	"context"

	"github.com/panyam/megh/internal/providers"
)

// placeNamer labels location codes with the provider's own names ("ord" ->
// "Chicago, US") for the CLI's tables. It asks each provider at most once per
// command, and a provider that has no names or cannot answer leaves its codes
// bare: a missing label never fails a listing.
type placeNamer struct {
	ctx    context.Context
	lookup func(provider string) providers.Placer
	cache  map[string]map[string]string
}

func newPlaceNamer(ctx context.Context, lookup func(string) providers.Placer) *placeNamer {
	return &placeNamer{ctx: ctx, lookup: lookup, cache: map[string]map[string]string{}}
}

// registeredPlacer is the lookup the commands use: the registered backend of
// that name, if it can name its locations.
func registeredPlacer(name string) providers.Placer {
	p, err := providers.For(name)
	if err != nil {
		return nil
	}
	pl, _ := p.(providers.Placer)
	return pl
}

func (n *placeNamer) place(provider, code string) string {
	m, ok := n.cache[provider]
	if !ok {
		if pl := n.lookup(provider); pl != nil {
			m, _ = pl.Places(n.ctx)
		}
		n.cache[provider] = m
	}
	return m[code]
}

// orDash keeps a table column aligned when a cell has nothing to say.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
