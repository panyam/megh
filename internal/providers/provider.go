package providers

import (
	"context"

	"github.com/panyam/megh/internal/config"
)

// MeshTailscale is the one overlay megh speaks today.
const MeshTailscale = config.MeshTailscale

// Mesh is how a backend's boxes reach the overlay network they are reachable
// on. It carries a vendor rather than a bool so a second overlay is a new value
// instead of a new concept, and the config key (`providers.<p>.mesh`) names the
// same thing.
//
// AtBoot is the difference between the two backends and the reason this is not
// one field. A pod is handed its node key in the create-time env and joins
// itself while booting, because there is no SSH to join it over yet and with
// expose_ssh: false the mesh is the only way in at all. A local box is always
// reachable over loopback, so it joins when asked (`megh mesh join`) and its key
// travels on the stdin of the bring-up script, never in the container env where
// `docker inspect` would keep it.
type Mesh struct {
	Vendor string // "" = no mesh; MeshTailscale today
	AtBoot bool   // the key goes in the create-time env, and the box joins itself
}

// On reports whether this backend's boxes are on a mesh at all.
func (m Mesh) On() bool { return m.Vendor != "" }

// Options configures a box launch. Not every backend honours every field: a
// docker box ignores VCPU/RAMGiB/DiskGiB (the container gets the host's
// resources) and DataCenter (there is one place), and never receives TSAuthKey.
// A field a backend ignores is documented on that backend, not defended here.
type Options struct {
	Name       string
	VCPU       int
	RAMGiB     int
	DiskGiB    int
	Image      string
	VolumeID   string
	DataCenter string
	PubKey     string
	ExposeSSH  bool              // expose public break-glass SSH 22/tcp
	ExtraEnv   map[string]string // copied into the box env (e.g. box_envs)
	// TSAuthKey is the Tailscale node key to boot with. Set when megh minted a
	// key for this box specifically; empty falls back to the ambient TS_AUTHKEY,
	// which is the shared static key. Only a backend whose Mesh().AtBoot is true
	// receives it; the rest are joined afterwards, over SSH.
	TSAuthKey string
}

// Result is a successful launch. Only the human-facing summary is shared: what
// a backend prints after `up` is the access path it actually offers, and those
// differ (tailnet URLs on a cloud box, an ssh tunnel on a local one).
type Result interface {
	// Summary is the connection info printed after a successful launch.
	Summary() string
}

// Provider is a megh compute backend. Implementations live in sibling packages
// and are registered explicitly in cmd/root.go.
//
// Find, Sole and Managed are NOT methods: resolving a bare name to a box and
// filtering to megh-managed ones is C1's rule, identical everywhere, and a
// backend that reimplemented it could drift from the constraint. They are
// package helpers over List instead.
type Provider interface {
	// Name is the provider's identifier, as typed at --provider.
	Name() string

	// Mesh reports the overlay network this backend's boxes join, and when they
	// join it. The zero value means none, and `up`/`down` skip every mesh step
	// rather than minting a key nothing redeems or announcing a logout from a
	// network the box was never on.
	Mesh() Mesh

	Up(ctx context.Context, o Options) (Result, error)
	List(ctx context.Context) ([]Box, error)
	Terminate(ctx context.Context, id string) error

	Volumes(ctx context.Context) ([]Volume, error)
	CreateVolume(ctx context.Context, name string, sizeGiB int, dc string) (*Volume, error)
	DeleteVolume(ctx context.Context, id string) error
}
