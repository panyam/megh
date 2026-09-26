//go:build darwin

package iterm

import "fmt"

// Install seeds the built-in template when missing, then load → iTerm.
func Install(set Settings) error {
	if set.StoreDir == "" {
		return fmt.Errorf("no iTerm profile store directory (megh.yaml iterm.dir or a discovered config path)")
	}
	name := set.profileName()
	if _, err := SeedDefaultStore(set.StoreDir, name); err != nil {
		return err
	}
	_, err := Load(set.StoreDir, name)
	return err
}
