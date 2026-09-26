//go:build darwin

package iterm

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func itermPrefsPlist() string {
	return filepath.Join(os.Getenv("HOME"), "Library", "Preferences", "com.googlecode.iterm2.plist")
}

// ReadITermProfile reads a profile dict from iTerm's preferences by display name.
func ReadITermProfile(name string) (map[string]any, error) {
	bookmarks, err := readITermBookmarks()
	if err != nil {
		return nil, err
	}
	for _, b := range bookmarks {
		if n, _ := b["Name"].(string); n == name {
			return b, nil
		}
	}
	return nil, fmt.Errorf("iTerm2 has no profile named %q (Settings → Profiles)", name)
}

func readITermBookmarks() ([]map[string]any, error) {
	cmd := exec.Command("plutil", "-extract", "New Bookmarks", "json", "-o", "-", itermPrefsPlist())
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read iTerm preferences: %w", err)
	}
	var bookmarks []map[string]any
	if err := json.Unmarshal(out, &bookmarks); err != nil {
		return nil, fmt.Errorf("parse iTerm bookmarks: %w", err)
	}
	return bookmarks, nil
}
