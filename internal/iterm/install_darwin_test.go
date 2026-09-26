//go:build darwin

package iterm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallSeedAndLoad(t *testing.T) {
	store := t.TempDir()
	dynamic := t.TempDir()
	t.Setenv("MEGH_ITERM_DYNAMIC_PROFILES_DIR", dynamic)

	set := Settings{Profile: "megh", Auto: true, StoreDir: store}
	if err := Install(set); err != nil {
		t.Fatal(err)
	}
	if !ProfileLoaded("megh") {
		t.Fatal("megh profile not loaded into dynamic profiles dir")
	}
	path := filepath.Join(store, "megh.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("store file: %v", err)
	}
}
