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

// A box joined by `megh mesh join` holds no auth key: the key travelled on the
// bring-up script's stdin and was single-use. Its tailscaled state survives a
// restart, so the entrypoint has to treat that state as a second reason to bring
// tailscale up, or a restarted box drops off the mesh with no way back except a
// re-join from the control machine.
func TestEntrypointBringsTailscaleUpFromStoredAuth(t *testing.T) {
	src, err := os.ReadFile("env/base/entrypoint.sh")
	if err != nil {
		t.Fatalf("read entrypoint: %v", err)
	}
	guard := `if [ -n "${TS_AUTHKEY:-}" ] || [ -s /var/lib/tailscale/tailscaled.state ]; then`
	if !strings.Contains(string(src), guard) {
		t.Errorf("entrypoint does not bring tailscale up from stored auth; expected:\n%s", guard)
	}
}

// CONSTRAINTS C2: the entrypoint delegates bring-up to the embedded helper
// rather than reimplementing it. The command it delegates through is now
// `megh mesh join --local`; `megh doctor ts start --local` is the retired
// spelling, kept working for images built before the move.
func TestEntrypointDelegatesBringUpToTheMeshCommand(t *testing.T) {
	src, err := os.ReadFile("env/base/entrypoint.sh")
	if err != nil {
		t.Fatalf("read entrypoint: %v", err)
	}
	if !strings.Contains(string(src), "megh mesh join --local") {
		t.Error("entrypoint must bring tailscale up through the embedded helper (C2)")
	}
}

// A script's own output is a user interface. `megh doctor ts` still works, but
// it is hidden and retired, so a box telling you to run it hands you a spelling
// that is absent from every help page — the worst kind of hint, since the
// command works and the docs deny it exists.
func TestShippedScriptsAdvertiseCurrentCommands(t *testing.T) {
	for _, glob := range []string{"env/base/*.sh", "internal/tsops/*.sh", "internal/features/*.sh"} {
		paths, _ := filepath.Glob(glob)
		for _, p := range paths {
			src, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("read %s: %v", p, err)
			}
			if strings.Contains(string(src), "doctor ts") {
				t.Errorf("%s names the retired `megh doctor ts`; say `megh mesh ...` instead", p)
			}
		}
	}
}

// The image bakes the Playwright viewer launcher the same way it bakes the
// webterm page: by running the feature's own script from the baked binary, so
// internal/features/playwright.sh stays the single source of truth. Without this
// RUN a full box has playwright and chromium from provision.sh but no `pw-ui`,
// so :9323 never comes up and the surface looks broken rather than absent.
func TestImageBakesTheViewerLauncher(t *testing.T) {
	src, err := os.ReadFile("env/base/Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	if !strings.Contains(string(src), "MEGH_PLAYWRIGHT_EMIT_ONLY=1 megh enable playwright --local") {
		t.Error("the Dockerfile does not bake pw-ui; the :9323 surface then needs `megh enable playwright` on every box")
	}
}
