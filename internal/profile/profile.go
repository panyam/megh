// Package profile manages megh profiles: self-contained contexts each holding a
// dedicated box-access SSH key, a set of GitHub identity keys, and secret
// values. megh depends on nothing at the system level (not your ~/.ssh keys,
// not a shared agent that might also hold a corporate key). Blast the profile
// dir and you lose only that profile's VM access, which is re-mintable;
// everything else is in git.
//
// Keys are generated in-process via oneauth/sshkeys (no ssh-keygen dependency).
// Storage is plain 0600 files for now; oneauth/keys.EncryptedKeyStorage is the
// intended drop-in for encryption at rest in a later secure-storage phase.
package profile

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/panyam/oneauth/sshkeys"
)

// Profile is a named context under ~/.megh/profiles/<name>/.
type Profile struct {
	Name string
	Dir  string
}

// Root is ~/.megh (override with $MEGH_HOME, e.g. ./.megh for repo-local use).
func Root() string {
	if v := os.Getenv("MEGH_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".megh")
}

func profilesDir() string { return filepath.Join(Root(), "profiles") }

// ActiveName resolves the active profile: explicit > $MEGH_PROFILE >
// ~/.megh/current > "default".
func ActiveName(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if v := os.Getenv("MEGH_PROFILE"); v != "" {
		return v
	}
	if b, err := os.ReadFile(filepath.Join(Root(), "current")); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	return "default"
}

// Get returns the profile for name if its directory exists.
func Get(name string) (*Profile, bool) {
	dir := filepath.Join(profilesDir(), name)
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		return &Profile{Name: name, Dir: dir}, true
	}
	return nil, false
}

// --- paths --------------------------------------------------------------------

// BoxKeyFile is the single key used to SSH INTO the VMs (its pubkey is injected).
func (p *Profile) BoxKeyFile() string    { return filepath.Join(p.Dir, "box.key") }
func (p *Profile) BoxPubKeyFile() string { return filepath.Join(p.Dir, "box.key.pub") }

func (p *Profile) ghDir() string                   { return filepath.Join(p.Dir, "gh") }
func (p *Profile) GHKeyFile(name string) string    { return filepath.Join(p.ghDir(), name+".key") }
func (p *Profile) GHPubKeyFile(name string) string { return filepath.Join(p.ghDir(), name+".key.pub") }
func (p *Profile) SecretsFile() string             { return filepath.Join(p.Dir, "secrets.env") }

// GHKeyNames lists the GitHub identities (gh/<name>.key) in the profile.
func (p *Profile) GHKeyNames() []string {
	entries, err := os.ReadDir(p.ghDir())
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".key") {
			names = append(names, strings.TrimSuffix(e.Name(), ".key"))
		}
	}
	sort.Strings(names)
	return names
}

// GHKeyFiles returns the private-key paths of all GitHub identities, for
// forwarding through the scoped agent.
func (p *Profile) GHKeyFiles() []string {
	names := p.GHKeyNames()
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, p.GHKeyFile(n))
	}
	return out
}

// HasGHKey reports whether a GitHub identity exists.
func (p *Profile) HasGHKey(name string) bool {
	_, err := os.Stat(p.GHKeyFile(name))
	return err == nil
}

// --- lifecycle ----------------------------------------------------------------

func writeKeyPair(keyFile string) error {
	pub, priv, err := sshkeys.GenerateEd25519()
	if err != nil {
		return err
	}
	if err := os.WriteFile(keyFile, priv, 0o600); err != nil {
		return err
	}
	return os.WriteFile(keyFile+".pub", pub, 0o644)
}

// Create makes a new profile with a dedicated box keypair and a secrets template.
// GitHub identities are added separately with AddGHKey.
func Create(name string) (*Profile, error) {
	dir := filepath.Join(profilesDir(), name)
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("profile %q already exists at %s", name, dir)
	}
	p := &Profile{Name: name, Dir: dir}
	if err := os.MkdirAll(p.ghDir(), 0o700); err != nil {
		return nil, err
	}
	if err := writeKeyPair(p.BoxKeyFile()); err != nil {
		return nil, fmt.Errorf("box key: %w", err)
	}
	if err := os.WriteFile(p.SecretsFile(), []byte(secretsTemplate), 0o600); err != nil {
		return nil, err
	}
	return p, nil
}

// AddGHKey generates a new GitHub identity key in the profile.
func (p *Profile) AddGHKey(name string) error {
	if p.HasGHKey(name) {
		return fmt.Errorf("gh key %q already exists in profile %q", name, p.Name)
	}
	if err := os.MkdirAll(p.ghDir(), 0o700); err != nil {
		return err
	}
	return writeKeyPair(p.GHKeyFile(name))
}

// List returns the names of all profiles.
func List() []string {
	entries, err := os.ReadDir(profilesDir())
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// Use sets the active profile.
func Use(name string) error {
	if _, ok := Get(name); !ok {
		return fmt.Errorf("no profile %q (create it: megh profile create %s)", name, name)
	}
	if err := os.MkdirAll(Root(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(Root(), "current"), []byte(name+"\n"), 0o600)
}

// LoadSecrets applies non-empty values from secrets.env into the process
// environment, overriding ambient values for the vars it defines. Vars it omits
// fall back to the ambient environment; empty values are skipped so a blank
// template never clobbers an ambient value.
func (p *Profile) LoadSecrets() error {
	f, err := os.Open(p.SecretsFile())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if k != "" && v != "" {
			os.Setenv(k, v)
		}
	}
	return sc.Err()
}

const secretsTemplate = `# Secrets for this megh profile (values; kept 0600; never committed).
# megh applies non-empty values here into the environment, overriding ambient
# values for the vars named. Vars left blank fall back to your ambient env, so
# you can migrate gradually.
export RUNPOD_API_KEY=
export GH_MEGH_TOKEN=
export TS_AUTHKEY=
export MEGH_SESSIONS_TOKEN=
`
