package serve

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

const pullToken = "ghp_SECRETPULL321"

func TestTheRegistryTokenReachesTheLaunch(t *testing.T) {
	f := &fake{}
	s, _, _ := testServer(f)
	hdr := map[string]string{HeaderRunPodKey: runpodKey, HeaderRegistry: pullToken, "Content-Type": "application/json"}
	if w := do(t, s.Handler(), "POST", "/api/up", `{"name":"ab"}`, hdr); w.Code != http.StatusOK || f.upOpts == nil {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if f.upOpts.PullToken != pullToken {
		t.Errorf("pull token = %q", f.upOpts.PullToken)
	}
}

func TestAServerHeldRegistryTokenWins(t *testing.T) {
	f := &fake{}
	s, _, _ := testServer(f)
	s.Secrets = func(context.Context) (Keys, error) { return Keys{Registry: "held"}, nil }
	hdr := map[string]string{HeaderRunPodKey: runpodKey, HeaderRegistry: "tab", "Content-Type": "application/json"}
	do(t, s.Handler(), "POST", "/api/up", `{"name":"ab"}`, hdr)
	if f.upOpts == nil || f.upOpts.PullToken != "held" {
		t.Errorf("launched with %+v", f.upOpts)
	}
	body := do(t, s.Handler(), "GET", "/api/keys", "", nil).Body.String()
	if !strings.Contains(body, `"registry":true`) || !strings.Contains(body, `"registryEnv":"GH_MEGH_TOKEN"`) {
		t.Errorf("/api/keys = %s", body)
	}
}

// The page learns the note's name for the token from /api/keys, so a
// renamed token_env still matches the secrets file.
func TestKeysNamesTheConfiguredRegistryEnv(t *testing.T) {
	s, _, _ := testServer(&fake{})
	s.Config.Registries[0].TokenEnv = "GHCR_PULL"
	if body := do(t, s.Handler(), "GET", "/api/keys", "", nil).Body.String(); !strings.Contains(body, `"registryEnv":"GHCR_PULL"`) {
		t.Errorf("/api/keys = %s", body)
	}
}

// A pull token opens no backend, so on its own it is still a 401.
func TestARegistryTokenAloneIsNotAProviderKey(t *testing.T) {
	s, seen, _ := testServer(&fake{})
	if w := do(t, s.Handler(), "GET", "/api/boxes", "", map[string]string{HeaderRegistry: pullToken}); w.Code != http.StatusUnauthorized || len(*seen) != 0 {
		t.Errorf("code=%d", w.Code)
	}
}

func TestTheRegistryTokenNeverAppearsInAResponse(t *testing.T) {
	f := &fake{listErr: errors.New("echo " + pullToken)}
	s, _, logs := testServer(f)
	w := do(t, s.Handler(), "GET", "/api/boxes", "", map[string]string{HeaderRunPodKey: runpodKey, HeaderRegistry: pullToken})
	if strings.Contains(w.Body.String(), pullToken) || strings.Contains(logs.String(), pullToken) {
		t.Errorf("leaked: %s", w.Body.String())
	}
}
