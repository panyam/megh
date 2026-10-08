package serve

import (
	"net/http"
	"strings"
)

// Capability is one thing a box launched from the page can or cannot do,
// given the keys present (the server's, else the tab's). Fix names what to add
// when it cannot.
type Capability struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Label string `json:"label"`
	Fix   string `json:"fix,omitempty"`
}

// capabilities answers GET /api/capabilities: what the present keys allow,
// and per provider what a launch there would lack. It reports consequences of
// which keys exist, never their values, so like /api/keys it needs no
// provider key to answer.
func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	k, _ := s.keysFor(r)
	have := map[string]bool{"runpod": k.RunPod != "", "hetzner": k.Hetzner != "", "vultr": k.Vultr != ""}
	tailnet := k.TSClientID != "" && k.TSClientSecret != ""
	image := k.Registry != ""
	logins := pubkeyNames(s.Config.ExtraPubKeys)

	var caps []Capability
	for _, kv := range keyVars {
		caps = append(caps, Capability{ID: "launch-" + kv.provider, OK: have[kv.provider],
			Label: "Launch boxes on " + kv.provider, Fix: "add " + kv.env})
	}
	caps = append(caps,
		Capability{ID: "tailnet", OK: tailnet, Label: "New boxes join your tailnet, so they are reachable by name and their web surfaces open from any device",
			Fix: "add MEGH_TAILSCALE_CLIENT_ID and MEGH_TAILSCALE_CLIENT_SECRET"},
		Capability{ID: "image", OK: image, Label: "Hetzner and Vultr boxes can pull the private megh image",
			Fix: "add " + s.RegistryEnv() + "; RunPod pulls with its console credential and does not need it"},
		Capability{ID: "login", OK: len(logins) > 0, Label: "Boxes launched here authorize " + orNobody(logins),
			Fix: "add a public key to extra_pubkeys in megh.yaml: boxes launched here trust only those, since the server has no key of its own"},
	)

	warnings := map[string][]string{}
	for _, kv := range keyVars {
		var ws []string
		if !tailnet {
			ws = append(ws, "won't join your tailnet (no Tailscale keys): reach it only over public SSH")
		}
		if !image && kv.provider != "runpod" {
			ws = append(ws, "can't pull the private image (no "+s.RegistryEnv()+"): the VM boots but the box never starts")
		}
		if len(logins) == 0 {
			ws = append(ws, "nobody can log in: extra_pubkeys is empty")
		}
		warnings[kv.provider] = ws
	}
	writeJSON(w, http.StatusOK, map[string]any{"capabilities": caps, "warnings": warnings})
}

// pubkeyNames is each key's comment (the third field), else its type, which is
// how a person recognizes "their" key without reading base64.
func pubkeyNames(keys []string) []string {
	var out []string
	for _, k := range keys {
		f := strings.Fields(k)
		switch {
		case len(f) >= 3:
			out = append(out, strings.Join(f[2:], " "))
		case len(f) >= 1:
			out = append(out, f[0]+" key")
		}
	}
	return out
}

func orNobody(names []string) string {
	if len(names) == 0 {
		return "no key, so nobody can log in"
	}
	return "your key " + strings.Join(names, ", ")
}
