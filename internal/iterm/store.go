package iterm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type dynamicProfilesDoc struct {
	Profiles []map[string]any `json:"Profiles"`
}

// StorePath is a saved dynamic-profile JSON file under the megh config tree.
type StorePath struct {
	Name string // iTerm profile name (Profiles[0].Name)
	Path string // absolute path to the JSON file
}

// ListStore returns every profile JSON in dir (one or more Profiles per file).
func ListStore(dir string) ([]StorePath, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []StorePath
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		names, err := profileNamesInFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		for _, n := range names {
			out = append(out, StorePath{Name: n, Path: path})
		}
	}
	return out, nil
}

func profileNamesInFile(path string) ([]string, error) {
	doc, err := readProfileDoc(path)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, p := range doc.Profiles {
		if n, ok := p["Name"].(string); ok && n != "" {
			names = append(names, n)
		}
	}
	return names, nil
}

func readProfileDoc(path string) (*dynamicProfilesDoc, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc dynamicProfilesDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

// StoreFileForName finds the JSON file in dir that defines the named profile.
func StoreFileForName(dir, name string) (string, error) {
	list, err := ListStore(dir)
	if err != nil {
		return "", err
	}
	for _, s := range list {
		if s.Name == name {
			return s.Path, nil
		}
	}
	return "", fmt.Errorf("no saved iTerm profile %q under %s (try `megh iterm save %s`)", name, dir, name)
}

// WriteStoreFile writes a dynamic profile document for one profile name.
func WriteStoreFile(dir, name string, profile map[string]any) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	profile["Name"] = name
	doc := dynamicProfilesDoc{Profiles: []map[string]any{profile}}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	b = append(b, '\n')
	path := filepath.Join(dir, storeFileName(name))
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func storeFileName(profileName string) string {
	s := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, profileName)
	if s == "" {
		s = "profile"
	}
	return s + ".json"
}

// dynamicProfileDestName is the filename megh uses under iTerm's DynamicProfiles dir.
func dynamicProfileDestName(profileName string) string {
	return "megh-" + storeFileName(profileName)
}
