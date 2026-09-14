// Package docker is the megh local backend: a box is a container on this
// machine rather than a rented pod. It exists for two reasons. A local box is
// free and instant, so the box contract (the entrypoint's persist/symlink/sshd
// behavior, the feature scripts, hydrate) can be exercised without paying a
// provider. And a container whose work trees are bind-mounted from the host is
// the containerized-agent setup people actually want: one Linux userland, the
// real repos, and a tool login that persists separately from the host's.
//
// It shells out to the `docker` CLI rather than taking an SDK dependency, the
// same way internal/registry talks to OCI registries with stdlib only.
//
// A local box never joins the tailnet. Over loopback the tailnet buys nothing,
// and skipping it means megh mints no node key and leaves no node behind.
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/providers"
)

// managedLabel marks a container as megh's. Docker has real labels, which
// RunPod does not, so this is the durable filter rather than the name. The
// megh- name prefix stays anyway: C1's display and lookup rules are shared code
// keyed on it, and a container named megh-work also reads correctly in a plain
// `docker ps`.
const managedLabel = "megh.managed"

// tsAuthKeyEnv is the node auth key the entrypoint keys its tailscale bring-up
// off. Named rather than inlined so the exclusion below reads as a decision.
const tsAuthKeyEnv = "TS_AUTHKEY"

// workMount is where the box's scratch lives inside the container. It matches
// what RunPod mounts a network volume at, so the entrypoint's WORK_MOUNT
// handling is identical on both backends.
const workMount = "/workspace"

// settings are the resolved `providers.docker` block. Unlike RunPod, whose
// configuration is a credential read from the environment per call, this
// backend is configured with paths on this machine.
type settings struct {
	image      string
	workDir    string
	volumeRoot string
	mounts     map[string]string
}

// Provider is the local docker backend.
//
// It holds a config ACCESSOR rather than a config value, because the two are
// available at different times: backends are registered from an init() so the
// list of them is visible in one place, and megh.yaml is not loaded until
// PersistentPreRunE. Capturing the value at registration would capture the
// empty default, and every local box would launch with no mounts and the wrong
// image while looking perfectly configured.
type Provider struct {
	cfg func() config.Config
}

// New builds the backend. The accessor is read on each call, after the config
// has loaded.
func New(cfg func() config.Config) *Provider { return &Provider{cfg: cfg} }

func (p *Provider) settings() settings {
	c := p.cfg().Provider("docker")
	return settings{
		image:      c.Image,
		workDir:    orDefault(c.WorkDir, "~/.megh/volumes/local"),
		volumeRoot: orDefault(c.VolumeRoot, "~/.megh/volumes"),
		mounts:     c.Mounts,
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

var _ providers.Provider = (*Provider)(nil)

func (*Provider) Name() string { return "docker" }

// Tailnet is false: see the package comment.
func (*Provider) Tailnet() bool { return false }

// Result is a launched local box.
type Result struct {
	ID      string
	Name    string
	SSHPort int
}

// Summary is the connection info after `megh up`. It deliberately does not
// print tailnet URLs the way the RunPod summary does, because a local box has
// no tailnet: its web surfaces bind loopback INSIDE the container and only 22 is
// published, so `megh browse` tunnelling over SSH is the only path to them.
func (r *Result) Summary() string {
	return fmt.Sprintf(`box created: %s (container %s)

Access:
  ssh       : megh ssh %[1]s
  surfaces  : megh browse %[1]s        (ttyd, webterm, code-server over an SSH tunnel)

No tailnet: a local box is reached over loopback, so no node key was minted and
no node was added. The surfaces still need the tunnel: they bind the box's own
loopback (C4), which a published port cannot reach. Only 22/tcp is published,
and only on 127.0.0.1.
`, r.Name, shortID(r.ID))
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// Up creates and starts a container from the megh dev-env image.
//
// Resources are not requested: a container shares the host's CPU and memory, so
// Options.VCPU/RAMGiB/DiskGiB are meaningless here rather than merely
// unsupported. DataCenter and VolumeID are likewise ignored; the work mount is
// a host directory from config.
func (p *Provider) Up(ctx context.Context, o providers.Options) (providers.Result, error) {
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	set := p.settings()
	image := set.image
	if o.Image != "" && set.image == "" {
		image = o.Image
	}
	if image == "" {
		return nil, fmt.Errorf("no image: set providers.docker.image in megh.yaml (build one with `make image-local`)")
	}
	name := providers.PrefixName(o.Name)

	work := config.ExpandPath(set.workDir)
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, fmt.Errorf("work dir %s: %w", work, err)
	}

	args, err := runArgs(set, name, image, work, o)
	if err != nil {
		return nil, err
	}
	out, err := run(ctx, args...)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(out)

	box, err := p.inspect(ctx, id)
	if err != nil {
		return nil, err
	}
	return &Result{ID: id, Name: providers.ShortName(name), SSHPort: box.SSHPort}, nil
}

// runArgs builds the full `docker run` argv. Split out from Up so it can be
// asserted without a daemon: what this backend does to a box IS its argv, and a
// test that cannot read the argv cannot check C3.
func runArgs(set settings, name, image, work string, o providers.Options) ([]string, error) {
	args := []string{
		"run", "-d",
		"--name", name,
		"--hostname", providers.ShortName(name),
		"--label", managedLabel + "=1",
		// Everything is published on 127.0.0.1 ONLY, so a box is never on the
		// machine's network interfaces (CONSTRAINTS.md C4). Docker picks each host
		// port; megh reads them back rather than tracking any, which keeps megh
		// stateless with the daemon as the source of truth.
		"-p", "127.0.0.1::22",
		"-v", work + ":" + workMount,
	}

	// The web surfaces are deliberately NOT published, and publishing them would
	// not work anyway. Measured on a running box:
	//
	//   sshd   0.0.0.0:22
	//   ttyd   127.0.0.1:7681
	//
	// C4 requires every box service to bind the box's loopback, and a docker
	// publish forwards to the container's eth0, not to its loopback, so a
	// published :7681 accepts the connection on the host and finds nothing to
	// forward it to. sshd works only because it is the one service that binds
	// 0.0.0.0. Reaching a surface is therefore an SSH tunnel here exactly as it
	// is on a cloud box, which is what `megh browse` does.

	mounts, err := ParseMounts(set.mounts, workMount)
	if err != nil {
		return nil, err
	}
	for _, m := range mounts {
		args = append(args, "-v", m.Arg())
	}

	// The box env. TS_AUTHKEY is deliberately absent rather than empty: the
	// entrypoint branches on it being set, so leaving it out is what makes the
	// box skip tailscale bring-up entirely.
	env := map[string]string{
		"PUBLIC_KEY": o.PubKey,
		"WORK_MOUNT": workMount,
		"ARCH_TAG":   archTag(),
	}
	for k, v := range o.ExtraEnv {
		// Two different reasons, both deliberate. A control-plane credential must
		// never reach ANY box on ANY backend (C5). A node auth key is legitimate
		// on a cloud box and pointless here, and worse than pointless: the
		// entrypoint branches on the variable being SET, so passing it even empty
		// would take the tailscale bring-up path with nothing to authenticate.
		if config.IsControlPlaneSecret(k) || k == tsAuthKeyEnv {
			continue
		}
		env[k] = v
	}
	for _, k := range sortedKeys(env) {
		args = append(args, "-e", k+"="+env[k])
	}
	return append(args, image), nil
}

// archTag matches what the entrypoint expects for the arch-tagged cache
// directory on the work mount. It is the HOST's architecture because the image
// is built for it; a box run under emulation would need this overridden.
func archTag() string {
	if runtime.GOARCH == "arm64" {
		return "aarch64"
	}
	return "x86_64"
}

// sortedKeys keeps the argv deterministic, which is what makes it assertable
// in a test and diffable when a launch misbehaves.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// List returns every megh-managed container, running or not. Stopped boxes are
// included so `megh list` can show one and `megh down` can remove it.
func (p *Provider) List(ctx context.Context) ([]providers.Box, error) {
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	out, err := run(ctx, "ps", "-a", "--filter", "label="+managedLabel+"=1", "--format", "{{.ID}}")
	if err != nil {
		return nil, err
	}
	var boxes []providers.Box
	for _, id := range strings.Fields(out) {
		b, err := p.inspect(ctx, id)
		if err != nil {
			return nil, err
		}
		boxes = append(boxes, *b)
	}
	return boxes, nil
}

// Terminate removes a container. The work directory on the host is untouched,
// mirroring RunPod where terminating a pod leaves its network volume alone.
func (p *Provider) Terminate(ctx context.Context, id string) error {
	if err := p.check(ctx); err != nil {
		return err
	}
	_, err := run(ctx, "rm", "-f", id)
	return err
}

// inspectOut is the slice of `docker inspect` megh reads. Keeping it a named
// type documents the contract with the CLI's JSON, which is the one place this
// backend depends on docker's output shape.
type inspectOut struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Status string `json:"Status"`
	} `json:"State"`
	NetworkSettings struct {
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
	} `json:"NetworkSettings"`
}

func (p *Provider) inspect(ctx context.Context, id string) (*providers.Box, error) {
	out, err := run(ctx, "inspect", id)
	if err != nil {
		return nil, err
	}
	return parseInspect(out)
}

// parseInspect turns `docker inspect` output into a Box. Separate from the
// exec so it can be tested against a recorded fixture.
func parseInspect(out string) (*providers.Box, error) {
	var got []inspectOut
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		return nil, fmt.Errorf("docker: parse inspect: %w", err)
	}
	if len(got) == 0 {
		return nil, fmt.Errorf("docker: inspect returned nothing")
	}
	c := got[0]
	b := &providers.Box{
		ID:         c.ID,
		Name:       strings.TrimPrefix(c.Name, "/"),
		Status:     strings.ToUpper(c.State.Status),
		DataCenter: "local",
		Image:      c.Config.Image,
	}
	// A stopped container has no published port, which is not an error: it is
	// how `megh list` shows a box that exists but is not up.
	for _, binding := range c.NetworkSettings.Ports["22/tcp"] {
		port, err := strconv.Atoi(binding.HostPort)
		if err != nil {
			continue
		}
		b.PublicIP = binding.HostIP
		if b.PublicIP == "0.0.0.0" || b.PublicIP == "" {
			b.PublicIP = "127.0.0.1"
		}
		b.SSHPort = port
		break
	}
	return b, nil
}

// check fails early and legibly when docker is missing or its daemon is down,
// rather than letting every subcommand surface a raw exec error.
func (p *Provider) check(ctx context.Context) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker is not on PATH (the local backend needs Docker Desktop or a docker CLI)")
	}
	if _, err := run(ctx, "info", "--format", "{{.ServerVersion}}"); err != nil {
		return fmt.Errorf("the docker daemon is not reachable: %w", err)
	}
	return nil
}

// run executes docker and returns stdout, folding stderr into the error so a
// failure says what docker actually complained about.
func run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("docker %s: %s", args[0], msg)
	}
	return string(out), nil
}

// volumePath is the host directory backing a named local volume.
func (p *Provider) volumePath(name string) string {
	return filepath.Join(config.ExpandPath(p.settings().volumeRoot), name)
}
