package serve

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/panyam/megh/internal/providers/runpod"
)

// A size is a whole shape: RAM and disk follow the vCPU count, so the page
// cannot ask for a disk RunPod would refuse at that size.
func TestUpTakesAWholeSize(t *testing.T) {
	f := &fake{}
	s, _, _ := testServer(f)
	w := do(t, s.Handler(), "POST", "/api/up", `{"name":"ab","vcpu":2}`, withKey)
	if w.Code != http.StatusOK || f.upOpts == nil {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if o := f.upOpts; o.VCPU != 2 || o.RAMGiB != 8 || o.DiskGiB != 20 {
		t.Errorf("2 vCPU launched as %d/%d/%d", o.VCPU, o.RAMGiB, o.DiskGiB)
	}
}

func TestUpRefusesAnUnofferedSize(t *testing.T) {
	f := &fake{}
	s, _, _ := testServer(f)
	if w := do(t, s.Handler(), "POST", "/api/up", `{"name":"ab","vcpu":3}`, withKey); w.Code != http.StatusBadRequest || f.upOpts != nil {
		t.Errorf("code=%d launched=%v", w.Code, f.upOpts != nil)
	}
}

func TestUpWithoutASizeKeepsTheConfiguredDefault(t *testing.T) {
	f := &fake{}
	s, _, _ := testServer(f)
	want := s.Config.Provider("runpod").VCPU
	if want == 0 {
		want = 2
	}
	do(t, s.Handler(), "POST", "/api/up", `{"name":"ab"}`, withKey)
	if f.upOpts == nil || f.upOpts.VCPU != want {
		t.Errorf("default size changed: %+v", f.upOpts)
	}
}

// A full data center is transient, not a fault: 503 with the advice intact.
func TestUpReportsNoCapacityAs503(t *testing.T) {
	f := &fake{upErr: fmt.Errorf("%w: RunPod has no CPU machine with 4 vCPU free in US-IL-1 right now; ask for a smaller box", runpod.ErrNoCapacity)}
	s, _, _ := testServer(f)
	w := do(t, s.Handler(), "POST", "/api/up", `{"name":"ab"}`, withKey)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "smaller box") {
		t.Errorf("code=%d body=%s", w.Code, w.Body.String())
	}
}
