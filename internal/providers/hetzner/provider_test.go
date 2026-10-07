package hetzner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/providers"
)

const serverTypes = `{"server_types":[
 {"name":"cpx11","cores":2,"memory":2,"disk":40,"architecture":"x86","deprecation":null,"prices":[{"location":"ash","price_hourly":{"gross":"0.0076"}}]},
 {"name":"cpx31","cores":4,"memory":8,"disk":160,"architecture":"x86","deprecation":null,"prices":[{"location":"ash","price_hourly":{"gross":"0.0236"}}]},
 {"name":"ccx13","cores":2,"memory":8,"disk":80,"architecture":"x86","deprecation":null,"prices":[{"location":"ash","price_hourly":{"gross":"0.0240"}}]},
 {"name":"cpx41","cores":8,"memory":16,"disk":240,"architecture":"x86","deprecation":null,"prices":[{"location":"ash","price_hourly":{"gross":"0.0438"}}]},
 {"name":"cax21","cores":4,"memory":8,"disk":80,"architecture":"arm","deprecation":null,"prices":[{"location":"ash","price_hourly":{"gross":"0.0010"}}]},
 {"name":"cx22","cores":2,"memory":8,"disk":80,"architecture":"x86","deprecation":{"announced":"2025-01-01"},"prices":[{"location":"ash","price_hourly":{"gross":"0.0010"}}]},
 {"name":"cpx21eu","cores":4,"memory":8,"disk":80,"architecture":"x86","deprecation":null,"prices":[{"location":"fsn1","price_hourly":{"gross":"0.0010"}}]}
]}`

// fakeAPI is a Hetzner API stand-in that records each request.
type fakeAPI struct {
	srv      *httptest.Server
	posted   map[string]any
	paths    []string
	gotToken string
}

func newFake(t *testing.T) *fakeAPI {
	f := &fakeAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.gotToken = r.Header.Get("Authorization")
		f.paths = append(f.paths, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.Method == "GET" && r.URL.Path == "/volumes/77":
			io.WriteString(w, `{"volume":{"id":77,"name":"megh-work","size":50,"location":{"name":"ash"}}}`)
		case r.Method == "GET" && r.URL.Path == "/server_types":
			io.WriteString(w, serverTypes)
		case r.Method == "POST" && (r.URL.Path == "/servers" || r.URL.Path == "/volumes"):
			json.NewDecoder(r.Body).Decode(&f.posted)
			if r.URL.Path == "/servers" {
				io.WriteString(w, `{"server":{"id":5,"name":"megh-dev","public_net":{"ipv4":{"ip":"203.0.113.9"}}}}`)
			} else {
				io.WriteString(w, `{"volume":{"id":88,"name":"megh-new","size":50,"location":{"name":"ash"}}}`)
			}
		case r.Method == "GET" && r.URL.Path == "/servers":
			io.WriteString(w, `{"servers":[{"id":5,"name":"megh-dev","status":"running",
			  "server_type":{"name":"cpx31","prices":[{"location":"ash","price_hourly":{"gross":"0.0236"}}]},
			  "datacenter":{"location":{"name":"ash"}},"public_net":{"ipv4":{"ip":"203.0.113.9"}}}],
			  "meta":{"pagination":{"next_page":null}}}`)
		default:
			http.Error(w, `{"error":{"code":"not_found","message":"nope"}}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func testProvider(t *testing.T, f *fakeAPI, c config.Config) *Provider {
	t.Setenv("HCLOUD_TOKEN", "tok-123")
	t.Setenv("GH_MEGH_TOKEN", "ghp_pull")
	return &Provider{cfg: func() config.Config { return c }, base: f.srv.URL}
}

func registryConfig() config.Config {
	c := config.Default()
	c.Registries[0].Namespace, c.Registries[0].Username = "acme", "acme"
	return c
}

func opts(vcpu, ram, disk int) providers.Options {
	return providers.Options{Name: "dev", Image: "ghcr.io/acme/megh-slim:latest", VolumeID: "77",
		VCPU: vcpu, RAMGiB: ram, DiskGiB: disk, PubKey: "ssh-ed25519 AAAA a\nssh-ed25519 BBBB b", ExposeSSH: true,
		TSAuthKey: "tskey-1", ExtraEnv: map[string]string{"MEGH_PERSIST": "~/.claude"}}
}

// Sizes flex per launch: the cheapest current x86 type sold in the volume's
// location that fits, never an arm, deprecated or elsewhere-only type.
func TestUpPicksTheCheapestFittingTypeInTheVolumesLocation(t *testing.T) {
	for _, c := range []struct {
		vcpu, ram, disk int
		want            string
	}{
		{2, 2, 20, "cpx11"},
		{2, 8, 20, "cpx31"}, // cheaper than ccx13 with the same RAM
		{4, 8, 100, "cpx31"},
		{8, 16, 50, "cpx41"},
	} {
		f := newFake(t)
		p := testProvider(t, f, registryConfig())
		if _, err := p.Up(context.Background(), opts(c.vcpu, c.ram, c.disk)); err != nil {
			t.Fatalf("%d/%d/%d: %v", c.vcpu, c.ram, c.disk, err)
		}
		if got := f.posted["server_type"]; got != c.want {
			t.Errorf("%d vCPU/%d GB/%d GB: got %v, want %s", c.vcpu, c.ram, c.disk, got, c.want)
		}
	}
}

func TestUpRefusesAShapeNothingSells(t *testing.T) {
	f := newFake(t)
	p := testProvider(t, f, registryConfig())
	if _, err := p.Up(context.Background(), opts(32, 128, 50)); err == nil || !strings.Contains(err.Error(), "no x86 server type in ash") {
		t.Errorf("got %v", err)
	}
}

func TestUpCreatesTheServerInTheVolumesLocationWithTheVolume(t *testing.T) {
	f := newFake(t)
	p := testProvider(t, f, registryConfig())
	res, err := p.Up(context.Background(), opts(2, 8, 20))
	if err != nil {
		t.Fatal(err)
	}
	g := f.posted
	if g["location"] != "ash" || g["name"] != "megh-dev" || g["image"] != vmImage || g["automount"] != true {
		t.Errorf("request %v", g)
	}
	if v, _ := g["volumes"].([]any); len(v) != 1 || v[0] != float64(77) {
		t.Errorf("volumes %v", g["volumes"])
	}
	if l, _ := g["labels"].(map[string]any); l[managedLabel] != "1" {
		t.Errorf("labels %v", g["labels"])
	}
	if f.gotToken != "Bearer tok-123" {
		t.Errorf("auth %q", f.gotToken)
	}
	if !strings.Contains(res.Summary(), "ssh -p 2222 root@203.0.113.9") {
		t.Errorf("summary %q", res.Summary())
	}
}

func TestUpNeedsAVolumeInTheRequestedLocation(t *testing.T) {
	f := newFake(t)
	p := testProvider(t, f, registryConfig())
	o := opts(2, 8, 20)
	o.VolumeID = ""
	if _, err := p.Up(context.Background(), o); err == nil || !strings.Contains(err.Error(), "needs a volume") {
		t.Errorf("no volume: %v", err)
	}
	o = opts(2, 8, 20)
	o.DataCenter = "fsn1"
	if _, err := p.Up(context.Background(), o); err == nil || !strings.Contains(err.Error(), "is in ash, not fsn1") {
		t.Errorf("wrong location: %v", err)
	}
}

func TestNoTokenSaysWhichVariable(t *testing.T) {
	f := newFake(t)
	p := testProvider(t, f, registryConfig())
	t.Setenv("HCLOUD_TOKEN", "")
	if _, err := p.List(context.Background()); err == nil || !strings.Contains(err.Error(), "HCLOUD_TOKEN") {
		t.Errorf("got %v", err)
	}
}

func TestListMapsManagedServersToBoxes(t *testing.T) {
	f := newFake(t)
	p := testProvider(t, f, registryConfig())
	boxes, err := p.List(context.Background())
	if err != nil || len(boxes) != 1 {
		t.Fatalf("%v %v", boxes, err)
	}
	b := boxes[0]
	if b.Status != "RUNNING" || b.PublicIP != "203.0.113.9" || b.SSHPort != boxSSHPort || b.DataCenter != "ash" || b.CostPerHr != 0.0236 || b.DisplayName() != "dev" {
		t.Errorf("box %+v", b)
	}
	if !strings.Contains(strings.Join(f.paths, " "), "label_selector="+managedLabel) {
		t.Errorf("listing must filter on the label: %v", f.paths)
	}
}

// Hetzner formats at creation, which is why the boot script never formats.
func TestCreateVolumeIsFormattedAndLabelled(t *testing.T) {
	f := newFake(t)
	p := testProvider(t, f, registryConfig())
	v, err := p.CreateVolume(context.Background(), "megh-new", 50, "ash")
	if err != nil || v.ID != "88" || v.Provider != "hetzner" {
		t.Fatalf("%+v %v", v, err)
	}
	if f.posted["format"] != "ext4" || f.posted["location"] != "ash" {
		t.Errorf("request %v", f.posted)
	}
}

func TestCloudInitRunsTheImageSafely(t *testing.T) {
	s := cloudInit(bootSpec{Image: "ghcr.io/acme/megh-slim:latest", VolumeID: 77, ExposeSSH: true,
		PullUser: "acme", PullToken: "ghp_pull",
		Env: map[string]string{"PUBLIC_KEY": "ssh-ed25519 AAAA a\nssh-ed25519 BBBB b", "TS_AUTHKEY": "tskey-1"}})
	for _, want := range []string{
		"systemctl disable --now ssh.socket ssh.service",
		"iptables -I DOCKER-USER -d 169.254.169.254 -j DROP",
		"/mnt/HC_Volume_77",
		"docker login 'ghcr.io' -u 'acme' --password-stdin",
		"-p 2222:22",
		`-v "$vol":/workspace`,
		" -e PUBLIC_KEY -e TS_AUTHKEY ",
		"rm -f /root/megh.env",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	runLine := s[strings.Index(s, "docker run"):]
	runLine = runLine[:strings.Index(runLine, "\n")]
	for _, secret := range []string{"ghp_pull", "tskey-1", "AAAA"} {
		if strings.Contains(runLine, secret) {
			t.Errorf("value %q on the docker run command line (visible in ps): %s", secret, runLine)
		}
	}
	if strings.Contains(runLine, "MEGH_PULL_TOKEN") {
		t.Error("the pull token must not reach the box")
	}
	if strings.Contains(s, "mkfs") {
		t.Error("the boot script must never format a volume")
	}
	if noSSH := cloudInit(bootSpec{Image: "x/y", VolumeID: 1}); strings.Contains(noSSH, "-p 2222") || strings.Contains(noSSH, "docker login") {
		t.Errorf("tailnet-only, public image: %s", noSSH)
	}
}

// shq's quoting must survive a real shell, including newlines and quotes.
func TestShqRoundTripsThroughBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	for _, v := range []string{"plain", "it's", "a\nb", `$HOME "x" \n`, "ssh-ed25519 AAAA a\nssh-ed25519 BBBB b"} {
		out, err := exec.Command(bash, "-c", "x="+shq(v)+"; printf '%s' \"$x\"").Output()
		if err != nil || string(out) != v {
			t.Errorf("%q came back %q (%v)", v, out, err)
		}
	}
}

// Offers is the region search: every location with a fitting x86 type, at the
// type Up would pick there, cheapest first.
func TestOffersListsEachLocationsCheapestFittingType(t *testing.T) {
	p := testProvider(t, newFake(t), registryConfig())
	got, err := p.Offers(context.Background(), 4, 8, 50)
	if err != nil {
		t.Fatal(err)
	}
	want := []providers.Offer{{DC: "fsn1", Type: "cpx21eu", PerHr: 0.0010}, {DC: "ash", Type: "cpx31", PerHr: 0.0236}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got, _ := p.Offers(context.Background(), 64, 512, 50); len(got) != 0 {
		t.Errorf("an unsellable shape has no offers: %+v", got)
	}
}

// A token handed in per request wins over the environment, and an empty one
// falls back to it, as runpod.NewWithKey does.
func TestNewWithTokenPrefersItsOwnToken(t *testing.T) {
	f := newFake(t)
	t.Setenv("HCLOUD_TOKEN", "from-env")
	for tok, want := range map[string]string{"from-request": "from-request", "": "from-env"} {
		p := NewWithToken(registryConfig, tok)
		p.base = f.srv.URL
		p.List(context.Background())
		if f.gotToken != "Bearer "+want {
			t.Errorf("token %q: sent %q", tok, f.gotToken)
		}
	}
}
