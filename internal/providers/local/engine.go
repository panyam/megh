package local

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/providers"
)

// Engine is the container engine local boxes and the gateway run under. megh
// shells out to its CLI, and podman's CLI takes docker's verbs and flags for
// everything megh does, with two exceptions this type absorbs: picking a
// daemon is --context on docker and --connection on podman, and podman's info
// has no ServerVersion.
type Engine struct {
	// Name is "podman" or "docker", whatever Bin is called.
	Name string
	// Bin is what to execute: the name on PATH, or the configured path.
	Bin string
	// Source says how it was chosen ("MEGH_ENGINE", "providers.local.engine"
	// or "auto-detected"), for `megh config` and the other-engine notice.
	Source string
	// Context is providers.local.context: a docker context or a podman
	// connection. Empty uses the engine's own default.
	Context string
}

// Detection is podman whenever podman is installed, and docker only on a
// machine with no podman at all; the Makefile's CONTAINER_CMD uses the same
// order. Docker on purpose is MEGH_ENGINE or providers.local.engine.
//
// "Installed" is not "on PATH". podman's macOS installer (and Podman Desktop)
// puts it in /opt/podman/bin and adds that via /etc/paths.d, which only a login
// shell that does not reset PATH ever reads, while Docker Desktop links docker
// into /usr/local/bin, which every PATH has. A dotfiles ~/.zshrc that SETS PATH
// therefore hid podman and detection quietly chose docker, so megh also looks
// where podman's installers put it.
var podmanInstallPaths = []string{
	"/opt/podman/bin/podman",   // podman.io installer, Podman Desktop
	"/opt/homebrew/bin/podman", // Homebrew, Apple silicon
	"/usr/local/bin/podman",    // Homebrew, Intel; Linux from source
}

// findEngine is what to execute for an engine name and whether it is
// installed: the name when it is on PATH, else (podman only) the first
// install location that holds it.
func findEngine(name string) (string, bool) {
	if _, err := exec.LookPath(name); err == nil {
		return name, true
	}
	if name == "podman" {
		for _, p := range podmanInstallPaths {
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
				return p, true
			}
		}
	}
	return "", false
}

// autoDetected prefixes every detected Source; Elsewhere keys on it.
const autoDetected = "auto-detected"

// ResolveEngine picks the engine: MEGH_ENGINE, else providers.local.engine,
// else podman if it is installed anywhere (findEngine), else docker. A named
// engine that is not installed is an error rather than a switch to the other
// one, because a box lives in one engine's store and the other would not show
// it. Neither installed is providers.ErrNotConfigured, so a machine without
// containers (a cloud box, a phone) simply has no local backend.
func ResolveEngine(c config.Provider) (Engine, error) {
	e := Engine{Context: c.Context}
	want, src := os.Getenv("MEGH_ENGINE"), "MEGH_ENGINE"
	if want == "" {
		want, src = c.Engine, "providers.local.engine"
	}
	podman, hasPodman := findEngine("podman")
	if want == "" && c.Context != "" && hasPodman {
		// A context belongs to one engine, and until podman support every
		// context megh saw was docker's; on a detected podman it would become
		// an unknown --connection. With only docker installed there is nothing
		// to guess, so existing docker configs keep working; with podman there
		// is, so ask.
		return e, fmt.Errorf("providers.local.context is %q but no engine is named, so megh can't tell whose it is: set providers.local.engine to docker (a docker context) or podman (a podman connection)", c.Context)
	}
	if want == "" {
		if hasPodman {
			e.Name, e.Bin, e.Source = "podman", podman, autoDetected
			if podman != "podman" {
				e.Source = autoDetected + " at " + podman + ", which is not on PATH"
			}
			return e, nil
		}
		if _, err := exec.LookPath("docker"); err == nil {
			e.Name, e.Bin, e.Source = "docker", "docker", autoDetected+"; no podman installed (set providers.local.engine: docker to make it deliberate)"
			return e, nil
		}
		return e, fmt.Errorf("%w: no container engine on PATH (install podman, or docker; the local backend and megh gw need one)", providers.ErrNotConfigured)
	}
	e.Name, e.Bin, e.Source = filepath.Base(want), want, src
	if e.Name != "podman" && e.Name != "docker" {
		return e, fmt.Errorf("%s=%q: the engine is podman or docker, or a path to either", src, want)
	}
	if want == "podman" && hasPodman {
		e.Bin = podman
		return e, nil
	}
	if _, err := exec.LookPath(want); err != nil {
		return e, fmt.Errorf("%w: %s is not on PATH (%s names it; install it, or change that)", providers.ErrNotConfigured, e.Name, src)
	}
	return e, nil
}

// OtherInstalled is the engine megh did not pick, when it is installed too:
// what a leftover container in the other store is looked up with.
func (e Engine) OtherInstalled() (Engine, bool) {
	bin, ok := findEngine(e.Other())
	return Engine{Name: e.Other(), Bin: bin, Source: "the other engine"}, ok
}

// Detected reports whether the engine was guessed rather than named.
func (e Engine) Detected() bool { return strings.HasPrefix(e.Source, autoDetected) }

// Args prefixes the context flag, so every call reaches the same daemon or
// VM whatever the shell's own default is.
func (e Engine) Args(args ...string) []string {
	if e.Context == "" {
		return args
	}
	flag := "--context"
	if e.Name == "podman" {
		flag = "--connection"
	}
	return append([]string{flag, e.Context}, args...)
}

// Command is the engine CLI with args, context included.
func (e Engine) Command(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, e.Bin, e.Args(args...)...)
}

// CommandLine is Command as one shell string, for ssh's ProxyCommand.
func (e Engine) CommandLine(args ...string) string {
	return strings.Join(append([]string{e.Bin}, e.Args(args...)...), " ")
}

// Other is the engine megh did not pick, for the other-engine notice.
func (e Engine) Other() string {
	if e.Name == "podman" {
		return "docker"
	}
	return "podman"
}

// Unreachable wraps a failed `info` with how to start this engine: the CLI is
// there, the thing behind it isn't.
func (e Engine) Unreachable(msg string) error {
	hint := "start Docker Desktop or colima"
	if e.Name == "podman" {
		hint = "on macOS or Windows run `podman machine start` (`podman machine init` the first time); on Linux check `podman info` works for this user"
	}
	if e.Context != "" {
		hint += fmt.Sprintf(", and that %s exists (providers.local.context)", e.Context)
	}
	return fmt.Errorf("%s is installed but not reachable (%s): %s", e.Name, hint, msg)
}
