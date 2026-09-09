package providers

import "context"

// All returns every registered backend, ordered by name so output is stable.
func All() []Provider {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Provider, 0, len(providers))
	for _, n := range names() {
		out = append(out, providers[n])
	}
	return out
}

// ListAll gathers boxes from every registered backend. It is for the commands
// that are a GLOBAL view rather than a per-provider one (`megh portal`,
// `megh storage list`) and so carry no --provider flag.
//
// It returns partial results alongside per-backend errors rather than failing
// on the first one, because the backends fail independently and for unrelated
// reasons: with no RUNPOD_API_KEY set, the RunPod backend errors while the
// docker one is perfectly healthy, and a local-only user should still get their
// local boxes listed. The caller decides whether an error is fatal; a caller
// with results and errors should show both.
func ListAll(ctx context.Context) ([]Box, []error) {
	var boxes []Box
	var errs []error
	for _, p := range All() {
		got, err := p.List(ctx)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		boxes = append(boxes, got...)
	}
	return boxes, errs
}

// VolumesAll is ListAll for scratch volumes: one cross-provider view, partial
// results plus per-backend errors.
func VolumesAll(ctx context.Context) ([]Volume, []error) {
	var vols []Volume
	var errs []error
	for _, p := range All() {
		got, err := p.Volumes(ctx)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		vols = append(vols, got...)
	}
	return vols, errs
}
