package serve

import (
	"net/http"
	"strings"
	"testing"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/providers"
)

func volumeFake() *fake {
	return &fake{vols: []providers.Volume{
		{Provider: "runpod", ID: "vol-il", Name: "megh-work", DataCenter: "US-IL-1", Size: 100},
		{Provider: "runpod", ID: "vol-ca", Name: "megh-work-2", DataCenter: "US-CA-2", Size: 50},
	}}
}

func TestVolumesListsThemWithTheConfiguredDefault(t *testing.T) {
	s, _, _ := testServer(volumeFake())
	s.Config.Providers = map[string]config.Provider{"runpod": {DefaultVolume: "vol-il", DefaultDC: "US-IL-1"}}
	body := do(t, s.Handler(), "GET", "/api/volumes", "", withKey).Body.String()
	for _, want := range []string{`"id":"vol-ca"`, `"dc":"US-CA-2"`, `"sizeGB":50`, `"default":"vol-il"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
}

// Choosing a volume chooses the region: the box must start in that volume's
// data center, whatever megh.yaml's default DC is.
func TestUpOntoAChosenVolumeStartsInItsDataCenter(t *testing.T) {
	f := volumeFake()
	s, _, _ := testServer(f)
	s.Config.Providers = map[string]config.Provider{"runpod": {DefaultVolume: "vol-il", DefaultDC: "US-IL-1"}}
	w := do(t, s.Handler(), "POST", "/api/up", `{"name":"ab","vcpu":2,"volume":"vol-ca"}`, withKey)
	if w.Code != http.StatusOK || f.upOpts == nil {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if f.upOpts.VolumeID != "vol-ca" || f.upOpts.DataCenter != "US-CA-2" {
		t.Errorf("launched on %s in %s", f.upOpts.VolumeID, f.upOpts.DataCenter)
	}
}

func TestUpRefusesAVolumeThatIsNotOnTheAccount(t *testing.T) {
	f := volumeFake()
	s, _, _ := testServer(f)
	if w := do(t, s.Handler(), "POST", "/api/up", `{"name":"ab","volume":"vol-nope"}`, withKey); w.Code != http.StatusBadRequest || f.upOpts != nil {
		t.Errorf("code=%d launched=%v", w.Code, f.upOpts != nil)
	}
}

func TestUpWithoutAVolumeKeepsTheConfiguredPair(t *testing.T) {
	f := volumeFake()
	s, _, _ := testServer(f)
	s.Config.Providers = map[string]config.Provider{"runpod": {DefaultVolume: "vol-il", DefaultDC: "US-IL-1"}}
	do(t, s.Handler(), "POST", "/api/up", `{"name":"ab"}`, withKey)
	if f.upOpts == nil || f.upOpts.VolumeID != "vol-il" || f.upOpts.DataCenter != "US-IL-1" {
		t.Errorf("default pair changed: %+v", f.upOpts)
	}
}
