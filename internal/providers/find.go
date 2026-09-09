package providers

import (
	"context"
	"fmt"
	"strings"
)

// Managed keeps only megh-managed boxes (name-prefixed). It is the filter that
// separates megh's boxes from anything else on the same account or docker
// daemon, and it is why every backend stores a box under the megh- prefix.
func Managed(boxes []Box) []Box {
	out := make([]Box, 0, len(boxes))
	for _, b := range boxes {
		if strings.HasPrefix(b.Name, NamePrefix) {
			out = append(out, b)
		}
	}
	return out
}

// Find resolves a megh-managed box by exact id or name. The name may be given
// with or without the megh- prefix (the bare name is what the user typed), so
// `megh down tsdiag` and `megh down megh-tsdiag` both resolve. Errors on no
// match or ambiguity.
func Find(ctx context.Context, p Provider, idOrName string) (*Box, error) {
	all, err := p.List(ctx)
	if err != nil {
		return nil, err
	}
	var matches []Box
	for _, b := range Managed(all) {
		if b.ID == idOrName || b.Name == idOrName || b.DisplayName() == idOrName {
			matches = append(matches, b)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no box matching %q (try `megh list`)", idOrName)
	case 1:
		return &matches[0], nil
	default:
		ids := make([]string, len(matches))
		for i, m := range matches {
			ids[i] = m.ID
		}
		return nil, fmt.Errorf("%q is ambiguous across %s; pass an id", idOrName, strings.Join(ids, ", "))
	}
}

// Sole returns the only megh-managed box when exactly one exists.
func Sole(ctx context.Context, p Provider) (*Box, error) {
	all, err := p.List(ctx)
	if err != nil {
		return nil, err
	}
	boxes := Managed(all)
	switch len(boxes) {
	case 0:
		return nil, fmt.Errorf("no boxes (run `megh up` first)")
	case 1:
		return &boxes[0], nil
	default:
		return nil, fmt.Errorf("%d boxes; name one: `megh ssh <name>`", len(boxes))
	}
}

// FindOrSole resolves the box named by args (at most one), or the only box when
// none is named. Every command that takes an optional box name wants exactly
// this, and each used to spell it out.
func FindOrSole(ctx context.Context, p Provider, args []string) (*Box, error) {
	if len(args) == 1 {
		return Find(ctx, p, args[0])
	}
	return Sole(ctx, p)
}
