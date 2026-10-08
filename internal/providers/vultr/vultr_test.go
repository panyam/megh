package vultr

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/providers"
)

const plansJSON = `{"plans":[
 {"id":"vc2-1c-0.5gb-v6","vcpu_count":1,"ram":512,"disk":10,"monthly_cost":2.5,"type":"vc2","locations":["ewr"]},
 {"id":"vc2-2c-4gb","vcpu_count":2,"ram":4096,"disk":80,"monthly_cost":20,"type":"vc2","locations":["ewr","ord"]},
 {"id":"vhp-2c-4gb-amd","vcpu_count":2,"ram":4096,"disk":100,"monthly_cost":24,"type":"vhp","locations":["ewr"]},
 {"id":"vc2-4c-8gb","vcpu_count":4,"ram":8192,"disk":160,"monthly_cost":40,"type":"vc2","locations":["ewr"]},
 {"id":"vcg-a16-2c-8g","vcpu_count":2,"ram":8192,"disk":50,"monthly_cost":1,"type":"vcg","locations":["ewr"]},
 {"id":"vc2-8c-32gb","vcpu_count":8,"ram":32768,"disk":640,"monthly_cost":160,"type":"vc2","locations":["ewr"]},
 {"id":"vc2-2c-4gb-ord-only","vcpu_count":2,"ram":4096,"disk":80,"monthly_cost":1,"type":"vc2","locations":["ord"]}
]}`

type fakeAPI struct {
	srv         *httptest.Server
	block       string // GET /blocks/b1 body
	attachFails int    // fail this many attaches before succeeding (-1: always)
	attaches    int
	instance    map[string]any
	created     map[string]any
	deleted     []string
}

func newFake(t *testing.T) *fakeAPI {
	f := &fakeAPI{block: `{"block":{"id":"b1","label":"megh-work","region":"ewr","size_gb":50,"attached_to_instance":""}}`}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := r.URL.Path; {
		case r.Method == "GET" && p == "/blocks/b1":
			io.WriteString(w, f.block)
		case r.Method == "GET" && p == "/regions":
			io.WriteString(w, `{"regions":[{"id":"ord","city":"Chicago","country":"US","continent":"North America"},{"id":"ams","city":"Amsterdam","country":"NL","continent":"Europe"}]}`)
		case r.Method == "GET" && p == "/plans":
			io.WriteString(w, plansJSON)
		case r.Method == "GET" && p == "/os":
			io.WriteString(w, `{"os":[{"id":1743,"name":"Ubuntu 22.04 LTS x64"},{"id":2284,"name":"Ubuntu 24.04 LTS x64"}]}`)
		case r.Method == "POST" && p == "/instances":
			json.NewDecoder(r.Body).Decode(&f.instance)
			io.WriteString(w, `{"instance":{"id":"i-1","label":"megh-dev","main_ip":"0.0.0.0","status":"pending"}}`)
		case r.Method == "POST" && p == "/blocks/b1/attach":
			f.attaches++
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["instance_id"] != "i-1" || body["live"] != true {
				http.Error(w, `{"error":"bad attach body"}`, 400)
				return
			}
			if f.attachFails < 0 || f.attaches <= f.attachFails {
				http.Error(w, `{"error":"Server is currently locked"}`, 400)
			}
		case r.Method == "DELETE" && strings.HasPrefix(p, "/instances/"):
			f.deleted = append(f.deleted, strings.TrimPrefix(p, "/instances/"))
		case r.Method == "GET" && p == "/instances":
			if r.URL.Query().Get("tag") != managedTag {
				http.Error(w, `{"error":"untagged list"}`, 400)
				return
			}
			io.WriteString(w, `{"instances":[{"id":"i-1","label":"megh-dev","region":"ewr","plan":"vc2-4c-8gb","main_ip":"203.0.113.5","status":"active","power_status":"running"},
			 {"id":"i-2","label":"megh-new","region":"ewr","plan":"vc2-2c-4gb","main_ip":"0.0.0.0","status":"pending","power_status":"stopped"}]}`)
		case r.Method == "POST" && p == "/blocks":
			json.NewDecoder(r.Body).Decode(&f.created)
			io.WriteString(w, `{"block":{"id":"b9","label":"megh-new","region":"ewr","size_gb":50}}`)
		default:
			http.Error(w, `{"error":"not found"}`, 404)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func testProvider(t *testing.T, f *fakeAPI, c config.Config) *Provider {
	t.Setenv("VULTR_API_KEY", "vk")
	t.Setenv("GH_MEGH_TOKEN", "ghp_pull")
	return &Provider{cfg: func() config.Config { return c }, base: f.srv.URL, attachTries: 5}
}

func cfg() config.Config {
	c := config.Default()
	c.Registries[0].Namespace, c.Registries[0].Username = "acme", "acme"
	return c
}

func opts(vcpu, ram, disk int) providers.Options {
	return providers.Options{Name: "dev", Image: "ghcr.io/acme/megh-slim:latest", VolumeID: "b1",
		VCPU: vcpu, RAMGiB: ram, DiskGiB: disk, PubKey: "ssh-ed25519 AAAA a", ExposeSSH: true, PullToken: "ghp_pull"}
}

func TestPickPlanIsTheCheapestIPv4CPUPlanInTheRegionThatFits(t *testing.T) {
	var plans []plan
	json.Unmarshal([]byte(plansJSON[len(`{"plans":`):len(plansJSON)-1]), &plans)
	for _, c := range []struct {
		vcpu, ram, disk int
		want            string
	}{
		{1, 0, 0, "vc2-2c-4gb"}, // not the IPv6-only $2.50 plan, not the GPU plan
		{2, 4, 50, "vc2-2c-4gb"},
		{2, 4, 90, "vhp-2c-4gb-amd"},
		{4, 8, 50, "vc2-4c-8gb"},
		{8, 16, 50, "vc2-8c-32gb"},
	} {
		got, err := pickPlan(plans, "ewr", c.vcpu, c.ram, c.disk)
		if err != nil || got.ID != c.want {
			t.Errorf("%d/%d/%d: got %s %v, want %s", c.vcpu, c.ram, c.disk, got.ID, err, c.want)
		}
	}
	if _, err := pickPlan(plans, "ewr", 64, 512, 50); err == nil {
		t.Error("an unsellable shape must be an error")
	}
}

// The instance is created in the volume's region, then the volume attaches,
// retried while Vultr is still building the instance.
func TestUpCreatesThenAttachesWithRetries(t *testing.T) {
	f := newFake(t)
	f.attachFails = 2
	p := testProvider(t, f, cfg())
	res, err := p.Up(context.Background(), opts(4, 8, 50))
	if err != nil {
		t.Fatal(err)
	}
	if f.attaches != 3 || len(f.deleted) != 0 {
		t.Errorf("attaches=%d deleted=%v", f.attaches, f.deleted)
	}
	in := f.instance
	if in["region"] != "ewr" || in["plan"] != "vc2-4c-8gb" || in["os_id"] != float64(2284) || in["label"] != "megh-dev" {
		t.Errorf("instance request %v", in)
	}
	if tags, _ := in["tags"].([]any); len(tags) != 1 || tags[0] != managedTag {
		t.Errorf("tags %v", in["tags"])
	}
	ud, _ := base64.StdEncoding.DecodeString(in["user_data"].(string))
	for _, want := range []string{"megh_prepare_volume 50 \"$vol\"", "docker login 'ghcr.io' -u 'acme'", "-p 2222:22"} {
		if !strings.Contains(string(ud), want) {
			t.Errorf("user_data missing %q", want)
		}
	}
	if !strings.Contains(res.Summary(), "vc2-4c-8gb") {
		t.Errorf("summary %q", res.Summary())
	}
}

// A volume that never attaches leaves no instance billing.
func TestUpTerminatesTheInstanceWhenTheVolumeNeverAttaches(t *testing.T) {
	f := newFake(t)
	f.attachFails = -1
	p := testProvider(t, f, cfg())
	if _, err := p.Up(context.Background(), opts(2, 4, 50)); err == nil || !strings.Contains(err.Error(), "terminated it") {
		t.Fatalf("got %v", err)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "i-1" {
		t.Errorf("deleted %v", f.deleted)
	}
}

func TestUpChecksTheVolumeBeforeCreatingAnything(t *testing.T) {
	f := newFake(t)
	p := testProvider(t, f, cfg())
	o := opts(2, 4, 50)
	o.VolumeID = ""
	if _, err := p.Up(context.Background(), o); err == nil || !strings.Contains(err.Error(), "needs a volume") {
		t.Errorf("no volume: %v", err)
	}
	o = opts(2, 4, 50)
	o.DataCenter = "lax"
	if _, err := p.Up(context.Background(), o); err == nil || !strings.Contains(err.Error(), "is in ewr, not lax") {
		t.Errorf("wrong region: %v", err)
	}
	f.block = `{"block":{"id":"b1","region":"ewr","size_gb":50,"attached_to_instance":"i-other"}}`
	if _, err := p.Up(context.Background(), opts(2, 4, 50)); err == nil || !strings.Contains(err.Error(), "attached to instance i-other") {
		t.Errorf("attached: %v", err)
	}
	if f.instance != nil {
		t.Error("an instance was created despite a bad volume")
	}
}

func TestListMapsTaggedInstances(t *testing.T) {
	f := newFake(t)
	p := testProvider(t, f, cfg())
	boxes, err := p.List(context.Background())
	if err != nil || len(boxes) != 2 {
		t.Fatalf("%v %v", boxes, err)
	}
	if b := boxes[0]; b.Status != "RUNNING" || b.PublicIP != "203.0.113.5" || b.SSHPort != 2222 || b.DisplayName() != "dev" || b.CostPerHr < 0.054 || b.CostPerHr > 0.056 {
		t.Errorf("running box %+v", b)
	}
	if b := boxes[1]; b.Status == "RUNNING" || b.PublicIP != "" || b.SSHPort != 0 {
		t.Errorf("pending box should have no endpoint yet: %+v", b)
	}
}

func TestCreateVolumeDefaultsToNVMe(t *testing.T) {
	f := newFake(t)
	p := testProvider(t, f, cfg())
	if _, err := p.CreateVolume(context.Background(), "megh-new", 50, "ewr"); err != nil {
		t.Fatal(err)
	}
	if f.created["block_type"] != "high_perf" || f.created["region"] != "ewr" {
		t.Errorf("request %v", f.created)
	}
	c := cfg()
	c.Providers["vultr"] = config.Provider{APIKeyEnv: "VULTR_API_KEY", BlockType: "storage_opt"}
	p = testProvider(t, f, c)
	p.CreateVolume(context.Background(), "megh-hdd", 50, "ewr")
	if f.created["block_type"] != "storage_opt" {
		t.Errorf("block_type from config: %v", f.created["block_type"])
	}
}

func TestNoKeyIsNotConfigured(t *testing.T) {
	f := newFake(t)
	p := testProvider(t, f, cfg())
	t.Setenv("VULTR_API_KEY", "")
	if _, err := p.List(context.Background()); !errors.Is(err, providers.ErrNotConfigured) || !strings.Contains(err.Error(), "VULTR_API_KEY") {
		t.Errorf("got %v", err)
	}
}

// Offers is the region search: every region with a fitting plan, at the plan
// Up would pick there, cheapest first.
func TestOffersListsEachRegionsCheapestFittingPlan(t *testing.T) {
	p := testProvider(t, newFake(t), cfg())
	all, err := p.Offers(context.Background(), providers.Want{VCPU: 2, RAMGiB: 4, DiskGiB: 50})
	if err != nil {
		t.Fatal(err)
	}
	got := providers.CheapestPerDC(all)
	if len(got) != 2 || got[0].DC != "ord" || got[0].Type != "vc2-2c-4gb-ord-only" || got[1].DC != "ewr" || got[1].Type != "vc2-2c-4gb" {
		t.Fatalf("got %+v", got)
	}
	if got[1].PerHr != 20.0/730 {
		t.Errorf("ewr per hour = %v", got[1].PerHr)
	}
}

func TestNewWithKeyPrefersItsOwnKey(t *testing.T) {
	var sent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = r.Header.Get("Authorization")
		io.WriteString(w, `{"blocks":[]}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("VULTR_API_KEY", "from-env")
	for key, want := range map[string]string{"from-request": "from-request", "": "from-env"} {
		p := NewWithKey(cfg, key)
		p.base = srv.URL
		p.Volumes(context.Background())
		if sent != "Bearer "+want {
			t.Errorf("key %q: sent %q", key, sent)
		}
	}
}

// The pull token comes from the caller, never this process's environment, so
// a server with no GH_MEGH_TOKEN of its own can still launch a private image.
func TestUpLogsInWithTheCallersPullTokenOnly(t *testing.T) {
	userData := func(token string) string {
		f := newFake(t)
		p := testProvider(t, f, cfg())
		o := opts(2, 4, 50)
		o.PullToken = token
		if _, err := p.Up(context.Background(), o); err != nil {
			t.Fatal(err)
		}
		ud, _ := base64.StdEncoding.DecodeString(f.instance["user_data"].(string))
		return string(ud)
	}
	if ud := userData("from-request"); !strings.Contains(ud, "'from-request'") || !strings.Contains(ud, "docker login 'ghcr.io' -u 'acme'") {
		t.Errorf("no login with the caller's token:\n%s", ud)
	}
	if ud := userData(""); strings.Contains(ud, "docker login") || strings.Contains(ud, "ghp_pull") {
		t.Errorf("logged in with the environment's token:\n%s", ud)
	}
}

func TestPlacesNamesEachRegion(t *testing.T) {
	p := testProvider(t, newFake(t), cfg())
	got, err := p.Places(context.Background())
	if err != nil || got["ord"] != "Chicago, US" || got["ams"] != "Amsterdam, NL" {
		t.Fatalf("got %v %v", got, err)
	}
}

// The catalog is every plan meeting the minimums, not one per region, so the
// user can pick; each carries what it actually has.
func TestOffersListsEveryPlanMeetingTheMinimums(t *testing.T) {
	p := testProvider(t, newFake(t), cfg())
	got, err := p.Offers(context.Background(), providers.Want{VCPU: 4, RAMGiB: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Type != "vc2-4c-8gb" || got[0].VCPU != 4 || got[0].RAMGiB != 8 || got[0].DiskGiB != 160 ||
		got[1].Type != "vc2-8c-32gb" || got[1].RAMGiB != 32 {
		t.Fatalf("got %+v", got)
	}
	ord, _ := p.Offers(context.Background(), providers.Want{VCPU: 2, RAMGiB: 4, DC: "ord"})
	if len(ord) != 2 || ord[0].DC != "ord" || ord[1].DC != "ord" {
		t.Fatalf("DC filter: got %+v", ord)
	}
}

// A picked type is launched as picked, even when a cheaper one also fits; a
// type the volume's region does not sell is refused before anything is made.
func TestUpLaunchesThePickedType(t *testing.T) {
	f := newFake(t)
	p := testProvider(t, f, cfg())
	o := opts(2, 4, 50)
	o.Type = "vc2-8c-32gb"
	if _, err := p.Up(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if f.instance["plan"] != "vc2-8c-32gb" {
		t.Errorf("plan = %v", f.instance["plan"])
	}
	f2 := newFake(t)
	p2 := testProvider(t, f2, cfg())
	o.Type = "vc2-2c-4gb-ord-only"
	if _, err := p2.Up(context.Background(), o); err == nil || !strings.Contains(err.Error(), "ewr") || f2.instance != nil {
		t.Errorf("unsold type: err=%v created=%v", err, f2.instance != nil)
	}
}
