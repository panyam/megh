package providers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// AmbiguousError is Locate's error when the same name is a box on more than
// one backend. It refuses rather than picks, since the caller may be about to
// terminate the box; --provider settles it.
type AmbiguousError struct {
	Name      string
	Providers []string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("%q is a box on %s; pass --provider to say which", e.Name, strings.Join(e.Providers, " and "))
}

// Locate is FindOrSole across backends: the box named by args (at most one),
// or the only box when none is named, on whichever backend holds it. It also
// returns that backend, since a Box does not record it.
//
// Every backend is asked at once. One with no credential (ErrNotConfigured) is
// skipped silently. One that fails otherwise only matters when no other backend
// has the box: then the failure is returned instead of a NotFoundError, because
// "not found" would be a guess. A NotFoundError carries the backends that were
// actually searched, empty when none had a credential.
func Locate(ctx context.Context, ps []Provider, args []string) (Provider, *Box, error) {
	type answer struct {
		p     Provider
		boxes []Box
		err   error
	}
	answers := make([]answer, len(ps))
	var wg sync.WaitGroup
	for i, p := range ps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			all, err := p.List(ctx)
			answers[i] = answer{p, Managed(all), err}
		}()
	}
	wg.Wait()

	var searched []string
	var failures []error
	type hit struct {
		p Provider
		b Box
	}
	var hits []hit
	for _, a := range answers {
		if a.err != nil {
			if !errors.Is(a.err, ErrNotConfigured) {
				failures = append(failures, a.err)
			}
			continue
		}
		searched = append(searched, a.p.Name())
		for _, b := range a.boxes {
			if len(args) == 0 || b.ID == args[0] || b.Name == args[0] || b.DisplayName() == args[0] {
				hits = append(hits, hit{a.p, b})
			}
		}
	}

	if len(args) == 0 {
		switch {
		case len(hits) == 1:
			return hits[0].p, &hits[0].b, nil
		case len(hits) > 1:
			names := make([]string, len(hits))
			for i, h := range hits {
				names[i] = h.b.DisplayName() + " (" + h.p.Name() + ")"
			}
			return nil, nil, fmt.Errorf("%d boxes: %s; name one", len(hits), strings.Join(names, ", "))
		case len(failures) > 0:
			return nil, nil, errors.Join(failures...)
		default:
			return nil, nil, fmt.Errorf("no boxes (run `megh up` first)")
		}
	}

	byProvider := map[string][]Box{}
	var order []Provider
	for _, h := range hits {
		if _, ok := byProvider[h.p.Name()]; !ok {
			order = append(order, h.p)
		}
		byProvider[h.p.Name()] = append(byProvider[h.p.Name()], h.b)
	}
	switch {
	case len(order) > 1:
		names := make([]string, len(order))
		for i, p := range order {
			names[i] = p.Name()
		}
		return nil, nil, &AmbiguousError{Name: args[0], Providers: names}
	case len(order) == 1:
		got := byProvider[order[0].Name()]
		if len(got) > 1 {
			// Same rule as Find within one backend.
			b, err := Find(ctx, order[0], args[0])
			return order[0], b, err
		}
		return order[0], &got[0], nil
	case len(failures) > 0:
		return nil, nil, fmt.Errorf("no box %q on %s, and could not ask the rest: %w",
			args[0], orNone(searched), errors.Join(failures...))
	default:
		return nil, nil, &NotFoundError{Name: args[0], Searched: searched}
	}
}

func orNone(names []string) string {
	if len(names) == 0 {
		return "no backend"
	}
	return strings.Join(names, ", ")
}
