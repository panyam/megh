// Package hetzner is the Hetzner Cloud backend. A box is a VM that does one
// thing: run the megh image under Docker with the box's volume at /workspace,
// so everything inside the box (entrypoint, persistence, tailnet bring-up)
// behaves exactly as on RunPod. What differs is the outside: real VMs sized
// per launch from Hetzner's server types, and volumes pinned to a location.
package hetzner

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/providers"
)

// managedLabel marks megh's servers and volumes. Hetzner has real labels, so
// listing filters on it server-side; the megh- name prefix stays too, since
// C1's display and lookup rules are shared code keyed on it.
const managedLabel = "megh.managed"

// vmImage is the OS the box host runs. Only Docker is installed on it.
const vmImage = "ubuntu-24.04"

// Provider is the Hetzner backend. Like the docker backend it takes a config
// accessor, because cfg is loaded after registration.
type Provider struct {
	cfg   func() config.Config
	base  string // API base, overridable for tests
	token string // "" = read the environment
}

// New returns the Hetzner backend for the registration list in cmd/root.go.
// It reads its token from HCLOUD_TOKEN (or providers.hetzner.api_key_env).
func New(cfg func() config.Config) *Provider { return &Provider{cfg: cfg, base: apiBase} }

// NewWithToken returns a Hetzner backend that authenticates with token instead
// of the environment, so one web request's token is never another's. An empty
// token behaves like New.
func NewWithToken(cfg func() config.Config, token string) *Provider {
	return &Provider{cfg: cfg, base: apiBase, token: token}
}

var (
	_ providers.Provider = (*Provider)(nil)
	_ providers.Locator  = (*Provider)(nil)
)

func (p *Provider) client() *client {
	env := cmp.Or(p.cfg().Provider("hetzner").APIKeyEnv, "HCLOUD_TOKEN")
	return &client{base: p.base, token: cmp.Or(p.token, os.Getenv(env))}
}

// Name is "hetzner".
func (*Provider) Name() string { return "hetzner" }

// Mesh is Tailscale at boot, as on RunPod: the node key rides in the box's
// env and the box joins as it starts, which is how a phone reaches it.
func (*Provider) Mesh() providers.Mesh {
	return providers.Mesh{Vendor: providers.MeshTailscale, AtBoot: true}
}

// Up creates a VM sized from the smallest Hetzner server type that fits
// o.VCPU/o.RAMGiB/o.DiskGiB in the volume's location, and boots the megh image
// on it. A volume is required: it fixes the location, and without one the
// box would keep nothing across a terminate.
func (p *Provider) Up(ctx context.Context, o providers.Options) (providers.Result, error) {
	if o.Image == "" {
		return nil, fmt.Errorf("no image: set registries[0].namespace in megh.yaml (or $MEGH_GHCR_NAMESPACE), or pass --image")
	}
	if o.VolumeID == "" {
		return nil, fmt.Errorf("hetzner needs a volume: create one with `megh storage create --provider hetzner --dc <location> --size 50`, then pass --volume or set providers.hetzner.default_volume")
	}
	c := p.client()
	vid, err := strconv.ParseInt(o.VolumeID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("hetzner volume ids are numbers, got %q", o.VolumeID)
	}
	var vr struct {
		Volume volume `json:"volume"`
	}
	if err := c.do(ctx, "GET", fmt.Sprintf("/volumes/%d", vid), nil, &vr); err != nil {
		return nil, err
	}
	loc := vr.Volume.Location.Name
	if o.DataCenter != "" && !strings.EqualFold(o.DataCenter, loc) {
		return nil, fmt.Errorf("volume %s is in %s, not %s; a box can only attach a volume in its own location", o.VolumeID, loc, o.DataCenter)
	}

	st, err := p.pickServerType(ctx, c, loc, o.VCPU, o.RAMGiB, o.DiskGiB)
	if err != nil {
		return nil, err
	}

	name := providers.PrefixName(o.Name)
	env := map[string]string{
		"PUBLIC_KEY":  o.PubKey,
		"WORK_MOUNT":  "/workspace",
		"ARCH_TAG":    "x86_64",
		"TS_AUTHKEY":  cmp.Or(o.TSAuthKey, os.Getenv("TS_AUTHKEY")),
		"TS_HOSTNAME": providers.ShortName(name),
	}
	for k, v := range o.ExtraEnv {
		env[k] = v
	}
	spec := bootSpec{Image: o.Image, VolumeID: vid, Env: env, ExposeSSH: o.ExposeSSH}
	if o.PullToken != "" {
		spec.PullToken = o.PullToken
		if regs := p.cfg().Registries; len(regs) > 0 {
			spec.PullUser = cmp.Or(regs[0].Username, regs[0].Namespace)
		}
	}

	req := map[string]any{
		"name":        name,
		"server_type": st.Name,
		"image":       vmImage,
		"location":    loc,
		"user_data":   cloudInit(spec),
		"labels":      map[string]string{managedLabel: "1"},
		"volumes":     []int64{vid},
		"automount":   true,
		"public_net":  map[string]bool{"enable_ipv4": true, "enable_ipv6": true},
	}
	var sr struct {
		Server server `json:"server"`
	}
	if err := c.do(ctx, "POST", "/servers", req, &sr); err != nil {
		return nil, err
	}
	return &result{name: providers.ShortName(name), typ: st, loc: loc, ip: sr.Server.PublicNet.IPv4.IP, ssh: o.ExposeSSH}, nil
}

// pickServerType is the cheapest current x86 server type sold in loc with at
// least the requested cores, memory and disk. x86 because the megh image is
// published for linux/amd64.
func (p *Provider) pickServerType(ctx context.Context, c *client, loc string, vcpu, ramGiB, diskGiB int) (serverType, error) {
	types, err := listServerTypes(ctx, c)
	if err != nil {
		return serverType{}, err
	}
	best, ok := cheapestByLocation(types, vcpu, ramGiB, diskGiB)[loc]
	if !ok {
		return serverType{}, fmt.Errorf("hetzner sells no x86 server type in %s with %d vCPU, %d GB RAM and %d GB disk", loc, vcpu, ramGiB, diskGiB)
	}
	return best.typ, nil
}

// Offers lists every location selling an x86 type that fits, with the type Up
// would pick there, cheapest first.
func (p *Provider) Offers(ctx context.Context, vcpu, ramGiB, diskGiB int) ([]providers.Offer, error) {
	types, err := listServerTypes(ctx, p.client())
	if err != nil {
		return nil, err
	}
	var out []providers.Offer
	for loc, b := range cheapestByLocation(types, vcpu, ramGiB, diskGiB) {
		out = append(out, providers.Offer{DC: loc, Type: b.typ.Name, PerHr: b.perHr})
	}
	slices.SortFunc(out, func(a, b providers.Offer) int {
		return cmp.Or(cmp.Compare(a.PerHr, b.PerHr), cmp.Compare(a.DC, b.DC))
	})
	return out, nil
}

func listServerTypes(ctx context.Context, c *client) ([]serverType, error) {
	var out struct {
		ServerTypes []serverType `json:"server_types"`
	}
	err := c.do(ctx, "GET", "/server_types?per_page=50", nil, &out)
	return out.ServerTypes, err
}

type priced struct {
	typ   serverType
	perHr float64
}

// cheapestByLocation is, per location, the cheapest current x86 type with at
// least the requested cores, memory and disk.
func cheapestByLocation(types []serverType, vcpu, ramGiB, diskGiB int) map[string]priced {
	best := map[string]priced{}
	for _, t := range types {
		if t.Architecture != "x86" || t.Deprecation != nil {
			continue
		}
		if t.Cores < vcpu || t.Memory < float64(ramGiB) || t.Disk < diskGiB {
			continue
		}
		for _, pr := range t.Prices {
			f, err := strconv.ParseFloat(pr.PriceHourly.Gross, 64)
			if err != nil {
				continue
			}
			if cur, ok := best[pr.Location]; !ok || f < cur.perHr {
				best[pr.Location] = priced{t, f}
			}
		}
	}
	return best
}

type result struct {
	name, loc, ip string
	typ           serverType
	ssh           bool
}

// Summary says what was rented and how to reach it once it has booted.
func (r *result) Summary() string {
	s := fmt.Sprintf("launched %s on hetzner %s (%d vCPU, %.0f GB) in %s\n", r.name, r.typ.Name, r.typ.Cores, r.typ.Memory, r.loc)
	s += "first boot installs Docker and pulls the image, so allow a few minutes\n"
	if r.ssh && r.ip != "" {
		s += fmt.Sprintf("ssh -p %d root@%s\n", boxSSHPort, r.ip)
	}
	return s
}

// List returns megh's servers (labelled megh.managed).
func (p *Provider) List(ctx context.Context) ([]providers.Box, error) {
	c := p.client()
	var boxes []providers.Box
	for page := 1; page > 0; {
		var out struct {
			Servers []server `json:"servers"`
			pagination
		}
		q := url.Values{"label_selector": {managedLabel}, "per_page": {"50"}, "page": {strconv.Itoa(page)}}
		if err := c.do(ctx, "GET", "/servers?"+q.Encode(), nil, &out); err != nil {
			return nil, err
		}
		for _, s := range out.Servers {
			boxes = append(boxes, toBox(s))
		}
		page = 0
		if n := out.Meta.Pagination.NextPage; n != nil {
			page = *n
		}
	}
	return boxes, nil
}

func toBox(s server) providers.Box {
	loc := s.Datacenter.Location.Name
	b := providers.Box{
		ID:         strconv.FormatInt(s.ID, 10),
		Name:       s.Name,
		Status:     strings.ToUpper(s.Status),
		DataCenter: loc,
		PublicIP:   s.PublicNet.IPv4.IP,
		Image:      "hetzner/" + s.ServerType.Name,
	}
	if b.PublicIP != "" {
		b.SSHPort = boxSSHPort
	}
	for _, pr := range s.ServerType.Prices {
		if pr.Location == loc {
			b.CostPerHr, _ = strconv.ParseFloat(pr.PriceHourly.Gross, 64)
		}
	}
	return b
}

// Terminate deletes the VM. Its volume is detached and survives.
func (p *Provider) Terminate(ctx context.Context, id string) error {
	return p.client().do(ctx, "DELETE", "/servers/"+url.PathEscape(id), nil, nil)
}

// Volumes lists every volume on the project, megh's or not, like RunPod's
// listing, so `megh storage list` shows what is billing.
func (p *Provider) Volumes(ctx context.Context) ([]providers.Volume, error) {
	var out struct {
		Volumes []volume `json:"volumes"`
	}
	if err := p.client().do(ctx, "GET", "/volumes?per_page=50", nil, &out); err != nil {
		return nil, err
	}
	vols := make([]providers.Volume, 0, len(out.Volumes))
	for _, v := range out.Volumes {
		vols = append(vols, toVolume(v))
	}
	return vols, nil
}

func toVolume(v volume) providers.Volume {
	return providers.Volume{Provider: "hetzner", ID: strconv.FormatInt(v.ID, 10), Name: v.Name, DataCenter: v.Location.Name, Size: v.Size}
}

// CreateVolume makes an ext4 volume in location dc. Hetzner formats it at
// creation, which is why nothing at boot ever formats a volume.
func (p *Provider) CreateVolume(ctx context.Context, name string, sizeGiB int, dc string) (*providers.Volume, error) {
	req := map[string]any{
		"name": name, "size": sizeGiB, "location": dc, "format": "ext4",
		"labels": map[string]string{managedLabel: "1"},
	}
	var out struct {
		Volume volume `json:"volume"`
	}
	if err := p.client().do(ctx, "POST", "/volumes", req, &out); err != nil {
		return nil, err
	}
	v := toVolume(out.Volume)
	return &v, nil
}

// DeleteVolume deletes a volume by id. Hetzner refuses one still attached.
func (p *Provider) DeleteVolume(ctx context.Context, id string) error {
	return p.client().do(ctx, "DELETE", "/volumes/"+url.PathEscape(id), nil, nil)
}
