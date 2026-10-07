// Package vultr is the Vultr backend. Like Hetzner, a box is a VM running the
// megh image under Docker (internal/providers/vmhost), sized per launch from
// Vultr's plans. The difference is the volume: Vultr block storage arrives
// raw and attaches after the VM exists, so the boot script finds it, formats
// it only if it is blank, and mounts it.
package vultr

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/providers"
	"github.com/panyam/megh/internal/providers/vmhost"
)

const apiBase = "https://api.vultr.com/v2"

// managedTag marks megh's instances; listing filters on it server-side.
const managedTag = "megh-managed"

// planTypes are the shared and dedicated CPU plan families megh will rent.
// GPU and bare-metal plans are left out.
var planTypes = []string{"vc2", "vhf", "vhp", "voc"}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// Provider is the Vultr backend. It takes a config accessor, like the docker
// and Hetzner backends, because cfg is loaded after registration.
type Provider struct {
	cfg  func() config.Config
	base string
	key  string // "" = read the environment
	// attachTries and attachWait bound how long Up retries attaching the
	// volume while the new instance is still being created.
	attachTries int
	attachWait  time.Duration
}

// New returns the Vultr backend for the registration list in cmd/root.go.
// It reads its key from VULTR_API_KEY (or providers.vultr.api_key_env).
func New(cfg func() config.Config) *Provider {
	return &Provider{cfg: cfg, base: apiBase, attachTries: 36, attachWait: 5 * time.Second}
}

// NewWithKey returns a Vultr backend that authenticates with key instead of
// the environment, so one web request's key is never another's. An empty key
// behaves like New.
func NewWithKey(cfg func() config.Config, key string) *Provider {
	p := New(cfg)
	p.key = key
	return p
}

var (
	_ providers.Provider = (*Provider)(nil)
	_ providers.Locator  = (*Provider)(nil)
)

// Name is "vultr".
func (*Provider) Name() string { return "vultr" }

// Mesh is Tailscale at boot, as on RunPod and Hetzner.
func (*Provider) Mesh() providers.Mesh {
	return providers.Mesh{Vendor: providers.MeshTailscale, AtBoot: true}
}

func (p *Provider) settings() config.Provider { return p.cfg().Provider("vultr") }

type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string { return fmt.Sprintf("vultr: HTTP %d: %s", e.Status, e.Message) }

func (p *Provider) do(ctx context.Context, method, path string, body, out any) error {
	tok := cmp.Or(p.key, os.Getenv(cmp.Or(p.settings().APIKeyEnv, "VULTR_API_KEY")))
	if tok == "" {
		return fmt.Errorf("%w: vultr has no API key (set VULTR_API_KEY, or providers.vultr.api_key_env in megh.yaml)", providers.ErrNotConfigured)
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return &apiError{Status: resp.StatusCode, Message: cmp.Or(e.Error, strings.TrimSpace(string(raw)))}
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

type plan struct {
	ID          string   `json:"id"`
	VCPUCount   int      `json:"vcpu_count"`
	RAM         int      `json:"ram"` // MB
	Disk        int      `json:"disk"`
	MonthlyCost float64  `json:"monthly_cost"`
	Type        string   `json:"type"`
	Locations   []string `json:"locations"`
}

type block struct {
	ID                 string `json:"id"`
	Label              string `json:"label"`
	Region             string `json:"region"`
	SizeGB             int    `json:"size_gb"`
	AttachedToInstance string `json:"attached_to_instance"`
}

type instance struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Region      string   `json:"region"`
	Plan        string   `json:"plan"`
	MainIP      string   `json:"main_ip"`
	Status      string   `json:"status"`
	PowerStatus string   `json:"power_status"`
	Tags        []string `json:"tags"`
}

func (p *Provider) plans(ctx context.Context) ([]plan, error) {
	var out struct {
		Plans []plan `json:"plans"`
	}
	err := p.do(ctx, "GET", "/plans?per_page=500", nil, &out)
	return out.Plans, err
}

// pickPlan is the cheapest CPU plan sold in region with at least the
// requested vCPU, RAM and disk.
func pickPlan(plans []plan, region string, vcpu, ramGiB, diskGiB int) (plan, error) {
	best, ok := cheapestByRegion(plans, vcpu, ramGiB, diskGiB)[region]
	if !ok {
		return plan{}, fmt.Errorf("vultr sells no CPU plan in %s with %d vCPU, %d GB RAM and %d GB disk", region, vcpu, ramGiB, diskGiB)
	}
	return best, nil
}

// cheapestByRegion is, per region, the cheapest CPU plan with at least the
// requested vCPU, RAM and disk. Plans ending in -v6 are IPv6-only and left
// out, since the box's SSH and tailnet bring-up want IPv4.
func cheapestByRegion(plans []plan, vcpu, ramGiB, diskGiB int) map[string]plan {
	best := map[string]plan{}
	for _, pl := range plans {
		if !slices.Contains(planTypes, pl.Type) || strings.HasSuffix(pl.ID, "-v6") {
			continue
		}
		if pl.VCPUCount < vcpu || pl.RAM < ramGiB*1024 || pl.Disk < diskGiB {
			continue
		}
		for _, r := range pl.Locations {
			if cur, ok := best[r]; !ok || pl.MonthlyCost < cur.MonthlyCost {
				best[r] = pl
			}
		}
	}
	return best
}

// Offers lists every region selling a CPU plan that fits, with the plan Up
// would pick there, cheapest first. The hourly price is the monthly one over
// 730 hours, as List reports it.
func (p *Provider) Offers(ctx context.Context, vcpu, ramGiB, diskGiB int) ([]providers.Offer, error) {
	plans, err := p.plans(ctx)
	if err != nil {
		return nil, err
	}
	var out []providers.Offer
	for r, pl := range cheapestByRegion(plans, vcpu, ramGiB, diskGiB) {
		out = append(out, providers.Offer{DC: r, Type: pl.ID, PerHr: pl.MonthlyCost / 730})
	}
	slices.SortFunc(out, func(a, b providers.Offer) int {
		return cmp.Or(cmp.Compare(a.PerHr, b.PerHr), cmp.Compare(a.DC, b.DC))
	})
	return out, nil
}

// ubuntuID is the os_id of Ubuntu 24.04 x64, looked up rather than hardcoded.
func (p *Provider) ubuntuID(ctx context.Context) (int, error) {
	var out struct {
		OS []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"os"`
	}
	if err := p.do(ctx, "GET", "/os?per_page=500", nil, &out); err != nil {
		return 0, err
	}
	for _, o := range out.OS {
		if strings.Contains(o.Name, "Ubuntu 24.04") && strings.Contains(o.Name, "x64") {
			return o.ID, nil
		}
	}
	return 0, fmt.Errorf("vultr offers no Ubuntu 24.04 x64 image")
}

// Up creates an instance from the cheapest plan in the volume's region that
// fits, then attaches the volume, retrying while Vultr is still creating the
// instance. The boot script waits for the disk, formats it only if it is
// blank, and runs the megh image on it.
func (p *Provider) Up(ctx context.Context, o providers.Options) (providers.Result, error) {
	if o.Image == "" {
		return nil, fmt.Errorf("no image: set registries[0].namespace in megh.yaml (or $MEGH_GHCR_NAMESPACE), or pass --image")
	}
	if o.VolumeID == "" {
		return nil, fmt.Errorf("vultr needs a volume: create one with `megh storage create --provider vultr --dc <region> --size 50`, then pass --volume or set providers.vultr.default_volume")
	}
	var br struct {
		Block block `json:"block"`
	}
	if err := p.do(ctx, "GET", "/blocks/"+url.PathEscape(o.VolumeID), nil, &br); err != nil {
		return nil, err
	}
	vol := br.Block
	if vol.AttachedToInstance != "" {
		return nil, fmt.Errorf("volume %s is attached to instance %s; terminate that box first", o.VolumeID, vol.AttachedToInstance)
	}
	if o.DataCenter != "" && !strings.EqualFold(o.DataCenter, vol.Region) {
		return nil, fmt.Errorf("volume %s is in %s, not %s; a box can only attach a volume in its own region", o.VolumeID, vol.Region, o.DataCenter)
	}
	plans, err := p.plans(ctx)
	if err != nil {
		return nil, err
	}
	pl, err := pickPlan(plans, vol.Region, o.VCPU, o.RAMGiB, o.DiskGiB)
	if err != nil {
		return nil, err
	}
	osID, err := p.ubuntuID(ctx)
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
	spec := vmhost.Spec{Image: o.Image, Volume: vmhost.Volume{BlockSizeGB: vol.SizeGB}, Env: env, ExposeSSH: o.ExposeSSH}
	if regs := p.cfg().Registries; len(regs) > 0 && regs[0].TokenEnv != "" {
		spec.PullUser = cmp.Or(regs[0].Username, regs[0].Namespace)
		spec.PullToken = os.Getenv(regs[0].TokenEnv)
	}

	var ir struct {
		Instance instance `json:"instance"`
	}
	req := map[string]any{
		"region":      vol.Region,
		"plan":        pl.ID,
		"os_id":       osID,
		"label":       name,
		"hostname":    providers.ShortName(name),
		"tags":        []string{managedTag},
		"user_data":   base64.StdEncoding.EncodeToString([]byte(vmhost.Script(spec))),
		"enable_ipv6": true,
		"backups":     "disabled",
	}
	if err := p.do(ctx, "POST", "/instances", req, &ir); err != nil {
		return nil, err
	}
	inst := ir.Instance

	// Vultr refuses to attach a volume to an instance it is still creating,
	// so retry for a few minutes. A box whose volume never attaches is
	// terminated rather than left billing with nothing to boot onto.
	var attachErr error
	for i := 0; i < p.attachTries; i++ {
		attachErr = p.do(ctx, "POST", "/blocks/"+url.PathEscape(vol.ID)+"/attach", map[string]any{"instance_id": inst.ID, "live": true}, nil)
		if attachErr == nil {
			break
		}
		select {
		case <-ctx.Done():
			attachErr = ctx.Err()
		case <-time.After(p.attachWait):
			continue
		}
		break
	}
	if attachErr != nil {
		_ = p.Terminate(context.WithoutCancel(ctx), inst.ID)
		return nil, fmt.Errorf("could not attach volume %s to the new instance (terminated it): %w", vol.ID, attachErr)
	}
	return &result{name: providers.ShortName(name), plan: pl, region: vol.Region, ip: inst.MainIP, ssh: o.ExposeSSH}, nil
}

type result struct {
	name, region, ip string
	plan             plan
	ssh              bool
}

// Summary says what was rented and how to reach it once it has booted.
func (r *result) Summary() string {
	s := fmt.Sprintf("launched %s on vultr %s (%d vCPU, %d GB) in %s, about $%.3f/h\n",
		r.name, r.plan.ID, r.plan.VCPUCount, r.plan.RAM/1024, r.region, r.plan.MonthlyCost/730)
	s += "first boot installs Docker, prepares the volume and pulls the image, so allow a few minutes\n"
	if r.ssh && r.ip != "" && r.ip != "0.0.0.0" {
		s += fmt.Sprintf("ssh -p %d root@%s\n", vmhost.BoxSSHPort, r.ip)
	} else if r.ssh {
		s += fmt.Sprintf("its IP is assigned shortly; `megh list` shows the ssh endpoint (port %d)\n", vmhost.BoxSSHPort)
	}
	return s
}

// List returns megh's instances (tagged megh-managed).
func (p *Provider) List(ctx context.Context) ([]providers.Box, error) {
	var out struct {
		Instances []instance `json:"instances"`
	}
	if err := p.do(ctx, "GET", "/instances?per_page=500&tag="+url.QueryEscape(managedTag), nil, &out); err != nil {
		return nil, err
	}
	cost := map[string]float64{}
	if plans, err := p.plans(ctx); err == nil {
		for _, pl := range plans {
			cost[pl.ID] = pl.MonthlyCost / 730
		}
	}
	boxes := make([]providers.Box, 0, len(out.Instances))
	for _, in := range out.Instances {
		boxes = append(boxes, toBox(in, cost[in.Plan]))
	}
	return boxes, nil
}

func toBox(in instance, perHr float64) providers.Box {
	status := strings.ToUpper(in.Status)
	if in.Status == "active" && in.PowerStatus == "running" {
		status = "RUNNING"
	}
	b := providers.Box{
		ID: in.ID, Name: in.Label, Status: status, DataCenter: in.Region,
		CostPerHr: perHr, Image: "vultr/" + in.Plan,
	}
	if in.MainIP != "" && in.MainIP != "0.0.0.0" {
		b.PublicIP, b.SSHPort = in.MainIP, vmhost.BoxSSHPort
	}
	return b
}

// Terminate deletes the instance. Vultr detaches its volume, which survives.
func (p *Provider) Terminate(ctx context.Context, id string) error {
	return p.do(ctx, "DELETE", "/instances/"+url.PathEscape(id), nil, nil)
}

// Volumes lists every block storage volume on the account, megh's or not, so
// `megh storage list` shows what is billing.
func (p *Provider) Volumes(ctx context.Context) ([]providers.Volume, error) {
	var out struct {
		Blocks []block `json:"blocks"`
	}
	if err := p.do(ctx, "GET", "/blocks?per_page=500", nil, &out); err != nil {
		return nil, err
	}
	vols := make([]providers.Volume, 0, len(out.Blocks))
	for _, b := range out.Blocks {
		vols = append(vols, providers.Volume{Provider: "vultr", ID: b.ID, Name: b.Label, DataCenter: b.Region, Size: b.SizeGB})
	}
	return vols, nil
}

// CreateVolume creates a block storage volume in region dc, NVMe
// ("high_perf") unless providers.vultr.block_type says "storage_opt". It is
// created blank; the first box to boot on it formats it.
func (p *Provider) CreateVolume(ctx context.Context, name string, sizeGiB int, dc string) (*providers.Volume, error) {
	req := map[string]any{
		"region": dc, "size_gb": sizeGiB, "label": name,
		"block_type": cmp.Or(p.settings().BlockType, "high_perf"),
	}
	var out struct {
		Block block `json:"block"`
	}
	if err := p.do(ctx, "POST", "/blocks", req, &out); err != nil {
		return nil, err
	}
	return &providers.Volume{Provider: "vultr", ID: out.Block.ID, Name: out.Block.Label, DataCenter: out.Block.Region, Size: out.Block.SizeGB}, nil
}

// DeleteVolume deletes a volume by id. Vultr refuses one still attached.
func (p *Provider) DeleteVolume(ctx context.Context, id string) error {
	return p.do(ctx, "DELETE", "/blocks/"+url.PathEscape(id), nil, nil)
}
