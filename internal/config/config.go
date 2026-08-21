// Package config resolves megh's configuration: built-in defaults overlaid with
// a checked-in megh.yaml. The file holds settings and POINTERS to secrets (env
// var names), never secret values. Secrets stay in the environment so they never
// land in git history.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Registry is one OCI registry megh can inspect or pull dev-env images from.
type Registry struct {
	Name      string `yaml:"name"`
	Host      string `yaml:"host"`
	Namespace string `yaml:"namespace"`
	Username  string `yaml:"username"`
	TokenEnv  string `yaml:"token_env"` // env var holding the auth token
}

// Token returns the registry token from its configured env var, if any.
func (r Registry) Token() string { return os.Getenv(r.TokenEnv) }

// Provider holds per-provider non-secret defaults plus a POINTER to the env var
// that holds its API key. The key value never lives in the config or the repo.
type Provider struct {
	APIKeyEnv     string `yaml:"api_key_env"`
	DefaultDC     string `yaml:"default_dc"`
	DefaultVolume string `yaml:"default_volume"`
	VCPU          int    `yaml:"vcpu"`
	RAM           int    `yaml:"ram"`
	Disk          int    `yaml:"disk"`
	ExposeSSH     *bool  `yaml:"expose_ssh"` // expose public break-glass SSH; nil -> true
}

// PublicSSH reports whether public break-glass SSH (22/tcp) is exposed. Default
// true; set expose_ssh: false to run tailnet-only (zero public ports).
func (p Provider) PublicSSH() bool { return p.ExposeSSH == nil || *p.ExposeSSH }

// Tailscale points at the env vars holding the (optional) Tailscale secrets.
// The two are deliberately distinct. AuthKeyEnv is a NODE auth key and is sent
// to the box so it can join the tailnet. APIKeyEnv is a CONTROL-PLANE token
// that can delete nodes, and it must never reach a box (CONSTRAINTS.md C5).
type Tailscale struct {
	AuthKeyEnv string `yaml:"authkey_env"`
	APIKeyEnv  string `yaml:"api_key_env"`
	// Tag applied to boxes when megh mints their auth key itself. Tailscale
	// REQUIRES a tag on keys minted through an OAuth client, and the tag must
	// already exist in the tailnet's ACL tagOwners. Empty disables per-box
	// minting, falling back to the static AuthKeyEnv.
	Tag string `yaml:"tag"`
	// MintKeys turns per-box auth keys on. When false (or no API key is set),
	// `megh up` passes the static auth key through unchanged.
	MintKeys bool `yaml:"mint_keys"`
}

// Sessions configures the durable, searchable agent-history repo. Repo is not a
// secret; TokenEnv points at the PAT that pushes to it.
type Sessions struct {
	Repo     string `yaml:"repo"`
	TokenEnv string `yaml:"token_env"`
}

// Config is the resolved megh configuration. It contains settings and pointers
// to secrets, never secret values.
type Config struct {
	DefaultProvider string              `yaml:"default_provider"`
	DefaultFlavor   string              `yaml:"default_flavor"`
	SSHPubKeyFile   string              `yaml:"ssh_pubkey_file"` // public key injected into boxes
	SSHKeyFile      string              `yaml:"ssh_key_file"`    // private key; megh forwards ONLY this (scoped agent)
	Registries      []Registry          `yaml:"registries"`
	Flavors         []string            `yaml:"flavors"`
	Providers       map[string]Provider `yaml:"providers"`
	Tailscale       Tailscale           `yaml:"tailscale"`
	Sessions        Sessions            `yaml:"sessions"`
	DefaultGHKey    string              `yaml:"default_gh_key"` // gh identity used by repos that don't set key
	Repos           []Repo              `yaml:"repos"`          // cloned into /mnt/work/repos by `megh hydrate`
	Requires        Requires            `yaml:"requires"`
	Tailnet         string              `yaml:"tailnet"` // MagicDNS suffix (e.g. example.ts.net); for portal surface URLs
	Portal          Portal              `yaml:"portal"`
	Persist         []string            `yaml:"persist"`  // home dirs symlinked to the volume so their state survives rebuilds
	Symlinks        map[string]string   `yaml:"symlinks"` // home path -> volume path (relative to /mnt/work, or absolute); maps repo trees into ~
	Files           map[string]string   `yaml:"files"`    // local path -> box path; copied over SSH (secrets/rc files not in a repo)
}

// Portal configures `megh portal`: a bookmarkable box+URL index (PORTAL.md)
// force-pushed to a branch of a private repo you bookmark on your phone. Empty
// Repo disables it (and the up/down auto-refresh).
type Portal struct {
	Repo   string `yaml:"repo"`   // git URL to push the index to (e.g. git@host:you/megh.git)
	Branch string `yaml:"branch"` // branch to force-push (default "portal")
	Scheme string `yaml:"scheme"` // http or https for the surface links (default http)
}

// Requires declares env vars that must be present on the host. Envs are needed
// by megh itself (a launch is blocked if any is missing). BoxEnvs are needed by
// the repos/services that run ON the box; their host values are copied into the
// box at launch. BoxEnvs must also be present on the host, so they are checked
// too.
type Requires struct {
	Envs    []string `yaml:"envs"`
	BoxEnvs []string `yaml:"box_envs"`
}

// MissingEnvs returns declared required env vars (host + box) that are unset,
// so callers can flag them before launching.
func (c Config) MissingEnvs() []string {
	seen := map[string]bool{}
	var missing []string
	for _, e := range append(append([]string{}, c.Requires.Envs...), c.Requires.BoxEnvs...) {
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		if os.Getenv(e) == "" {
			missing = append(missing, e)
		}
	}
	return missing
}

// BoxEnv returns the host values of box_envs to copy onto a box (only those set).
func (c Config) BoxEnv() map[string]string {
	m := map[string]string{}
	for _, e := range c.Requires.BoxEnvs {
		if v := os.Getenv(e); v != "" {
			m[e] = v
		}
	}
	return m
}

// Repo is a git repo to hydrate onto a box's volume, with the GitHub identity
// (profile gh key name) it authenticates as, and an optional destination path
// under repos/ (for nested layouts). It accepts a plain URL string (uses
// DefaultGHKey, dir derived from the URL) or an object {url, key, dir} in YAML.
type Repo struct {
	URL string `yaml:"url"`
	Key string `yaml:"key"` // gh identity name; empty -> DefaultGHKey
	Dir string `yaml:"dir"` // path under /mnt/work/repos; empty -> basename of URL
}

// UnmarshalYAML accepts a scalar URL or a {url, key, dir} mapping.
func (r *Repo) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		r.URL = node.Value
		return nil
	}
	var m struct {
		URL string `yaml:"url"`
		Key string `yaml:"key"`
		Dir string `yaml:"dir"`
	}
	if err := node.Decode(&m); err != nil {
		return err
	}
	r.URL, r.Key, r.Dir = m.URL, m.Key, m.Dir
	return nil
}

// GHKey returns the repo's GitHub identity, falling back to the config default.
func (c Config) GHKey(r Repo) string {
	if r.Key != "" {
		return r.Key
	}
	return c.DefaultGHKey
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Default is the built-in configuration, used when no megh.yaml is found and as
// the base a found megh.yaml overlays.
func Default() Config {
	return Config{
		DefaultProvider: "runpod",
		DefaultFlavor:   "slim",
		SSHPubKeyFile:   "~/.ssh/id_ed25519.pub",
		Registries: []Registry{{
			Name:      "ghcr",
			Host:      "ghcr.io",
			Namespace: env("MEGH_GHCR_NAMESPACE", "panyam"),
			Username:  env("MEGH_GHCR_USER", "panyam"),
			TokenEnv:  "GH_MEGH_TOKEN",
		}},
		Flavors: []string{"base", "slim"},
		Providers: map[string]Provider{
			"runpod": {APIKeyEnv: "RUNPOD_API_KEY", VCPU: 2, RAM: 8, Disk: 20},
		},
		Tailscale: Tailscale{AuthKeyEnv: "TS_AUTHKEY", APIKeyEnv: "MEGH_TAILSCALE_API_KEY", Tag: "tag:megh"},
		Sessions:  Sessions{TokenEnv: "MEGH_SESSIONS_TOKEN"},
	}
}

// Load returns Default() overlaid with the first megh.yaml found. Search order:
// explicit path (arg or $MEGH_CONFIG), then megh.yaml walking up from the cwd,
// then ~/.config/megh/megh.yaml. A missing file is not an error.
func Load(explicit string) (Config, string, error) {
	c := Default()
	path := findConfig(explicit)
	if path == "" {
		return c, "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return c, path, err
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		return c, path, fmt.Errorf("parse %s: %w", path, err)
	}
	return c, path, nil
}

func findConfig(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if v := os.Getenv("MEGH_CONFIG"); v != "" {
		return v
	}
	if dir, err := os.Getwd(); err == nil {
		for {
			p := filepath.Join(dir, "megh.yaml")
			if _, err := os.Stat(p); err == nil {
				return p
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".config", "megh", "megh.yaml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// Baked into the dev-env image, so megh works on a box out of the box.
	if _, err := os.Stat("/etc/megh/megh.yaml"); err == nil {
		return "/etc/megh/megh.yaml"
	}
	return ""
}

// Provider returns the named provider config (zero value if absent).
func (c Config) Provider(name string) Provider { return c.Providers[name] }

// DefaultImage returns <host>/<namespace>/megh-<flavor>:latest from the first
// registry. Used by `megh up` when neither --image nor $MEGH_IMAGE is set.
func (c Config) DefaultImage(flavor string) string {
	if flavor == "" {
		flavor = c.DefaultFlavor
	}
	if flavor == "" {
		flavor = "slim"
	}
	if len(c.Registries) == 0 {
		return ""
	}
	r := c.Registries[0]
	return fmt.Sprintf("%s/%s/megh-%s:latest", r.Host, r.Namespace, flavor)
}

// Find returns the named registry, or the first configured one when name empty.
func (c Config) Find(name string) (Registry, bool) {
	if name == "" && len(c.Registries) > 0 {
		return c.Registries[0], true
	}
	for _, r := range c.Registries {
		if r.Name == name {
			return r, true
		}
	}
	return Registry{}, false
}

// ExpandPath expands a leading ~ to the user's home directory.
func ExpandPath(p string) string {
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}
