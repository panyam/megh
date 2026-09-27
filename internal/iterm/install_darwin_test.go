//go:build darwin

package iterm

import (
	"os"
	"path/filepath"
	"strings"
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

func TestUnloadRemovesDynamicExport(t *testing.T) {
	store := t.TempDir()
	dynamic := t.TempDir()
	t.Setenv("MEGH_ITERM_DYNAMIC_PROFILES_DIR", dynamic)

	set := Settings{Profile: "megh", StoreDir: store}
	if err := Install(set); err != nil {
		t.Fatal(err)
	}
	if !ProfileLoaded("megh") {
		t.Fatal("expected export after install")
	}
	removed, err := Unload("megh")
	if err != nil || len(removed) != 1 {
		t.Fatalf("Unload: %v %v", removed, err)
	}
	if ProfileLoaded("megh") {
		t.Fatal("export should be gone")
	}
}

func TestBuildReexecShellWrapsLoginZsh(t *testing.T) {
	cmd := buildReexecShell("/opt/megh/bin/megh", []string{"ssh", "dev"})
	if !strings.HasPrefix(cmd, "/bin/zsh -lic ") {
		t.Fatalf("expected login zsh wrapper, got %q", cmd)
	}
	if !strings.Contains(cmd, "MEGH_ITERM_REEXEC=1") {
		t.Fatal("missing reexec guard")
	}
	if !strings.Contains(cmd, "/opt/megh/bin/megh") {
		t.Fatal("missing megh path")
	}
}
