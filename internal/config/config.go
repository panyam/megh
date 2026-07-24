package config

import (
	"fmt"
	"os"
)

// Registry is one OCI registry megh can inspect or pull dev-env images from.
type Registry struct {
	Name      string // short handle, e.g. "ghcr"
	Host      string // e.g. "ghcr.io"
	Namespace string // owner/org, e.g. "panyam"
	Username  string // auth username for private pulls
	TokenEnv  string // env var holding the auth token
}

// Token returns the registry token from its configured env var, if any.
func (r Registry) Token() string { return os.Getenv(r.TokenEnv) }

// Config is the resolved megh configuration.
type Config struct {
	Registries []Registry
	Flavors    []string // dev-env flavors, imaged as megh-<flavor>
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Default builds config from the environment with sane defaults. GHCR is the
// interim registry; the self-hosted Forgejo registry gets appended here once it
// exists, and every megh registry command then covers both.
func Default() Config {
	return Config{
		Registries: []Registry{
			{
				Name:      "ghcr",
				Host:      "ghcr.io",
				Namespace: env("MEGH_GHCR_NAMESPACE", "panyam"),
				Username:  env("MEGH_GHCR_USER", "panyam"),
				TokenEnv:  "GH_MEGH_TOKEN",
			},
		},
		Flavors: []string{"base"},
	}
}

// DefaultImage returns the conventional dev-env image for a flavor, from the
// first configured registry: <host>/<namespace>/megh-<flavor>:latest. This is
// what `megh up` uses when neither --image nor $MEGH_IMAGE is set.
func (c Config) DefaultImage(flavor string) string {
	if flavor == "" {
		flavor = "base"
	}
	if len(c.Registries) == 0 {
		return ""
	}
	r := c.Registries[0]
	return fmt.Sprintf("%s/%s/megh-%s:latest", r.Host, r.Namespace, flavor)
}

// Find returns the named registry, or the first configured one when name is
// empty.
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
