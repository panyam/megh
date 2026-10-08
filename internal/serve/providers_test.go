package serve

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/providers"
)

const (
	hcloudToken = "hc_SECRETTOKEN456"
	vultrKey    = "vk_SECRETKEY789"
)

// locator is a fake VM backend with a catalog, as Hetzner and Vultr have.
type locator struct {
	fake
	offers []providers.Offer
	asked  []int
}

func (l *locator) Offers(_ context.Context, vcpu, ram, disk int) ([]providers.Offer, error) {
	l.asked = append(l.asked, vcpu, ram, disk)
	return l.offers, nil
}

func newLocator() *locator {
	l := &locator{offers: []providers.Offer{{DC: "ord", Type: "vc2-2c-4gb", PerHr: 0.027}, {DC: "ewr", Type: "vc2-2c-8gb", PerHr: 0.05}}}
	l.name = "vultr"
	l.vols = []providers.Volume{{Provider: "vultr", ID: "b1", Name: "megh-vw", DataCenter: "ewr", Size: 50}}
	return l
}

// twoBackends is a server with a RunPod fake and a Vultr fake, both present.
func twoBackends(rp *fake, v providers.Provider) *Server {
	s, _, _ := testServer(rp)
	s.Backends = func(Keys) []providers.Provider { return []providers.Provider{rp, v} }
	return s
}

func TestNewBuildsOnlyTheBackendsWithAKey(t *testing.T) {
	s := New(config.Default())
	for _, c := range []struct {
		k    Keys
		want []string
	}{
		{Keys{RunPod: "r"}, []string{"runpod"}},
		{Keys{Vultr: "v"}, []string{"vultr"}},
		{Keys{RunPod: "r", Hetzner: "h", Vultr: "v"}, []string{"runpod", "hetzner", "vultr"}},
		{Keys{}, nil},
	} {
		var got []string
		for _, p := range s.Backends(c.k) {
			got = append(got, p.Name())
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%+v: got %v, want %v", c.k, got, c.want)
		}
	}
}

// RunPod is no longer required: any one provider key opens the API.
func TestAVultrKeyAloneOpensTheAPI(t *testing.T) {
	s, seen, _ := testServer(&fake{name: "vultr"})
	w := do(t, s.Handler(), "GET", "/api/boxes", "", map[string]string{HeaderVultrKey: vultrKey})
	if w.Code != http.StatusOK || len(*seen) != 1 || (*seen)[0].Vultr != vultrKey || (*seen)[0].RunPod != "" {
		t.Errorf("code=%d keys=%+v body=%s", w.Code, *seen, w.Body.String())
	}
	s, seen, _ = testServer(&fake{name: "hetzner"})
	do(t, s.Handler(), "GET", "/api/boxes", "", map[string]string{HeaderHcloudToken: hcloudToken})
	if len(*seen) != 1 || (*seen)[0].Hetzner != hcloudToken {
		t.Errorf("hetzner keys=%+v", *seen)
	}
}

func TestNoProviderKeyNamesAllThree(t *testing.T) {
	s, seen, _ := testServer(&fake{})
	w := do(t, s.Handler(), "GET", "/api/boxes", "", map[string]string{HeaderTSID: "id", HeaderTSSecret: "sec"})
	if w.Code != http.StatusUnauthorized || len(*seen) != 0 {
		t.Fatalf("code=%d", w.Code)
	}
	for _, name := range []string{"RUNPOD_API_KEY", "HCLOUD_TOKEN", "VULTR_API_KEY"} {
		if !strings.Contains(w.Body.String(), name) {
			t.Errorf("401 does not name %s: %s", name, w.Body.String())
		}
	}
}

func TestServerHeldVMKeysWinAndAreReported(t *testing.T) {
	s, seen, _ := testServer(&fake{})
	s.Secrets = func(context.Context) (Keys, error) { return Keys{Hetzner: "held-h"}, nil }
	do(t, s.Handler(), "GET", "/api/boxes", "", map[string]string{HeaderHcloudToken: "tab-h", HeaderVultrKey: "tab-v"})
	if len(*seen) != 1 || (*seen)[0].Hetzner != "held-h" || (*seen)[0].Vultr != "tab-v" {
		t.Errorf("merged keys = %+v", *seen)
	}
	body := do(t, s.Handler(), "GET", "/api/keys", "", nil).Body.String()
	if !strings.Contains(body, `"hetzner":true`) || !strings.Contains(body, `"vultr":false`) {
		t.Errorf("/api/keys = %s", body)
	}
}

func TestVMKeysNeverAppearInAResponse(t *testing.T) {
	f := &fake{listErr: errors.New("echo " + hcloudToken + " and " + vultrKey)}
	s, _, logs := testServer(f)
	w := do(t, s.Handler(), "GET", "/api/boxes", "", map[string]string{HeaderHcloudToken: hcloudToken, HeaderVultrKey: vultrKey})
	for _, k := range []string{hcloudToken, vultrKey} {
		if strings.Contains(w.Body.String(), k) || strings.Contains(logs.String(), k) {
			t.Errorf("leaked %s: %s", k, w.Body.String())
		}
	}
}

func TestBoxesComeFromEveryBackendTaggedWithIt(t *testing.T) {
	rp := &fake{boxes: []providers.Box{{ID: "p1", Name: "megh-pod", Status: "RUNNING"}}}
	v := newLocator()
	v.boxes = []providers.Box{{ID: "i-1", Name: "megh-vm", Status: "RUNNING"}}
	body := do(t, twoBackends(rp, v).Handler(), "GET", "/api/boxes", "", withKey).Body.String()
	for _, want := range []string{`"name":"pod","provider":"runpod"`, `"name":"vm","provider":"vultr"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
}

// One backend down must not hide the boxes on the other.
func TestOneFailingBackendStillListsTheOthers(t *testing.T) {
	rp := &fake{listErr: errors.New("runpod: HTTP 500")}
	v := newLocator()
	v.boxes = []providers.Box{{ID: "i-1", Name: "megh-vm", Status: "RUNNING"}}
	w := do(t, twoBackends(rp, v).Handler(), "GET", "/api/boxes", "", withKey)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"name":"vm"`) || !strings.Contains(w.Body.String(), "runpod: HTTP 500") {
		t.Errorf("code=%d body=%s", w.Code, w.Body.String())
	}
}

// A volume belongs to one provider, so launching onto it launches there.
func TestUpOntoAVultrVolumeLaunchesOnVultr(t *testing.T) {
	rp, v := &fake{}, newLocator()
	w := do(t, twoBackends(rp, v).Handler(), "POST", "/api/up", `{"name":"ab","vcpu":4,"volume":"b1"}`, withKey)
	if w.Code != http.StatusOK || v.upOpts == nil || rp.upOpts != nil {
		t.Fatalf("code=%d vultr=%v runpod=%v body=%s", w.Code, v.upOpts != nil, rp.upOpts != nil, w.Body.String())
	}
	if v.upOpts.VolumeID != "b1" || v.upOpts.DataCenter != "ewr" || v.upOpts.VCPU != 4 {
		t.Errorf("launched %+v", v.upOpts)
	}
}

// With only a Vultr key, the default backend is Vultr rather than an
// "unknown provider runpod".
func TestTheDefaultBackendIsOneTheRequestHoldsAKeyFor(t *testing.T) {
	v := newLocator()
	s, _, _ := testServer(&v.fake)
	s.Backends = func(Keys) []providers.Provider { return []providers.Provider{v} }
	s.Config.Providers = map[string]config.Provider{"vultr": {DefaultVolume: "b1", DefaultDC: "ewr"}}
	body := do(t, s.Handler(), "GET", "/api/volumes", "", withKey).Body.String()
	for _, want := range []string{`"provider":"vultr"`, `"providers":["vultr"]`, `"default":"b1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
	if w := do(t, s.Handler(), "POST", "/api/up", `{"name":"ab"}`, withKey); w.Code != http.StatusOK || v.upOpts == nil || v.upOpts.VolumeID != "b1" {
		t.Errorf("code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestDownFindsTheBoxOnWhicheverBackendHasIt(t *testing.T) {
	rp, v := &fake{}, newLocator()
	v.boxes = []providers.Box{{ID: "i-1", Name: "megh-vm", Status: "RUNNING"}}
	w := do(t, twoBackends(rp, v).Handler(), "POST", "/api/down", `{"name":"vm"}`, withKey)
	if w.Code != http.StatusOK || v.killed != "i-1" || rp.killed != "" {
		t.Errorf("code=%d vultr killed=%q runpod killed=%q", w.Code, v.killed, rp.killed)
	}
}

// The same name on two backends is ambiguous, and this is a terminate.
func TestDownRefusesANameOnTwoBackends(t *testing.T) {
	rp := &fake{boxes: []providers.Box{{ID: "p1", Name: "megh-ab", Status: "RUNNING"}}}
	v := newLocator()
	v.boxes = []providers.Box{{ID: "i-1", Name: "megh-ab", Status: "RUNNING"}}
	w := do(t, twoBackends(rp, v).Handler(), "POST", "/api/down", `{"name":"ab"}`, withKey)
	if w.Code != http.StatusConflict || rp.killed != "" || v.killed != "" {
		t.Errorf("code=%d killed=%q/%q", w.Code, rp.killed, v.killed)
	}
}

// A catalog backend answers the region question with offers for the size
// asked, not data centers to probe, and has nothing to probe.
func TestRegionsForACatalogBackendAreOffers(t *testing.T) {
	v := newLocator()
	s := twoBackends(&fake{}, v)
	h := s.Handler()
	w := do(t, h, "GET", "/api/regions?provider=vultr&vcpu=4", "", withKey)
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `"offers":[{"dc":"ord","type":"vc2-2c-4gb","perHr":0.027}`) || !strings.Contains(body, `"dcs":["ord","ewr"]`) {
		t.Errorf("code=%d body=%s", w.Code, body)
	}
	if !slices.Equal(v.asked, []int{4, 16, 40}) {
		t.Errorf("asked for %v", v.asked)
	}
	if w := do(t, h, "GET", "/api/regions?provider=vultr&vcpu=3", "", withKey); w.Code != http.StatusBadRequest {
		t.Errorf("unoffered size: code=%d", w.Code)
	}
	if w := do(t, h, "POST", "/api/regions/probe", `{"provider":"vultr","dc":"ewr"}`, withKey); w.Code != http.StatusNotImplemented {
		t.Errorf("probe on a catalog backend: code=%d", w.Code)
	}
	if w := do(t, h, "GET", "/api/regions?provider=hetzner", "", withKey); w.Code != http.StatusBadRequest {
		t.Errorf("a backend with no key: code=%d", w.Code)
	}
}

func TestCreateVolumeOnACatalogBackendChecksItsLocations(t *testing.T) {
	v := newLocator()
	h := twoBackends(&fake{}, v).Handler()
	if w := do(t, h, "POST", "/api/volumes", `{"provider":"vultr","name":"megh-x","sizeGB":50,"dc":"US-CA-2"}`, withKey); w.Code != http.StatusBadRequest || v.created != nil {
		t.Errorf("a RunPod DC on vultr: code=%d", w.Code)
	}
	w := do(t, h, "POST", "/api/volumes", `{"provider":"vultr","name":"megh-x","sizeGB":50,"dc":"ord"}`, withKey)
	if w.Code != http.StatusOK || v.created == nil || v.created.DataCenter != "ord" || !strings.Contains(w.Body.String(), `"provider":"vultr"`) {
		t.Errorf("code=%d created=%+v", w.Code, v.created)
	}
}

func TestDeleteVolumeGoesToItsOwnBackend(t *testing.T) {
	rp, v := &fake{}, newLocator()
	w := do(t, twoBackends(rp, v).Handler(), "POST", "/api/volumes/delete", `{"id":"b1","confirm":"megh-vw"}`, withKey)
	if w.Code != http.StatusOK || v.deleted != "b1" || rp.deleted != "" {
		t.Errorf("code=%d vultr=%q runpod=%q", w.Code, v.deleted, rp.deleted)
	}
}

// The page lists every provider it could use, keyed or not, before any key is
// pasted, with the variable that unlocks each.
func TestKeysListsEveryProviderWithItsVariable(t *testing.T) {
	s, _, _ := testServer(&fake{})
	body := do(t, s.Handler(), "GET", "/api/keys", "", nil).Body.String()
	want := `"providers":[{"env":"RUNPOD_API_KEY","name":"runpod"},{"env":"HCLOUD_TOKEN","name":"hetzner"},{"env":"VULTR_API_KEY","name":"vultr"}]`
	if !strings.Contains(body, want) {
		t.Errorf("/api/keys = %s", body)
	}
}

// Asking for a known provider the request has no key for says which key to add.
func TestAKnownProviderWithNoKeyNamesItsVariable(t *testing.T) {
	s, _, _ := testServer(&fake{})
	h := s.Handler()
	w := do(t, h, "POST", "/api/volumes", `{"provider":"vultr","name":"megh-x","sizeGB":50,"dc":"ewr"}`, withKey)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "add VULTR_API_KEY") {
		t.Errorf("code=%d body=%s", w.Code, w.Body.String())
	}
	if w := do(t, h, "GET", "/api/regions?provider=nope", "", withKey); w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "add ") {
		t.Errorf("unknown provider: code=%d body=%s", w.Code, w.Body.String())
	}
}

// placer is the fake catalog backend that can also name its locations.
type placer struct {
	locator
	places map[string]string
	err    error
}

func (p *placer) Places(context.Context) (map[string]string, error) { return p.places, p.err }

func newPlacer() *placer {
	p := &placer{locator: *newLocator(), places: map[string]string{"ord": "Chicago, US", "ewr": "New Jersey, US"}}
	p.boxes = []providers.Box{{ID: "i-1", Name: "megh-vm", Status: "RUNNING", DataCenter: "ord"}}
	return p
}

// Codes alone ("ord") say nothing to a person; every place the page shows one
// gets the backend's own name for it.
func TestRegionsVolumesAndBoxesCarryPlaceNames(t *testing.T) {
	v := newPlacer()
	v.vols = []providers.Volume{{Provider: "vultr", ID: "b1", Name: "megh-vw", DataCenter: "ord", Size: 50}}
	h := twoBackends(&fake{}, v).Handler()
	if body := do(t, h, "GET", "/api/regions?provider=vultr&vcpu=2", "", withKey).Body.String(); !strings.Contains(body, `"places":{"ewr":"New Jersey, US","ord":"Chicago, US"}`) {
		t.Errorf("regions: %s", body)
	}
	if body := do(t, h, "GET", "/api/volumes", "", withKey).Body.String(); !strings.Contains(body, `"dc":"ord","place":"Chicago, US"`) {
		t.Errorf("volumes: %s", body)
	}
	if body := do(t, h, "GET", "/api/boxes", "", withKey).Body.String(); !strings.Contains(body, `"place":"Chicago, US"`) {
		t.Errorf("boxes: %s", body)
	}
}

// A backend that cannot name its locations (no Placer, or the call failing)
// still lists everything, with bare codes.
func TestAFailingPlaceLookupLeavesBareCodes(t *testing.T) {
	v := newPlacer()
	v.err = errors.New("vultr: HTTP 503")
	w := do(t, twoBackends(&fake{}, v).Handler(), "GET", "/api/regions?provider=vultr&vcpu=2", "", withKey)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"dcs":["ord","ewr"]`) || strings.Contains(w.Body.String(), "Chicago") {
		t.Errorf("code=%d body=%s", w.Code, w.Body.String())
	}
}
