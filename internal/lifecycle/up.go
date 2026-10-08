package lifecycle

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/panyam/megh/internal/providers"
	"github.com/panyam/megh/internal/tsapi"
)

// UpRequest is a launch as the caller resolved it. Zero fields fall back to
// megh.yaml, then to built-in defaults, so the CLI fills in only what its flags
// and environment said and a server fills in only what its form said.
type UpRequest struct {
	Name                  string // as typed; the megh- marker is applied here
	Provider              string // "" = default_provider, then runpod
	Flavor                string // "" = default_flavor, then slim; picks the default image
	Image                 string
	VolumeID              string
	DataCenter            string
	VCPU, RAMGiB, DiskGiB int
	ExposeSSH             *bool // nil = the provider's expose_ssh setting
	// PubKey is the launching machine's own key, if it has one. A server has
	// none and relies on extra_pubkeys; a launch with no key at all is refused,
	// because the box would come up with nothing in authorized_keys.
	PubKey string
	// BoxEnv is copied into the box env (the CLI passes megh.yaml's box_envs,
	// read from its own environment).
	BoxEnv map[string]string
	// PullToken is the registry token for a VM backend's image pull (see
	// providers.Options.PullToken). The CLI reads it from registries[0]'s
	// token_env; a server takes it from the request. Empty means no login.
	PullToken string
	// Type is the exact machine to create, as a providers.Offer names it; empty
	// means the cheapest meeting VCPU/RAMGiB/DiskGiB.
	Type string
}

// Up launches a box, or starts it again when a backend that can restart
// stopped boxes still has one by that name. A running box, or a stopped one
// the backend cannot restart, is refused: the name is the box's tailnet
// hostname and two boxes cannot share it.
func (s *Service) Up(ctx context.Context, r UpRequest) (providers.Result, error) {
	prov, err := s.Provider(r.Provider)
	if err != nil {
		return nil, err
	}
	p := s.Config.Provider(prov.Name())
	flavor := cmp.Or(r.Flavor, s.Config.DefaultFlavor, "slim")
	o := providers.Options{
		Name:       providers.PrefixName(r.Name),
		DataCenter: cmp.Or(r.DataCenter, p.DefaultDC),
		VolumeID:   cmp.Or(r.VolumeID, p.DefaultVolume),
		Image:      cmp.Or(r.Image, s.Config.DefaultImage(flavor)),
		VCPU:       firstPositive(r.VCPU, p.VCPU, 2),
		RAMGiB:     firstPositive(r.RAMGiB, p.RAM, 8),
		DiskGiB:    firstPositive(r.DiskGiB, p.Disk, 20),
		ExposeSSH:  p.PublicSSH(),
	}
	if r.ExposeSSH != nil {
		o.ExposeSSH = *r.ExposeSSH
	}
	if o.PubKey, err = withExtraPubKeys(r.PubKey, s.Config.ExtraPubKeys); err != nil {
		return nil, err
	}
	if o.PubKey == "" {
		return nil, errors.New("no SSH public key to authorize on the box; pass one or set extra_pubkeys in megh.yaml")
	}
	o.ExtraEnv = s.boxEnv(r.BoxEnv)
	o.PullToken = r.PullToken
	o.Type = r.Type

	// Names double as the Tailscale hostname, so refuse a duplicate before
	// launching rather than let two boxes fight over one tailnet name.
	boxes, err := prov.List(ctx)
	if err != nil {
		return nil, err
	}
	short := providers.ShortName(o.Name)
	for _, b := range providers.Managed(boxes) {
		if b.Name != o.Name && b.DisplayName() != short {
			continue
		}
		if b.Status == "RUNNING" {
			return nil, fmt.Errorf("box %q is already running (id %s); use `megh ssh %s`", short, b.ID, short)
		}
		if starter, ok := prov.(providers.StoppedBoxStarter); ok && BoxStopped(b.Status) {
			return starter.StartStopped(ctx, b.ID)
		}
		return nil, fmt.Errorf("a box named %q already exists (id %s, status %s); pick another name or `megh down %s` first",
			short, b.ID, b.Status, short)
	}
	o.TSAuthKey = s.BootAuthKey(ctx, prov.Mesh(), short)
	return prov.Up(ctx, o)
}

// boxEnv is the caller's box env plus the persist and symlink maps the
// entrypoint applies on boot.
func (s *Service) boxEnv(base map[string]string) map[string]string {
	env := maps.Clone(base)
	set := func(k, v string) {
		if env == nil {
			env = map[string]string{}
		}
		env[k] = v
	}
	// Tool state (logins/config) to persist on the volume across rebuilds; the
	// entrypoint symlinks each onto the volume. Empty -> entrypoint default.
	if len(s.Config.Persist) > 0 {
		set("MEGH_PERSIST", strings.Join(s.Config.Persist, ","))
	}
	// Home->volume path maps (e.g. ~/projects -> repos/projects) so local paths
	// work on the box. Passed as MEGH_SYMLINKS ("link:target,..."); order-free.
	if len(s.Config.Symlinks) > 0 {
		var pairs []string
		for link, target := range s.Config.Symlinks {
			pairs = append(pairs, link+":"+target)
		}
		set("MEGH_SYMLINKS", strings.Join(pairs, ","))
	}
	return env
}

func firstPositive(vals ...int) int {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

// BoxStopped reports whether a provider status means the box exists but is
// not running, which is the case a StoppedBoxStarter can resume.
func BoxStopped(status string) bool {
	switch status {
	case "EXITED", "CREATED", "PAUSED", "DEAD":
		return true
	default:
		return false
	}
}

// withExtraPubKeys appends megh.yaml's extra_pubkeys to the launching machine's
// key, one per line, which is what the entrypoint writes to authorized_keys.
// Duplicates (same key body) are dropped. An entry that is not a public key is
// an error rather than a skip: the likeliest mistake is pasting the PRIVATE half,
// and that must never ride along into a box's env.
func withExtraPubKeys(primary string, extra []string) (string, error) {
	keys := []string{}
	seen := map[string]bool{}
	add := func(k string) {
		f := strings.Fields(k)
		if len(f) < 2 || seen[f[1]] {
			return
		}
		seen[f[1]] = true
		keys = append(keys, strings.TrimSpace(k))
	}
	for _, line := range strings.Split(primary, "\n") {
		add(line)
	}
	for i, k := range extra {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if !looksLikePubKey(k) {
			return "", fmt.Errorf("megh.yaml extra_pubkeys[%d] is not an SSH public key (want e.g. \"ssh-ed25519 AAAA... comment\"); never put a private key here", i)
		}
		add(k)
	}
	return strings.Join(keys, "\n"), nil
}

// looksLikePubKey is a shape check, not a parse: a known key type, then a
// base64 blob, and nothing that says PRIVATE.
func looksLikePubKey(k string) bool {
	if strings.Contains(k, "PRIVATE") || strings.Contains(k, "\n") {
		return false
	}
	f := strings.Fields(k)
	if len(f) < 2 {
		return false
	}
	t := f[0]
	if !strings.HasPrefix(t, "ssh-") && !strings.HasPrefix(t, "ecdsa-sha2-") && !strings.HasPrefix(t, "sk-") {
		return false
	}
	_, err := base64.StdEncoding.DecodeString(f[1])
	return err == nil
}

// mintKeyExpiry bounds how long a freshly minted key can be redeemed, not how
// long the box lives. The entrypoint runs `tailscale up` during boot, well
// inside this, and a key that outlives its box is exactly what we are getting
// away from.
const mintKeyExpiry = 15 * time.Minute

// BootAuthKey returns the node key to place in the box's create-time env.
//
// Only a backend that joins at boot gets one. A local box is joined afterwards
// over SSH (`megh mesh join`), so minting here would spend a single-use key on
// nothing and write a credential into the container's stored env, which
// `docker inspect` then keeps for the life of the box.
//
// Best effort even when it does apply: a failure warns and falls back rather
// than blocking a launch, because the control machine reaches a box over public
// SSH and the mesh is the convenience layer on top.
func (s *Service) BootAuthKey(ctx context.Context, m providers.Mesh, box string) string {
	if !m.AtBoot {
		return ""
	}
	return s.MintAuthKey(ctx, box)
}

// MintAuthKey returns a Tailscale node key minted for this box alone, or "" to
// mean "use the ambient static TS_AUTHKEY".
//
// Why bother, when a shared reusable key already works. Three things get fixed
// at once. The key is ephemeral, so Tailscale removes the node on its own once
// the box is gone, which is the root cause of names drifting to <name>-1 (a
// persistent node survives even a clean logout). The key is single-use and
// short-lived, so the copy sitting in the pod's env is worthless minutes later,
// where the shared key stays valid for its full 90 days. And it is minted at
// launch, which makes the most common real failure in this project impossible:
// a box booted with a key older than the one the tailnet now expects.
//
// Every failure path here is a warning and a fallback, never an error. Boxes
// must keep launching when the tailnet is misconfigured, unreachable, or simply
// not set up, because the control machine reaches a box over public SSH.
func (s *Service) MintAuthKey(ctx context.Context, box string) string {
	if !s.Config.Tailscale.MintKeys {
		return ""
	}
	c, err := s.tsClient()
	if err != nil {
		fmt.Fprintf(s.errOut(), "megh: %v\n", err)
		s.warnNoMintedKey()
		return ""
	}
	var tags []string
	if s.Config.Tailscale.Tag != "" {
		tags = []string{s.Config.Tailscale.Tag}
	}
	key, err := c.MintAuthKey(ctx, tsapi.AuthKeyOptions{Box: box, Tags: tags, Expiry: mintKeyExpiry})
	if err != nil {
		// The overwhelmingly likely cause is the tag not existing in the tailnet
		// ACL yet, since Tailscale requires one on OAuth-minted keys and rejects
		// a tag with no tagOwners entry. Say so rather than echoing a bare 4xx.
		fmt.Fprintf(s.errOut(), "megh: could not mint a Tailscale key (%v)\n", err)
		if len(tags) > 0 {
			fmt.Fprintf(s.errOut(), "megh: check that %q exists in the tailnet ACL tagOwners, and that the credential is scoped to it\n", tags[0])
		}
		s.warnNoMintedKey()
		return ""
	}
	fmt.Fprintf(s.out(), "minted a single-use ephemeral tailnet key for %s", box)
	if len(tags) > 0 {
		fmt.Fprintf(s.out(), " (%s)", tags[0])
	}
	fmt.Fprintln(s.out())
	return key
}

func (s *Service) tsClient() (*tsapi.Client, error) {
	if s.Tailscale == nil {
		return nil, errors.New("no Tailscale control-plane credential configured")
	}
	return s.Tailscale()
}

// warnNoMintedKey explains what a box loses when minting did not happen. Once
// TS_AUTHKEY is gone from the environment there is nothing to fall back TO, so
// saying "falling back to the static key" would point at something that does
// not exist. The box still launches either way: the control machine drives it
// over public SSH, and the tailnet is a phone's path.
func (s *Service) warnNoMintedKey() {
	if os.Getenv("TS_AUTHKEY") != "" {
		fmt.Fprintln(s.errOut(), "megh: falling back to the static TS_AUTHKEY")
		return
	}
	fmt.Fprintln(s.errOut(), "megh: no static TS_AUTHKEY either, so this box will not join the tailnet")
	fmt.Fprintln(s.errOut(), "megh: it still launches and `megh ssh` still works over public SSH")
}
