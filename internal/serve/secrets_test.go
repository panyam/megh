package serve

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const note = "export RUNPOD_API_KEY=rpa_FROMSECRET MEGH_TAILSCALE_CLIENT_ID=tsid1  # control\nexport MEGH_TAILSCALE_CLIENT_SECRET='tssec1'\n"

// fakeGCP serves the metadata token endpoint and Secret Manager's access
// endpoint, recording what it was asked.
type fakeGCP struct {
	srv      *httptest.Server
	accesses atomic.Int32
	status   int // Secret Manager's reply status; 0 = 200
	gotAuth  string
	gotPath  string
}

func newFakeGCP(t *testing.T) *fakeGCP {
	f := &fakeGCP{}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata-Flavor") != "Google" {
			http.Error(w, "missing Metadata-Flavor", http.StatusForbidden)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-abc", "expires_in": 3599})
	})
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		f.accesses.Add(1)
		f.gotAuth, f.gotPath = r.Header.Get("Authorization"), r.URL.Path
		if f.status != 0 {
			http.Error(w, `{"error":{"message":"Permission 'secretmanager.versions.access' denied"}}`, f.status)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"payload": map[string]string{"data": base64.StdEncoding.EncodeToString([]byte(note))}})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGCP) secret(name string) *GCPSecret {
	return &GCPSecret{Name: name, Project: "meghplane", TokenURL: f.srv.URL + "/token", APIBase: f.srv.URL + "/v1"}
}

func TestGCPSecretReadsTheNoteWithTheInstanceToken(t *testing.T) {
	f := newFakeGCP(t)
	k, err := f.secret("megh-control").Keys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if k != (Keys{RunPod: "rpa_FROMSECRET", TSClientID: "tsid1", TSClientSecret: "tssec1"}) {
		t.Errorf("keys = %+v", k)
	}
	if f.gotAuth != "Bearer tok-abc" {
		t.Errorf("auth = %q", f.gotAuth)
	}
	if f.gotPath != "/v1/projects/meghplane/secrets/megh-control/versions/latest:access" {
		t.Errorf("path = %q", f.gotPath)
	}
}

func TestGCPSecretAcceptsAFullResourceName(t *testing.T) {
	f := newFakeGCP(t)
	if _, err := f.secret("projects/123/secrets/other").Keys(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.gotPath != "/v1/projects/123/secrets/other/versions/latest:access" {
		t.Errorf("path = %q", f.gotPath)
	}
}

// Reads are cached so the app stays inside the free 10,000 accesses a month.
func TestGCPSecretCachesBetweenRequests(t *testing.T) {
	f := newFakeGCP(t)
	s := f.secret("megh-control")
	for i := 0; i < 5; i++ {
		s.Keys(context.Background())
	}
	if n := f.accesses.Load(); n != 1 {
		t.Errorf("accessed Secret Manager %d times, want 1", n)
	}
}

func TestGCPSecretMissingIsEmptyAndOtherFailuresAreErrors(t *testing.T) {
	f := newFakeGCP(t)
	f.status = http.StatusNotFound
	if k, err := f.secret("absent").Keys(context.Background()); err != nil || k != (Keys{}) {
		t.Errorf("a missing secret holds nothing: %+v %v", k, err)
	}
	f2 := newFakeGCP(t)
	f2.status = http.StatusForbidden
	k, err := f2.secret("locked").Keys(context.Background())
	if err == nil || k != (Keys{}) || !strings.Contains(err.Error(), "403") {
		t.Errorf("no access must be an error with empty keys: %+v %v", k, err)
	}
}

// Server keys win per field; the browser fills whatever the server lacks.
func TestServerKeysWinPerFieldAndTheBrowserFillsTheRest(t *testing.T) {
	s, seen, _ := testServer(&fake{})
	s.Secrets = func(context.Context) (Keys, error) { return Keys{RunPod: "rpa_SERVER"}, nil }
	do(t, s.Handler(), "GET", "/api/boxes", "", map[string]string{
		HeaderRunPodKey: "rpa_BROWSER", HeaderTSID: "id_BROWSER", HeaderTSSecret: "sec_BROWSER",
	})
	want := Keys{RunPod: "rpa_SERVER", TSClientID: "id_BROWSER", TSClientSecret: "sec_BROWSER"}
	if len(*seen) != 1 || (*seen)[0] != want {
		t.Errorf("keys used = %+v, want %+v", *seen, want)
	}
}

// With the RunPod key on the server, the page needs to send nothing.
func TestServerHeldKeysNeedNoHeaders(t *testing.T) {
	s, seen, _ := testServer(&fake{})
	s.Secrets = func(context.Context) (Keys, error) { return Keys{RunPod: "rpa_SERVER"}, nil }
	if w := do(t, s.Handler(), "GET", "/api/boxes", "", nil); w.Code != http.StatusOK || len(*seen) != 1 {
		t.Errorf("code=%d body=%s", w.Code, w.Body.String())
	}
}

// Without the key header, the JSON requirement is what stops a cross-site
// form from driving up/down.
func TestServerHeldKeysStillRefuseNonJSONWrites(t *testing.T) {
	f := &fake{}
	s, _, _ := testServer(f)
	s.Secrets = func(context.Context) (Keys, error) { return Keys{RunPod: "rpa_SERVER"}, nil }
	for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", ""} {
		w := do(t, s.Handler(), "POST", "/api/up", `{"name":"ab"}`, map[string]string{"Content-Type": ct})
		if w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("%q: code=%d", ct, w.Code)
		}
	}
	if f.upOpts != nil {
		t.Error("a cross-site shaped request launched a box")
	}
}

func TestServerKeysAreScrubbedToo(t *testing.T) {
	f := &fake{listErr: errorString("runpod: rejected rpa_SERVERSECRET")}
	s, _, _ := testServer(f)
	s.Secrets = func(context.Context) (Keys, error) { return Keys{RunPod: "rpa_SERVERSECRET"}, nil }
	w := do(t, s.Handler(), "GET", "/api/boxes", "", nil)
	if strings.Contains(w.Body.String(), "rpa_SERVERSECRET") {
		t.Errorf("server key leaked: %s", w.Body.String())
	}
}

// /api/keys tells the page where keys come from, and never what they are.
func TestKeySourcesReportsBooleansOnly(t *testing.T) {
	s, _, _ := testServer(&fake{})
	s.Secrets = func(context.Context) (Keys, error) {
		return Keys{RunPod: "rpa_SERVERSECRET", TSClientID: "id", TSClientSecret: "sec_SERVER"}, nil
	}
	w := do(t, s.Handler(), "GET", "/api/keys", "", nil)
	body := w.Body.String()
	if !strings.Contains(body, `"runpod":true`) || !strings.Contains(body, `"tailscale":true`) {
		t.Errorf("body = %s", body)
	}
	for _, v := range []string{"rpa_SERVERSECRET", "sec_SERVER"} {
		if strings.Contains(body, v) {
			t.Errorf("/api/keys leaked %s", v)
		}
	}
	s.Secrets = nil
	if body := do(t, s.Handler(), "GET", "/api/keys", "", nil).Body.String(); !strings.Contains(body, `"runpod":false`) {
		t.Errorf("no server keys: %s", body)
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }
