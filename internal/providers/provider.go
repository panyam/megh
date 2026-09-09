package providers

import "context"

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
	// which is the shared static key. Never set for a backend whose Tailnet()
	// is false.
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

	// Tailnet reports whether boxes from this backend join a Tailscale tailnet.
	// False for a local backend, where the tailnet buys nothing over loopback:
	// `up` then skips minting a node key rather than making one nothing uses.
	Tailnet() bool

	Up(ctx context.Context, o Options) (Result, error)
	List(ctx context.Context) ([]Box, error)
	Terminate(ctx context.Context, id string) error

	Volumes(ctx context.Context) ([]Volume, error)
	CreateVolume(ctx context.Context, name string, sizeGiB int, dc string) (*Volume, error)
	DeleteVolume(ctx context.Context, id string) error
}
