//go:build darwin

package iterm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Load pushes one or more saved profiles from storeDir into iTerm DynamicProfiles.
// Names empty loads every *.json in storeDir.
func Load(storeDir string, names ...string) ([]string, error) {
	if err := os.MkdirAll(dynamicProfilesDir(), 0o755); err != nil {
		return nil, err
	}
	var targets []string
	if len(names) == 0 {
		entries, err := os.ReadDir(storeDir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("profile store %s is empty or missing; run `megh iterm save <name>` or `megh iterm install`", storeDir)
			}
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
				pn, err := profileNamesInFile(filepath.Join(storeDir, e.Name()))
				if err != nil {
					return nil, err
				}
				targets = append(targets, pn...)
			}
		}
	} else {
		targets = append(targets, names...)
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no profiles to load from %s", storeDir)
	}
	var loaded []string
	for _, name := range targets {
		path, err := StoreFileForName(storeDir, name)
		if err != nil {
			return loaded, err
		}
		if err := copyToDynamicProfiles(path, name); err != nil {
			return loaded, err
		}
		loaded = append(loaded, name)
	}
	return loaded, nil
}

func copyToDynamicProfiles(storePath, profileName string) error {
	b, err := os.ReadFile(storePath)
	if err != nil {
		return err
	}
	dest := filepath.Join(dynamicProfilesDir(), dynamicProfileDestName(profileName))
	return os.WriteFile(dest, b, 0o644)
}

// DynamicExportPath is the JSON file megh writes under iTerm's DynamicProfiles dir.
func DynamicExportPath(name string) string {
	return filepath.Join(dynamicProfilesDir(), dynamicProfileDestName(name))
}

// ProfileLoaded reports whether megh's DynamicProfiles export file exists on disk.
// iTerm reloads profiles from that folder; deleting a profile in Settings often
// does not remove this file, so list also checks ProfileInITerm.
func ProfileLoaded(name string) bool {
	path := DynamicExportPath(name)
	if _, err := os.Stat(path); err != nil {
		return false
	}
	names, err := profileNamesInFile(path)
	return err == nil && len(names) > 0 && names[0] == name
}

// ProfileInITerm reports whether iTerm's preferences currently list the profile.
func ProfileInITerm(name string) bool {
	_, err := ReadITermProfile(name)
	return err == nil
}

// Unload removes megh's DynamicProfiles export(s). The git-backed store is unchanged.
func Unload(names ...string) ([]string, error) {
	var targets []string
	if len(names) == 0 {
		entries, err := os.ReadDir(dynamicProfilesDir())
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasPrefix(e.Name(), "megh-") || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			path := filepath.Join(dynamicProfilesDir(), e.Name())
			pns, err := profileNamesInFile(path)
			if err != nil || len(pns) == 0 {
				continue
			}
			targets = append(targets, pns[0])
		}
	} else {
		targets = append(targets, names...)
	}
	var removed []string
	for _, name := range targets {
		path := DynamicExportPath(name)
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, fmt.Errorf("remove %s: %w", path, err)
		}
		removed = append(removed, name)
	}
	return removed, nil
}

// SeedDefaultStore writes the built-in megh profile template when the store has no file for name.
func SeedDefaultStore(storeDir, name string) (string, error) {
	if _, err := StoreFileForName(storeDir, name); err == nil {
		return "", nil
	}
	return WriteStoreFile(storeDir, name, profileSpec(name))
}
