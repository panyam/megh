// Package lifecycle launches, lists and terminates boxes and manages volumes.
// The CLI drives it today and `megh serve` will drive it from HTTP handlers,
// so it reads no package globals and no process environment of its own:
// everything a call needs is on the Service or the request. Credentials live in
// the Providers and the Tailscale factory a caller builds, which lets a server
// build them per request from the keys the signed-in browser sent.
package lifecycle

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/providers"
	"github.com/panyam/megh/internal/tsapi"
)

// Service runs lifecycle operations against a fixed set of backends.
type Service struct {
	Config    config.Config
	Providers []providers.Provider
	// Tailscale builds a control-plane client, or errors when no credential is
	// configured. Nil means no credential: key minting warns and falls back,
	// and node cleanup after `down` is skipped.
	Tailscale func() (*tsapi.Client, error)
	// Out receives progress lines ("terminated ...", "minted ..."); Err receives
	// warnings. Nil discards.
	Out, Err io.Writer
}

func (s *Service) out() io.Writer {
	if s.Out == nil {
		return io.Discard
	}
	return s.Out
}

func (s *Service) errOut() io.Writer {
	if s.Err == nil {
		return io.Discard
	}
	return s.Err
}

// Provider returns the backend called name. An empty name means the config's
// default_provider, then runpod, which is the CLI's own fallback order.
func (s *Service) Provider(name string) (providers.Provider, error) {
	name = cmp.Or(name, s.Config.DefaultProvider, "runpod")
	var have []string
	for _, p := range s.Providers {
		if p.Name() == name {
			return p, nil
		}
		have = append(have, p.Name())
	}
	slices.Sort(have)
	return nil, fmt.Errorf("unknown provider %q (available: %s)", name, strings.Join(have, ", "))
}

// List returns the boxes on one backend: megh-managed ones only, or every box
// on the account when all is set.
func (s *Service) List(ctx context.Context, provider string, all bool) ([]providers.Box, error) {
	prov, err := s.Provider(provider)
	if err != nil {
		return nil, err
	}
	boxes, err := prov.List(ctx)
	if err != nil {
		return nil, err
	}
	if !all {
		boxes = providers.Managed(boxes)
	}
	return boxes, nil
}

// Volumes lists scratch volumes across every backend. Backends fail for
// unrelated reasons (no RunPod key, no docker daemon), so it returns what it
// got alongside one error per failed backend and leaves the caller to decide.
func (s *Service) Volumes(ctx context.Context) ([]providers.Volume, []error) {
	var vols []providers.Volume
	var errs []error
	for _, p := range s.Providers {
		got, err := p.Volumes(ctx)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		vols = append(vols, got...)
	}
	return vols, errs
}

// CreateVolume creates a volume pinned to dc, which every backend that has
// volumes requires.
func (s *Service) CreateVolume(ctx context.Context, provider, name string, sizeGiB int, dc string) (*providers.Volume, error) {
	prov, err := s.Provider(provider)
	if err != nil {
		return nil, err
	}
	if dc == "" {
		return nil, fmt.Errorf("a data center is required (the volume is pinned to it)")
	}
	return prov.CreateVolume(ctx, name, sizeGiB, dc)
}

// DeleteVolume deletes a volume by id. The backend refuses one still attached.
func (s *Service) DeleteVolume(ctx context.Context, provider, id string) error {
	prov, err := s.Provider(provider)
	if err != nil {
		return err
	}
	return prov.DeleteVolume(ctx, id)
}
