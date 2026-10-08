package local

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panyam/megh/internal/config"
)

// fakeBins puts a recording stub for each named engine on an otherwise empty
// PATH. Each call appends "<engine> <argv>" to the returned log.
func fakeBins(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "argv")
	for _, n := range names {
		script := "#!/bin/sh\necho \"" + n + " $*\" >> " + log + "\n"
		if err := os.WriteFile(filepath.Join(dir, n), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv("MEGH_ENGINE", "")
	return log
}

func calls(t *testing.T, log string) []string {
	b, _ := os.ReadFile(log)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// Like the Makefile's CONTAINER_CMD: podman when it is installed, else docker.
func TestEngineDetectionPrefersPodman(t *testing.T) {
	for _, tc := range []struct {
		bins []string
		want string
	}{
		{[]string{"podman", "docker"}, "podman"},
		{[]string{"docker"}, "docker"},
		{[]string{"podman"}, "podman"},
	} {
		fakeBins(t, tc.bins...)
		e, err := ResolveEngine(config.Provider{})
		if err != nil || e.Name != tc.want || e.Source != "auto-detected" {
			t.Errorf("%v: got %+v, %v", tc.bins, e, err)
		}
	}
	fakeBins(t)
	if _, err := ResolveEngine(config.Provider{}); err == nil || !strings.Contains(err.Error(), "install podman") {
		t.Errorf("neither installed: got %v", err)
	}
}

func TestEngineSettingBeatsDetectionAndEnvBeatsSetting(t *testing.T) {
	fakeBins(t, "podman", "docker")
	if e, _ := ResolveEngine(config.Provider{Engine: "docker"}); e.Name != "docker" || e.Source != "providers.local.engine" {
		t.Errorf("setting: got %+v", e)
	}
	t.Setenv("MEGH_ENGINE", "podman")
	if e, _ := ResolveEngine(config.Provider{Engine: "docker"}); e.Name != "podman" || e.Source != "MEGH_ENGINE" {
		t.Errorf("env: got %+v", e)
	}
	t.Setenv("MEGH_ENGINE", "nerdctl")
	if _, err := ResolveEngine(config.Provider{}); err == nil || !strings.Contains(err.Error(), "podman or docker") {
		t.Errorf("unknown engine in env: got %v", err)
	}
}

// A named engine that is not installed is an error naming it, never a quiet
// switch to the other one, which would put the box in a different store.
func TestAChosenEngineIsNeverSwappedForTheOther(t *testing.T) {
	fakeBins(t, "docker")
	if _, err := ResolveEngine(config.Provider{Engine: "podman"}); err == nil || !strings.Contains(err.Error(), "podman is not on PATH") {
		t.Errorf("got %v", err)
	}
}

// The setting can be a path, as CONTAINER_CMD can; the engine is its basename.
func TestEngineMayBeAPath(t *testing.T) {
	fakeBins(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "podman")
	os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755)
	e, err := ResolveEngine(config.Provider{Engine: bin})
	if err != nil || e.Name != "podman" || e.Bin != bin {
		t.Errorf("got %+v, %v", e, err)
	}
}

// providers.local.context picks the daemon or VM: docker calls it a context,
// podman a connection.
func TestContextMapsToEachEnginesFlag(t *testing.T) {
	for _, tc := range []struct{ engine, want string }{
		{"docker", "docker --context work ps -a"},
		{"podman", "podman --connection work ps -a"},
	} {
		log := fakeBins(t, tc.engine)
		p := New(func() config.Config {
			return config.Config{Providers: map[string]config.Provider{"local": {Engine: tc.engine, Context: "work"}}}
		})
		if _, err := p.List(context.Background()); err != nil {
			t.Fatal(err)
		}
		c := calls(t, log)
		if !strings.HasPrefix(c[len(c)-1], tc.want) {
			t.Errorf("%s: calls %q, want last to start %q", tc.engine, c, tc.want)
		}
		for _, l := range c {
			if !strings.HasPrefix(l, tc.engine+" "+strings.Fields(tc.want)[1]+" work ") {
				t.Errorf("%s: call without the context flag: %q", tc.engine, l)
			}
		}
	}
}

// podman has no {{.ServerVersion}}; the reachability check must not ask for it.
func TestReachabilityCheckWorksOnBothEngines(t *testing.T) {
	for _, engine := range []string{"podman", "docker"} {
		log := fakeBins(t, engine)
		New(func() config.Config { return config.Config{} }).List(context.Background())
		if c := calls(t, log); c[0] != engine+" info" {
			t.Errorf("%s: first call %q, want a plain info", engine, c[0])
		}
	}
}

func TestUnreachableEngineSaysHowToStartIt(t *testing.T) {
	for engine, want := range map[string]string{"podman": "podman machine start", "docker": "Docker Desktop"} {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, engine), []byte("#!/bin/sh\necho 'cannot connect' >&2\nexit 125\n"), 0o755)
		t.Setenv("PATH", dir)
		t.Setenv("MEGH_ENGINE", "")
		_, err := New(func() config.Config { return config.Config{} }).List(context.Background())
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "cannot connect") {
			t.Errorf("%s: got %v", engine, err)
		}
	}
}

// `podman inspect` of a box, recorded from podman 5 (rootless machine): the
// fields megh reads have docker's names and shapes, and Name has no slash.
func TestParseInspectReadsPodman(t *testing.T) {
	b, err := parseInspect(`[{
	  "Id": "5f0c3b1e9a7d2c4b8e6f1a0d3c5b7e9f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d",
	  "Name": "megh-work",
	  "Config": {"Image": "localhost/megh-local-slim:arm64", "Labels": {"megh.managed": "1"}},
	  "State": {"Status": "running", "Running": true},
	  "NetworkSettings": {"Ports": {"22/tcp": [{"HostIp": "127.0.0.1", "HostPort": "40123"}]}}
	}]`)
	if err != nil || b.Name != "megh-work" || b.Status != "RUNNING" || b.PublicIP != "127.0.0.1" || b.SSHPort != 40123 || b.Image != "localhost/megh-local-slim:arm64" {
		t.Fatalf("got %+v, %v", b, err)
	}
}

// When the engine was guessed and the other one is installed too, megh looks
// there for boxes, so a box left in docker does not silently vanish.
func TestElsewhereFindsBoxesInTheOtherEngine(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "podman"), []byte("#!/bin/sh\n"), 0o755)
	os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n[ \"$1\" = ps ] && printf 'abc\\ndef\\n'\n"), 0o755)
	t.Setenv("PATH", dir)
	t.Setenv("MEGH_ENGINE", "")
	p := New(func() config.Config { return config.Config{} })
	if other, n := p.Elsewhere(context.Background()); other != "docker" || n != 2 {
		t.Errorf("auto-detected: got %q %d", other, n)
	}
	chosen := New(func() config.Config {
		return config.Config{Providers: map[string]config.Provider{"local": {Engine: "podman"}}}
	})
	if other, n := chosen.Elsewhere(context.Background()); n != 0 {
		t.Errorf("an engine chosen on purpose should not look elsewhere: got %q %d", other, n)
	}
}

func TestBackendIsNamedLocal(t *testing.T) {
	if n := New(func() config.Config { return config.Config{} }).Name(); n != "local" {
		t.Errorf("got %q", n)
	}
}

// A context belongs to one engine, and before podman support every context in
// a megh.yaml was a docker one. Applied to a detected podman it becomes an
// unknown --connection, so a context with no engine named is refused rather
// than guessed at.
func TestContextWithoutAnEngineIsRefused(t *testing.T) {
	fakeBins(t, "podman", "docker")
	_, err := ResolveEngine(config.Provider{Context: "default"})
	if err == nil || !strings.Contains(err.Error(), "providers.local.engine") || !strings.Contains(err.Error(), "default") {
		t.Fatalf("got %v", err)
	}
	if e, err := ResolveEngine(config.Provider{Engine: "docker", Context: "default"}); err != nil || e.Name != "docker" {
		t.Errorf("engine named in config: got %+v, %v", e, err)
	}
	t.Setenv("MEGH_ENGINE", "docker")
	if e, err := ResolveEngine(config.Provider{Context: "default"}); err != nil || e.Name != "docker" {
		t.Errorf("engine named in env: got %+v, %v", e, err)
	}
	// With only docker installed there is nothing to guess: an existing docker
	// config keeps working through the upgrade.
	fakeBins(t, "docker")
	if e, err := ResolveEngine(config.Provider{Context: "colima"}); err != nil || e.Name != "docker" || e.Context != "colima" {
		t.Errorf("docker only: got %+v, %v", e, err)
	}
}
