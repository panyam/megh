package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The scripts under env/base are baked into the image or run as PID 1 on a box.
// A syntax error in one of them is not caught by `go build`, and only surfaces
// partway through a ~15 minute CI image build (or, for the entrypoint, on a box
// that then fails to boot). Parse them in milliseconds instead.
func TestEnvScriptsParse(t *testing.T) {
	scripts, err := filepath.Glob("env/base/*.sh")
	if err != nil || len(scripts) == 0 {
		t.Fatalf("no scripts found under env/base: %v", err)
	}
	for _, path := range scripts {
		t.Run(filepath.Base(path), func(t *testing.T) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			cmd := exec.Command("bash", "-n")
			cmd.Stdin = strings.NewReader(string(src))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("%s is not valid bash: %v\n%s", path, err, out)
			}
		})
	}
}
