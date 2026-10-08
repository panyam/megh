package providers

import (
	"cmp"
	"context"
	"slices"

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
	// PullToken is the registry token a VM backend (Hetzner, Vultr) logs in
	// with to pull Image on first boot, as registries[0]'s user. Empty means
	// no login, which is enough for a public image. The caller supplies it
	// (the CLI from registries[0].token_env, the web control plane from the
	// request), so a backend never reads it from its own environment. RunPod
	// ignores it, since its pull credential lives in the RunPod console, and
	// so does docker, which pulls with the host's own login.
	PullToken string
	// Type is the exact machine to create, as an Offer names it (a Vultr plan,
	// a Hetzner server type, a RunPod instance such as "cpu3g-4-16"). Empty
	// means the cheapest type meeting VCPU/RAMGiB/DiskGiB, which is what a
	// launch did before the catalog existed.
	Type string
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

// StoppedBoxStarter starts a box that still exists but is not running. Only the
// docker backend implements this; a stopped cloud pod is replaced with Up.
type StoppedBoxStarter interface {
	StartStopped(ctx context.Context, id string) (Result, error)
}

// Want is a request to a backend's catalog: the least a machine must have, and
// optionally the one location to look in. Zero means no minimum.
type Want struct {
	VCPU    int
	RAMGiB  int
	DiskGiB int
	DC      string // "" = every location; some backends need one (RunPod prices per data center)
}

// Offer is one machine type a backend sells in one location: what Up creates
// when Options.Type names it. DiskGiB is the most disk the type comes with or
// allows. Stock is the backend's own availability hint when it gives one
// ("High", "Low"), and is a hint only: capacity is proved by renting.
type Offer struct {
	DC      string  `json:"dc"`
	Type    string  `json:"type"`
	VCPU    int     `json:"vcpu"`
	RAMGiB  int     `json:"ramGiB"`
	DiskGiB int     `json:"diskGiB"`
	PerHr   float64 `json:"perHr"`
	Stock   string  `json:"stock,omitempty"`
}

// Locator answers "what can I rent that has at least this much" from the
// backend's own catalog: every machine type meeting Want in every location
// (or in Want.DC), cheapest first.
type Locator interface {
	Offers(ctx context.Context, w Want) ([]Offer, error)
}

// SortOffers orders offers cheapest first, then by location and type, so a
// listing is stable from one call to the next.
func SortOffers(o []Offer) {
	slices.SortFunc(o, func(a, b Offer) int {
		return cmp.Or(cmp.Compare(a.PerHr, b.PerHr), cmp.Compare(a.DC, b.DC), cmp.Compare(a.Type, b.Type))
	})
}

// CheapestPerDC keeps the cheapest offer in each location, in the order given:
// the answer to "where can this run", as opposed to "what can I rent".
func CheapestPerDC(o []Offer) []Offer {
	seen := map[string]bool{}
	var out []Offer
	for _, x := range o {
		if !seen[x.DC] {
			seen[x.DC] = true
			out = append(out, x)
		}
	}
	return out
}

// Placer names a backend's location codes for people: "Chicago, US" for
// Vultr's "ord". The codes are what every API takes and returns; the names are
// only for display. A backend answers from its own API, so a new location gets
// its name with no change here, and caches the answer for the life of the
// process. A code missing from the map has no known name, and callers show the
// bare code.
type Placer interface {
	Places(ctx context.Context) (map[string]string, error)
}
