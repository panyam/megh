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

// A dev box has to have a working `go` in every shell, not just a login one.
// Three separate routes put /usr/local/go/bin on PATH and all three leak: the
// Dockerfile's ENV does not reach an sshd session, /etc/profile.d is login-only,
// and a dotfiles ~/.zshrc arriving over mounts:/symlinks: SETS PATH rather than
// appending. The symlinks into /usr/local/bin are the route that survives, and
// without them the box looks Go-less and the next move is an apt install of an
// older toolchain beside the real one.
func TestProvisionPutsGoOnEveryPath(t *testing.T) {
	src, err := os.ReadFile("env/base/provision.sh")
	if err != nil {
		t.Fatalf("read provision.sh: %v", err)
	}
	text := string(src)
	for _, want := range []string{
		"ln -sf /usr/local/go/bin/go /usr/local/bin/go",
		"ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("provision.sh no longer runs %q, so go is only reachable from a login shell", want)
		}
	}
	// The editor tooling has to land somewhere on that same PATH, and it is
	// deliberately NOT gated on MEGH_SLIM: slim drops the frontend stack, not
	// the language this repo is written in.
	for _, tool := range []string{"gopls", "goimports", "delve", "staticcheck"} {
		if !strings.Contains(text, tool) {
			t.Errorf("provision.sh no longer installs %s", tool)
		}
	}
	if !strings.Contains(text, "GOBIN=/usr/local/bin") {
		t.Error("go tools must install to /usr/local/bin, which survives a PATH-replacing rc file")
	}
	// The module and build caches are the bulk of what installing them costs,
	// and no box ever reads them again.
	if !strings.Contains(text, "go clean -cache -modcache") {
		t.Error("provision.sh should drop the go caches, which are hundreds of MB of dead layer")
	}
}
