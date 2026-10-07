package serve

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/panyam/megh/internal/providers"
	"github.com/panyam/megh/internal/providers/runpod"
)

// prober is the fake backend plus RunPod's region search.
type prober struct {
	fake
	dcs    []string
	probed []providers.Options
	answer func(dc string) runpod.ProbeResult
}

func (p *prober) DataCenters(context.Context) []string { return p.dcs }
func (p *prober) Probe(_ context.Context, o providers.Options) runpod.ProbeResult {
	p.probed = append(p.probed, o)
	return p.answer(o.DataCenter)
}

func newProber() *prober {
	p := &prober{dcs: []string{"EU-RO-1", "US-CA-2", "US-IL-1"}}
	p.vols = []providers.Volume{{Provider: "runpod", ID: "vol-il", Name: "megh-work", DataCenter: "US-IL-1", Size: 100}}
	p.answer = func(dc string) runpod.ProbeResult {
		if dc == "US-CA-2" {
			return runpod.ProbeResult{DC: dc, Rentable: true, PodID: "pod-1"}
		}
		return runpod.ProbeResult{DC: dc, Err: runpod.ErrNoCapacity}
	}
	return p
}

func TestCreateVolumeChecksItsInputsBeforeCreating(t *testing.T) {
	p := newProber()
	s, _, _ := testServer(&p.fake)
	s.Backends = func(Keys) []providers.Provider { return []providers.Provider{p} }
	h := s.Handler()
	for _, bad := range []string{
		`{"name":"Bad Name","sizeGB":50,"dc":"US-CA-2"}`,
		`{"name":"megh-ca","sizeGB":37,"dc":"US-CA-2"}`,
		`{"name":"megh-ca","sizeGB":50,"dc":"MARS-1"}`,
	} {
		if w := do(t, h, "POST", "/api/volumes", bad, withKey); w.Code != http.StatusBadRequest {
			t.Errorf("%s: code=%d", bad, w.Code)
		}
	}
	if p.created != nil {
		t.Fatal("an invalid request created a volume")
	}
	w := do(t, h, "POST", "/api/volumes", `{"name":"megh-ca","sizeGB":50,"dc":"US-CA-2"}`, withKey)
	if w.Code != http.StatusOK || p.created == nil || p.created.DataCenter != "US-CA-2" || p.created.Size != 50 {
		t.Errorf("code=%d created=%+v", w.Code, p.created)
	}
}

// Deleting a volume needs its name typed back, so a stray request cannot.
func TestDeleteVolumeNeedsItsNameConfirmed(t *testing.T) {
	p := newProber()
	s, _, _ := testServer(&p.fake)
	s.Backends = func(Keys) []providers.Provider { return []providers.Provider{p} }
	h := s.Handler()
	for body, want := range map[string]int{
		`{"id":"vol-il"}`:                         http.StatusBadRequest,
		`{"id":"vol-il","confirm":"wrong"}`:       http.StatusBadRequest,
		`{"id":"vol-nope","confirm":"megh-work"}`: http.StatusNotFound,
	} {
		if w := do(t, h, "POST", "/api/volumes/delete", body, withKey); w.Code != want {
			t.Errorf("%s: code=%d want %d", body, w.Code, want)
		}
	}
	if p.deleted != "" {
		t.Fatal("deleted without a matching confirm")
	}
	if w := do(t, h, "POST", "/api/volumes/delete", `{"id":"vol-il","confirm":"megh-work"}`, withKey); w.Code != http.StatusOK || p.deleted != "vol-il" {
		t.Errorf("code=%d deleted=%q", w.Code, p.deleted)
	}
}

func TestRegionsListsUSByDefaultAndAllOnRequest(t *testing.T) {
	p := newProber()
	s, _, _ := testServer(&p.fake)
	s.Backends = func(Keys) []providers.Provider { return []providers.Provider{p} }
	body := do(t, s.Handler(), "GET", "/api/regions", "", withKey).Body.String()
	if strings.Contains(body, "EU-RO-1") || !strings.Contains(body, "US-CA-2") {
		t.Errorf("default should be US only: %s", body)
	}
	if body := do(t, s.Handler(), "GET", "/api/regions?all=1", "", withKey).Body.String(); !strings.Contains(body, "EU-RO-1") {
		t.Errorf("all=1 should include every region: %s", body)
	}
}

func TestProbeAnswersForOneDataCenter(t *testing.T) {
	p := newProber()
	s, _, _ := testServer(&p.fake)
	s.Backends = func(Keys) []providers.Provider { return []providers.Provider{p} }
	h := s.Handler()
	if body := do(t, h, "POST", "/api/regions/probe", `{"dc":"US-IL-1"}`, withKey).Body.String(); !strings.Contains(body, `"verdict":"no capacity"`) {
		t.Errorf("dry region: %s", body)
	}
	body := do(t, h, "POST", "/api/regions/probe", `{"dc":"US-CA-2","vcpu":2}`, withKey).Body.String()
	if !strings.Contains(body, `"rentable":true`) || strings.Contains(body, "orphanId") {
		t.Errorf("rentable region: %s", body)
	}
	if last := p.probed[len(p.probed)-1]; last.VCPU != 2 || last.RAMGiB != 8 || last.DiskGiB != 20 || last.Image == "" {
		t.Errorf("probed shape %+v", last)
	}
	if w := do(t, h, "POST", "/api/regions/probe", `{"dc":"MARS-1"}`, withKey); w.Code != http.StatusBadRequest {
		t.Errorf("unknown dc: code=%d", w.Code)
	}
}

// A probe pod that could not be terminated is still billing; the answer must
// say so with its id, so the page can offer to terminate it.
func TestProbeReportsAnOrphanedProbePod(t *testing.T) {
	p := newProber()
	p.answer = func(dc string) runpod.ProbeResult {
		return runpod.ProbeResult{DC: dc, Rentable: true, PodID: "pod-stuck", Orphan: errors.New("terminate failed")}
	}
	s, _, _ := testServer(&p.fake)
	s.Backends = func(Keys) []providers.Provider { return []providers.Provider{p} }
	body := do(t, s.Handler(), "POST", "/api/regions/probe", `{"dc":"US-CA-2"}`, withKey).Body.String()
	if !strings.Contains(body, `"orphanId":"pod-stuck"`) || !strings.Contains(body, "could not be terminated") {
		t.Errorf("orphan not surfaced: %s", body)
	}
}

func TestNewEndpointsNeedAKey(t *testing.T) {
	p := newProber()
	s, _, _ := testServer(&p.fake)
	s.Backends = func(Keys) []providers.Provider { return []providers.Provider{p} }
	h := s.Handler()
	for _, c := range []struct{ m, path, body string }{
		{"POST", "/api/volumes", `{}`}, {"POST", "/api/volumes/delete", `{}`},
		{"GET", "/api/regions", ""}, {"POST", "/api/regions/probe", `{}`},
	} {
		w := do(t, h, c.m, c.path, c.body, map[string]string{"Content-Type": "application/json"})
		if w.Code != http.StatusUnauthorized || w.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s %s: code=%d", c.m, c.path, w.Code)
		}
	}
}
