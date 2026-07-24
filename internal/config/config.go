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
}

// Tailscale points at the env var holding the (optional) Tailscale auth key.
type Tailscale struct {
	AuthKeyEnv string `yaml:"authkey_env"`
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
	SSHPubKeyFile   string              `yaml:"ssh_pubkey_file"`
	Registries      []Registry          `yaml:"registries"`
	Flavors         []string            `yaml:"flavors"`
	Providers       map[string]Provider `yaml:"providers"`
	Tailscale       Tailscale           `yaml:"tailscale"`
	Sessions        Sessions            `yaml:"sessions"`
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
		DefaultFlavor:   "base",
		SSHPubKeyFile:   "~/.ssh/id_ed25519.pub",
		Registries: []Registry{{
			Name:      "ghcr",
			Host:      "ghcr.io",
			Namespace: env("MEGH_GHCR_NAMESPACE", "panyam"),
			Username:  env("MEGH_GHCR_USER", "panyam"),
			TokenEnv:  "GH_MEGH_TOKEN",
		}},
		Flavors: []string{"base"},
		Providers: map[string]Provider{
			"runpod": {APIKeyEnv: "RUNPOD_API_KEY", VCPU: 2, RAM: 8, Disk: 20},
		},
		Tailscale: Tailscale{AuthKeyEnv: "TS_AUTHKEY"},
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
		flavor = "base"
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
