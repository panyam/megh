package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

type capsReply struct {
	Capabilities []Capability       `json:"capabilities"`
	Warnings     map[string][]string `json:"warnings"`
}

func caps(t *testing.T, s *Server, hdr map[string]string) capsReply {
	t.Helper()
	w := do(t, s.Handler(), "GET", "/api/capabilities", "", hdr)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	var r capsReply
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func capOK(r capsReply, id string) (ok, found bool) {
	for _, c := range r.Capabilities {
		if c.ID == id {
			return c.OK, true
		}
	}
	return false, false
}

// With only a Vultr key the page should say exactly what that buys: Vultr
// launches, nothing else, and a Vultr box that cannot join the tailnet or pull
// the private image. It needs no provider key to answer, like /api/keys.
func TestCapabilitiesFollowTheKeysPresent(t *testing.T) {
	s, _, _ := testServer(&fake{})
	r := caps(t, s, map[string]string{HeaderVultrKey: "vk"})
	for id, want := range map[string]bool{"launch-vultr": true, "launch-runpod": false, "launch-hetzner": false, "tailnet": false, "image": false, "login": true} {
		if got, found := capOK(r, id); !found || got != want {
			t.Errorf("%s: ok=%v found=%v, want %v", id, got, found, want)
		}
	}
	if len(r.Warnings["vultr"]) != 2 {
		t.Errorf("vultr warnings = %v, want tailnet + image", r.Warnings["vultr"])
	}
	if none := caps(t, s, nil); len(none.Capabilities) == 0 {
		t.Error("no keys at all must still answer")
	}
}

// RunPod pulls with its console credential, so a missing registry token is a
// Vultr/Hetzner warning only.
func TestThePullTokenWarningIsForVMBackendsOnly(t *testing.T) {
	s, _, _ := testServer(&fake{})
	r := caps(t, s, map[string]string{HeaderRunPodKey: "r", HeaderHcloudToken: "h", HeaderTSID: "id", HeaderTSSecret: "sec"})
	if len(r.Warnings["runpod"]) != 0 {
		t.Errorf("runpod warnings = %v", r.Warnings["runpod"])
	}
	if len(r.Warnings["hetzner"]) != 1 {
		t.Errorf("hetzner warnings = %v, want the image one only", r.Warnings["hetzner"])
	}
}

// Keys the server holds count the same as the tab's.
func TestServerHeldKeysCountTowardCapabilities(t *testing.T) {
	s, _, _ := testServer(&fake{})
	s.Secrets = func(context.Context) (Keys, error) {
		return Keys{Vultr: "vk", TSClientID: "id", TSClientSecret: "sec", Registry: "ghp"}, nil
	}
	r := caps(t, s, nil)
	if len(r.Warnings["vultr"]) != 0 {
		t.Errorf("vultr warnings = %v", r.Warnings["vultr"])
	}
	if ok, _ := capOK(r, "tailnet"); !ok {
		t.Error("server-held Tailscale keys not counted")
	}
}

// Boxes launched from the page trust only extra_pubkeys; with none, nobody can
// log in, and every launch is warned.
func TestNoExtraPubkeysMeansNobodyCanLogIn(t *testing.T) {
	s, _, _ := testServer(&fake{})
	s.Config.ExtraPubKeys = nil
	r := caps(t, s, map[string]string{HeaderVultrKey: "vk", HeaderTSID: "id", HeaderTSSecret: "sec", HeaderRegistry: "ghp"})
	if ok, _ := capOK(r, "login"); ok {
		t.Error("login reported possible with no extra_pubkeys")
	}
	if len(r.Warnings["vultr"]) != 1 {
		t.Errorf("vultr warnings = %v, want the login one", r.Warnings["vultr"])
	}
}
