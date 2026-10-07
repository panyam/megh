package lifecycle

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/providers"
)

// fake is an in-memory backend that records what the service asked of it.
type fake struct {
	name    string
	mesh    providers.Mesh
	boxes   []providers.Box
	upOpts  *providers.Options
	calls   []string
	started string
}

type result string

func (r result) Summary() string { return string(r) }

func (f *fake) Name() string         { return f.name }
func (f *fake) Mesh() providers.Mesh { return f.mesh }
func (f *fake) List(context.Context) ([]providers.Box, error) {
	f.calls = append(f.calls, "list")
	return f.boxes, nil
}
func (f *fake) Up(_ context.Context, o providers.Options) (providers.Result, error) {
	f.calls = append(f.calls, "up")
	f.upOpts = &o
	return result("launched\n"), nil
}
func (f *fake) Terminate(_ context.Context, id string) error {
	f.calls = append(f.calls, "terminate:"+id)
	return nil
}
func (f *fake) Volumes(context.Context) ([]providers.Volume, error) { return nil, nil }
func (f *fake) CreateVolume(context.Context, string, int, string) (*providers.Volume, error) {
	return nil, nil
}
func (f *fake) DeleteVolume(context.Context, string) error { return nil }

// starter is a fake that can resume a stopped box, as the docker backend can.
type starter struct{ fake }

func (s *starter) StartStopped(_ context.Context, id string) (providers.Result, error) {
	s.started = id
	return result("started\n"), nil
}

var key = "ssh-ed25519 cGhvbmUta2V5 phone"

func svc(p providers.Provider, c config.Config) *Service {
	return &Service{Config: c, Providers: []providers.Provider{p}}
}

func TestUpFillsUnsetFieldsFromConfigThenDefaults(t *testing.T) {
	f := &fake{name: "runpod"}
	c := config.Default()
	c.Providers = map[string]config.Provider{
		"runpod": {DefaultDC: "US-IL-1", DefaultVolume: "vol1", VCPU: 4},
	}
	if _, err := svc(f, c).Up(context.Background(), UpRequest{Name: "a", RAMGiB: 32, PubKey: key}); err != nil {
		t.Fatal(err)
	}
	o := f.upOpts
	if o.Name != "megh-a" || o.DataCenter != "US-IL-1" || o.VolumeID != "vol1" {
		t.Errorf("name/dc/volume = %q/%q/%q", o.Name, o.DataCenter, o.VolumeID)
	}
	if o.VCPU != 4 || o.RAMGiB != 32 || o.DiskGiB != 20 {
		t.Errorf("config vcpu, request ram, builtin disk: got %d/%d/%d", o.VCPU, o.RAMGiB, o.DiskGiB)
	}
	if !o.ExposeSSH {
		t.Error("expose_ssh unset means public SSH on")
	}
	if !strings.HasSuffix(o.Image, "megh-slim:latest") {
		t.Errorf("default flavor image: %q", o.Image)
	}
}

func TestUpRefusesARunningDuplicate(t *testing.T) {
	f := &fake{name: "runpod", boxes: []providers.Box{{ID: "p1", Name: "megh-a", Status: "RUNNING"}}}
	_, err := svc(f, config.Config{}).Up(context.Background(), UpRequest{Name: "a", PubKey: key})
	if err == nil || !strings.Contains(err.Error(), `box "a" is already running`) {
		t.Fatalf("got %v", err)
	}
	if f.upOpts != nil {
		t.Error("must not launch a second box under the same tailnet name")
	}
}

func TestUpResumesAStoppedBoxWhenTheBackendCan(t *testing.T) {
	s := &starter{fake{name: "docker", boxes: []providers.Box{{ID: "c1", Name: "megh-a", Status: "EXITED"}}}}
	res, err := svc(s, config.Config{}).Up(context.Background(), UpRequest{Name: "a", Provider: "docker", PubKey: key})
	if err != nil || res.Summary() != "started\n" || s.started != "c1" || s.upOpts != nil {
		t.Errorf("want a resume of c1 and no new box: res=%v err=%v started=%q up=%v", res, err, s.started, s.upOpts)
	}
}

func TestUpRefusesAStoppedBoxTheBackendCannotResume(t *testing.T) {
	f := &fake{name: "runpod", boxes: []providers.Box{{ID: "p1", Name: "megh-a", Status: "EXITED"}}}
	_, err := svc(f, config.Config{}).Up(context.Background(), UpRequest{Name: "a", PubKey: key})
	if err == nil || !strings.Contains(err.Error(), "already exists") || f.upOpts != nil {
		t.Errorf("got %v, launched=%v", err, f.upOpts != nil)
	}
}

// The server has no key of its own: extra_pubkeys alone must be enough, and no
// key at all must be refused rather than launch an unreachable box.
func TestUpNeedsSomeKeyButNotTheLaunchers(t *testing.T) {
	f := &fake{name: "runpod"}
	if _, err := svc(f, config.Config{}).Up(context.Background(), UpRequest{Name: "a"}); err == nil || f.upOpts != nil {
		t.Fatalf("no key anywhere must refuse: err=%v", err)
	}
	c := config.Config{ExtraPubKeys: []string{key}}
	if _, err := svc(f, c).Up(context.Background(), UpRequest{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if f.upOpts.PubKey != key {
		t.Errorf("pubkey = %q", f.upOpts.PubKey)
	}
}

func TestUpBoxEnvAddsPersistAndSymlinksWithoutTouchingTheCallersMap(t *testing.T) {
	f := &fake{name: "runpod"}
	c := config.Config{Persist: []string{"~/.claude"}, Symlinks: map[string]string{"~/n": "repos/n"}}
	base := map[string]string{"JIRA_EMAIL": "x"}
	if _, err := svc(f, c).Up(context.Background(), UpRequest{Name: "a", PubKey: key, BoxEnv: base}); err != nil {
		t.Fatal(err)
	}
	env := f.upOpts.ExtraEnv
	if env["JIRA_EMAIL"] != "x" || env["MEGH_PERSIST"] != "~/.claude" || env["MEGH_SYMLINKS"] != "~/n:repos/n" {
		t.Errorf("env = %v", env)
	}
	if len(base) != 1 {
		t.Errorf("caller's map was mutated: %v", base)
	}
}

func TestDownLeavesTheMeshBeforeTerminating(t *testing.T) {
	f := &fake{name: "runpod", mesh: providers.Mesh{Vendor: providers.MeshTailscale, AtBoot: true}}
	var out bytes.Buffer
	s := &Service{Providers: []providers.Provider{f}, Out: &out}
	leave := func(context.Context, providers.Box) string {
		f.calls = append(f.calls, "leave")
		return "asked a to leave the tailnet"
	}
	if err := s.Down(context.Background(), f, providers.Box{ID: "p1", Name: "megh-a"}, DownOptions{Leave: leave}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.calls, ",") != "leave,terminate:p1" {
		t.Errorf("calls = %v", f.calls)
	}
	if !strings.Contains(out.String(), "asked a to leave") || !strings.Contains(out.String(), "terminated a (p1)") {
		t.Errorf("out = %q", out.String())
	}
}

// The server's path: no SSH to the box and no Tailscale client. Termination
// must still happen.
func TestDownWithNoLeaveAndNoTailscaleStillTerminates(t *testing.T) {
	f := &fake{name: "runpod", mesh: providers.Mesh{Vendor: providers.MeshTailscale, AtBoot: true}}
	if err := svc(f, config.Config{}).Down(context.Background(), f, providers.Box{ID: "p1", Name: "megh-a"}, DownOptions{}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.calls, ",") != "terminate:p1" {
		t.Errorf("calls = %v", f.calls)
	}
}

func TestDownSkipsLeaveOffMesh(t *testing.T) {
	f := &fake{name: "docker"}
	called := false
	leave := func(context.Context, providers.Box) string { called = true; return "" }
	if err := svc(f, config.Config{}).Down(context.Background(), f, providers.Box{ID: "c1"}, DownOptions{Leave: leave}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("a backend with no mesh has nothing to leave")
	}
}

func TestProviderFallsBackToConfigDefaultAndNamesWhatExists(t *testing.T) {
	a, b := &fake{name: "docker"}, &fake{name: "runpod"}
	s := &Service{Config: config.Config{DefaultProvider: "docker"}, Providers: []providers.Provider{b, a}}
	if p, err := s.Provider(""); err != nil || p.Name() != "docker" {
		t.Errorf("empty name should take default_provider: %v %v", p, err)
	}
	_, err := s.Provider("hetzner")
	if err == nil || !strings.Contains(err.Error(), "available: docker, runpod") {
		t.Errorf("got %v", err)
	}
}

func TestMintWithNoTailscaleClientWarnsAndFallsBack(t *testing.T) {
	var errOut bytes.Buffer
	s := &Service{Config: config.Config{Tailscale: config.Tailscale{MintKeys: true}}, Err: &errOut}
	if k := s.BootAuthKey(context.Background(), providers.Mesh{Vendor: "tailscale", AtBoot: true}, "a"); k != "" {
		t.Errorf("key = %q", k)
	}
	if !strings.Contains(errOut.String(), "no Tailscale control-plane credential") {
		t.Errorf("err = %q", errOut.String())
	}
	if s.BootAuthKey(context.Background(), providers.Mesh{Vendor: "tailscale"}, "a") != "" {
		t.Error("a backend that joins later never mints")
	}
}
