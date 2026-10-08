package runpod

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/panyam/megh/internal/providers"
)

// fakeGraphQL answers the two catalog queries: the flavor rules, and a batch
// of priced instances (aliases i0, i1, ...). An instance missing from prices
// comes back with no specifics, as RunPod does for one it does not sell.
func fakeGraphQL(t *testing.T, prices map[string]float64) *int {
	asked := new(int)
	alias := regexp.MustCompile(`(i\d+): cpuFlavors \{ id specifics\(input:\{dataCenterId:"([^"]+)", instanceId:"([^"]+)"\}\)`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*asked++
		b, _ := io.ReadAll(r.Body)
		q := strings.ReplaceAll(string(b), `\"`, `"`)
		if strings.Contains(q, "ramMultiplier") {
			io.WriteString(w, `{"data":{"cpuFlavors":[
			 {"id":"cpu3c","minVcpu":2,"maxVcpu":8,"ramMultiplier":2,"diskLimitPerVcpu":10},
			 {"id":"cpu3g","minVcpu":2,"maxVcpu":8,"ramMultiplier":4,"diskLimitPerVcpu":10},
			 {"id":"cpu5m","minVcpu":4,"maxVcpu":32,"ramMultiplier":8,"diskLimitPerVcpu":15}]}}`)
			return
		}
		var parts []string
		for _, m := range alias.FindAllStringSubmatch(q, -1) {
			spec := "null"
			if pr, ok := prices[m[3]]; ok {
				spec = `{"stockStatus":"High","securePrice":` + strconvF(pr) + `}`
			}
			parts = append(parts, `"`+m[1]+`":[{"id":"x","specifics":`+spec+`}]`)
		}
		io.WriteString(w, `{"data":{`+strings.Join(parts, ",")+`}}`)
	}))
	t.Cleanup(srv.Close)
	saved := graphqlEndpoint
	graphqlEndpoint = srv.URL
	t.Cleanup(func() { graphqlEndpoint = saved })
	return asked
}

func strconvF(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// The catalog is every machine the flavor rules allow that meets the minimums
// and that RunPod prices in the data center: RAM is vCPU times the flavor's
// ratio and disk is up to vCPU times its per-vCPU limit, so a combination that
// cannot exist never appears.
func TestOffersAreTheMachinesTheFlavorRulesAllow(t *testing.T) {
	asked := fakeGraphQL(t, map[string]float64{"cpu3g-4-16": 0.16, "cpu3g-6-24": 0.24, "cpu3g-8-32": 0.32, "cpu5m-4-32": 0.2, "cpu5m-6-48": 0.3})
	got, err := NewWithKey("k").Offers(context.Background(), providers.Want{VCPU: 4, RAMGiB: 16, DiskGiB: 40, DC: "US-IL-1"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, o := range got {
		ids = append(ids, o.Type)
		if o.DC != "US-IL-1" || o.Stock != "High" {
			t.Errorf("%s: %+v", o.Type, o)
		}
	}
	// cpu3c is 2 GB/vCPU so 16 GB needs 8 vCPU, which it prices nowhere here;
	// cpu5m-8-64 etc. are unpriced; the rest sort by price.
	if strings.Join(ids, ",") != "cpu3g-4-16,cpu5m-4-32,cpu3g-6-24,cpu5m-6-48,cpu3g-8-32" {
		t.Fatalf("got %v", ids)
	}
	if g := got[0]; g.VCPU != 4 || g.RAMGiB != 16 || g.DiskGiB != 40 || g.PerHr != 0.16 {
		t.Errorf("cpu3g-4-16 = %+v", g)
	}
	if g := got[1]; g.DiskGiB != 60 {
		t.Errorf("cpu5m allows 15 GB per vCPU: %+v", g)
	}
	if *asked != 2 {
		t.Errorf("asked RunPod %d times, want 2 (flavors, then one batch of prices)", *asked)
	}
}

func TestOffersNeedADataCenter(t *testing.T) {
	fakeGraphQL(t, nil)
	if _, err := NewWithKey("k").Offers(context.Background(), providers.Want{VCPU: 2}); err == nil || !strings.Contains(err.Error(), "data center") {
		t.Errorf("got %v", err)
	}
}

// A picked instance launches as that one flavor at its vCPU count; without a
// pick the shape is today's: the requested vCPU across every flavor, the
// preferred one first.
func TestPodShapeFollowsThePick(t *testing.T) {
	vcpu, flavors, err := podShape(providers.Options{Type: "cpu5m-6-48", VCPU: 2, RAMGiB: 8})
	if err != nil || vcpu != 6 || strings.Join(flavors, ",") != "cpu5m" {
		t.Errorf("picked: %d %v %v", vcpu, flavors, err)
	}
	vcpu, flavors, _ = podShape(providers.Options{VCPU: 4, RAMGiB: 16})
	if vcpu != 4 || flavors[0] != "cpu5g" || len(flavors) != 6 {
		t.Errorf("unpicked: %d %v", vcpu, flavors)
	}
	if _, _, err := podShape(providers.Options{Type: "nonsense"}); err == nil {
		t.Error("a malformed type must be refused")
	}
}
