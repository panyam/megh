package runpod

import (
	"context"

	"github.com/panyam/megh/internal/providers"
)

// Provider is the RunPod backend. It is a stateless value: every credential is
// read from the environment per call, so there is nothing to configure at
// construction and nothing to keep in sync with a reloaded config.
type Provider struct{}

// New returns the RunPod backend, for the registration list in cmd/root.go.
func New() *Provider { return &Provider{} }

var _ providers.Provider = (*Provider)(nil)

func (*Provider) Name() string { return "runpod" }

// Tailnet is true: a RunPod box joins the tailnet, which is how a phone reaches
// its web surfaces and how the control machine reaches it when public SSH is
// off.
func (*Provider) Tailnet() bool { return true }

func (*Provider) Up(ctx context.Context, o providers.Options) (providers.Result, error) {
	return up(ctx, o)
}

func (*Provider) List(ctx context.Context) ([]providers.Box, error) { return List(ctx) }

func (*Provider) Terminate(ctx context.Context, id string) error { return Terminate(ctx, id) }

func (*Provider) Volumes(ctx context.Context) ([]providers.Volume, error) { return Volumes(ctx) }

func (*Provider) CreateVolume(ctx context.Context, name string, sizeGiB int, dc string) (*providers.Volume, error) {
	return CreateVolume(ctx, name, sizeGiB, dc)
}

func (*Provider) DeleteVolume(ctx context.Context, id string) error { return DeleteVolume(ctx, id) }
