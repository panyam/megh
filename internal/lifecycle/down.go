package lifecycle

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/panyam/megh/internal/providers"
	"github.com/panyam/megh/internal/tsapi"
)

// deregisterTimeout bounds the pre-terminate tailnet logout. Generous enough for
// a reachable box (the remote command alone allows 15s) and short enough that an
// unreachable one does not hold up termination.
const deregisterTimeout = 25 * time.Second

// Find resolves a box by name or id on one backend, or the only managed box
// when name is empty. It returns the backend too, which Down needs.
func (s *Service) Find(ctx context.Context, provider, name string) (providers.Provider, *providers.Box, error) {
	prov, err := s.Provider(provider)
	if err != nil {
		return nil, nil, err
	}
	var args []string
	if name != "" {
		args = []string{name}
	}
	box, err := providers.FindOrSole(ctx, prov, args)
	if err != nil {
		return nil, nil, err
	}
	return prov, box, nil
}

// DownOptions tunes a termination.
type DownOptions struct {
	// Leave asks the box to leave its mesh before it is terminated and returns
	// a line to show, or "" for none. It runs only on a mesh backend, under
	// deregisterTimeout. Nil skips it: a caller with no SSH to the box (the
	// server) relies on the node removal that follows termination instead.
	Leave func(ctx context.Context, box providers.Box) string
}

// Down terminates a box. The volume is untouched. On a mesh backend the box
// first leaves the tailnet (when o.Leave is set) and its nodes are removed
// through the control plane afterwards; both are best effort, since a box that
// was already unreachable is exactly the one that leaves debris, and cleaning
// up must never turn a successful termination into a failure.
func (s *Service) Down(ctx context.Context, prov providers.Provider, box providers.Box, o DownOptions) error {
	// The whole leave attempt is under a wall-clock deadline. A box with no
	// public SSH dials its MagicDNS name, and from a control machine that is not
	// on the tailnet that name never resolves; ssh's ConnectTimeout does not
	// cover resolution, so without this the terminate step never runs and the
	// pod keeps billing.
	if prov.Mesh().On() && o.Leave != nil {
		lctx, cancel := context.WithTimeout(ctx, deregisterTimeout)
		msg := o.Leave(lctx, box)
		cancel()
		if msg != "" {
			fmt.Fprintln(s.out(), msg)
		}
	}
	if err := prov.Terminate(ctx, box.ID); err != nil {
		return err
	}
	fmt.Fprintf(s.out(), "terminated %s (%s)\n", box.DisplayName(), box.ID)
	if prov.Mesh().On() {
		s.pruneNodesBestEffort(ctx, prov, box.DisplayName())
	}
	return nil
}

// pruneNodesBestEffort removes a terminated box's tailnet nodes. Every problem
// is a note rather than an error, and no credential configured is silent.
func (s *Service) pruneNodesBestEffort(ctx context.Context, prov providers.Provider, box string) {
	if s.Tailscale == nil {
		return
	}
	c, err := s.Tailscale()
	if err != nil {
		return // no API key configured; the leave above was the only path
	}
	live, err := LiveBoxNames(ctx, prov)
	if err != nil {
		live = map[string]bool{} // provider unreachable: fall back to name matching alone
	}
	delete(live, box) // we just terminated it
	deleted, _, err := PruneNodesFor(ctx, c, box, live)
	switch {
	case err != nil:
		fmt.Fprintf(s.out(), "note: could not remove %s from the tailnet (%v)\n", box, err)
	case len(deleted) > 0:
		fmt.Fprintf(s.out(), "removed %d tailnet node(s): %s\n", len(deleted), strings.Join(deleted, ", "))
	}
}

// LiveBoxNames is the set of box names that currently exist at the provider. It
// is the guard on every node delete: a node whose box is still running is never
// debris, whatever its name looks like.
func LiveBoxNames(ctx context.Context, prov providers.Provider) (map[string]bool, error) {
	boxes, err := prov.List(ctx)
	if err != nil {
		return nil, err
	}
	live := map[string]bool{}
	for _, b := range providers.Managed(boxes) {
		live[b.DisplayName()] = true
	}
	return live, nil
}

// PruneNodesFor deletes the tailnet nodes belonging to one box name, including
// the -N variants that accumulated under it. A variant whose name matches a
// DIFFERENT live box is left alone; box itself is always fair game, since the
// caller has just terminated it.
func PruneNodesFor(ctx context.Context, c *tsapi.Client, box string, live map[string]bool) (deleted, kept []string, err error) {
	devices, err := c.Devices(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, d := range devices {
		name := d.BareName()
		if !tsapi.MatchName(d, box) {
			continue
		}
		if name != box && live[name] {
			kept = append(kept, name)
			continue
		}
		if err := c.Delete(ctx, d.ID); err != nil {
			return deleted, kept, fmt.Errorf("delete %s (%s): %w", name, d.ID, err)
		}
		deleted = append(deleted, name)
	}
	return deleted, kept, nil
}
