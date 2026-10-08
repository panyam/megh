package runpod

import (
	"context"
	"os"
	"sync"

	"github.com/panyam/megh/internal/providers"
)

// Provider is the RunPod backend. With no key of its own it reads
// RUNPOD_API_KEY from the environment per call, which is what the CLI wants.
// A server handling requests for whoever is signed in builds one per request
// with NewWithKey instead, so one request's key is never another's.
type Provider struct {
	key string

	placesMu sync.Mutex
	places   map[string]string
}

// New returns the RunPod backend that reads its key from the environment, for
// the registration list in cmd/root.go.
func New() *Provider { return &Provider{} }

// NewWithKey returns a RunPod backend that authenticates with key and ignores
// RUNPOD_API_KEY. An empty key behaves like New.
func NewWithKey(key string) *Provider { return &Provider{key: key} }

type apiKeyCtx struct{}

// with scopes this provider's key to one call. The package-level functions
// take a context already, so the key rides on it rather than on a global.
func (p *Provider) with(ctx context.Context) context.Context {
	if p.key == "" {
		return ctx
	}
	return context.WithValue(ctx, apiKeyCtx{}, p.key)
}

// keyFor is the key for this call: the provider's own if it has one, else the
// environment's.
func keyFor(ctx context.Context) string {
	if k, ok := ctx.Value(apiKeyCtx{}).(string); ok && k != "" {
		return k
	}
	return os.Getenv("RUNPOD_API_KEY")
}

var _ providers.Provider = (*Provider)(nil)

func (*Provider) Name() string { return "runpod" }

// Mesh is Tailscale at boot: the node key travels in the pod env and the pod
// brings itself up on the tailnet, which is how a phone reaches its web surfaces
// and how the control machine reaches it when public SSH is off. It is not
// configurable, because a pod megh cannot reach over the mesh and cannot reach
// over public SSH is a pod megh cannot reach.
func (*Provider) Mesh() providers.Mesh {
	return providers.Mesh{Vendor: providers.MeshTailscale, AtBoot: true}
}

func (p *Provider) Up(ctx context.Context, o providers.Options) (providers.Result, error) {
	return up(p.with(ctx), o)
}

func (p *Provider) List(ctx context.Context) ([]providers.Box, error) { return List(p.with(ctx)) }

func (p *Provider) Terminate(ctx context.Context, id string) error {
	return Terminate(p.with(ctx), id)
}

func (p *Provider) Volumes(ctx context.Context) ([]providers.Volume, error) {
	return Volumes(p.with(ctx))
}

func (p *Provider) CreateVolume(ctx context.Context, name string, sizeGiB int, dc string) (*providers.Volume, error) {
	return CreateVolume(p.with(ctx), name, sizeGiB, dc)
}

// Probe is the package-level Probe run with this provider's key, so a server
// can test capacity with the key the request carried.
func (p *Provider) Probe(ctx context.Context, o providers.Options) ProbeResult {
	return Probe(p.with(ctx), o)
}

// DataCenters lists the data centers RunPod accepts for a CPU pod. It reads
// RunPod's public API spec and needs no key; it is a method so callers holding
// only a provider can reach it.
func (p *Provider) DataCenters(ctx context.Context) []string { return DataCenters(ctx) }

func (p *Provider) DeleteVolume(ctx context.Context, id string) error {
	return DeleteVolume(p.with(ctx), id)
}
