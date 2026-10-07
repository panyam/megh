package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/panyam/megh/internal/features"
)

// --print is the script exactly as embedded: no MEGH_* export preamble (a local
// bash inherits the environment anyway) and nothing run.
func TestEnablePrintWritesTheScriptAlone(t *testing.T) {
	t.Setenv("MEGH_REDIS_PORT", "7000")
	old := enablePrint
	t.Cleanup(func() { enablePrint = old })
	enablePrint = true

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	runErr := enableCmd.RunE(enableCmd, []string{"redis"})
	os.Stdout = stdout
	w.Close()
	got, _ := io.ReadAll(r)
	if runErr != nil {
		t.Fatal(runErr)
	}
	want, err := features.Script("redis")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("printed %d bytes, want the %d-byte embedded script", len(got), len(want))
	}
	if bytes.Contains(got, []byte("MEGH_REDIS_PORT")) && !bytes.Contains(want, []byte("MEGH_REDIS_PORT")) {
		t.Error("--print leaked the caller's MEGH_* values")
	}
}

func withBoxMarker(t *testing.T, present bool) {
	t.Helper()
	old := boxMarker
	t.Cleanup(func() { boxMarker = old })
	boxMarker = filepath.Join(t.TempDir(), "build-info")
	if present {
		if err := os.WriteFile(boxMarker, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// On a box, `megh enable postgres` runs right there, with no provider key and
// no control machine involved.
func TestEnableRunsHereOnABox(t *testing.T) {
	withBoxMarker(t, true)
	if !enableRunsHere(false, []string{"postgres"}) {
		t.Error("on a box, an unnamed target should run locally")
	}
	if !enableRunsHere(false, nil) {
		t.Error("on a box, the menu path should run locally too")
	}
	if enableRunsHere(false, []string{"postgres", "otherbox"}) {
		t.Error("naming a box must still target that box")
	}
}

func TestEnableTargetsABoxFromAControlMachine(t *testing.T) {
	withBoxMarker(t, false)
	if enableRunsHere(false, []string{"postgres"}) {
		t.Error("off a box, enable must pipe to a box, not run on this machine")
	}
	if !enableRunsHere(true, []string{"postgres"}) {
		t.Error("--local always runs here")
	}
}
