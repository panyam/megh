package serve

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/providers"
	"github.com/panyam/megh/internal/tsapi"
)

const runpodKey = "rpa_SECRETKEY123"

type fake struct {
	boxes   []providers.Box
	listErr error
	upOpts  *providers.Options
	killed  string
}

type result string

func (r result) Summary() string { return string(r) }

func (f *fake) Name() string         { return "runpod" }
func (f *fake) Mesh() providers.Mesh { return providers.Mesh{} }
func (f *fake) List(context.Context) ([]providers.Box, error) {
	return f.boxes, f.listErr
}
func (f *fake) Up(_ context.Context, o providers.Options) (providers.Result, error) {
	f.upOpts = &o
	return result("launched " + o.Name), nil
}
func (f *fake) Terminate(_ context.Context, id string) error { f.killed = id; return nil }
func (f *fake) Volumes(context.Context) ([]providers.Volume, error) { return nil, nil }
func (f *fake) CreateVolume(context.Context, string, int, string) (*providers.Volume, error) {
	return nil, nil
}
func (f *fake) DeleteVolume(context.Context, string) error { return nil }

// testServer returns a server whose backends are f, recording the keys each
// request carried, and the buffer its log goes to.
func testServer(f *fake) (*Server, *[]Keys, *bytes.Buffer) {
	cfg := config.Default()
	cfg.ExtraPubKeys = []string{"ssh-ed25519 cGhvbmU= bitwarden"}
	var seen []Keys
	var logs bytes.Buffer
	s := &Server{
		Config:    cfg,
		Backends:  func(k Keys) []providers.Provider { seen = append(seen, k); return []providers.Provider{f} },
		Tailscale: func(Keys) func() (*tsapi.Client, error) { return nil },
		Log:       log.New(&logs, "", 0),
	}
	return s, &seen, &logs
}

func do(t *testing.T, h http.Handler, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

var withKey = map[string]string{HeaderRunPodKey: runpodKey, "Content-Type": "application/json"}

func TestEachRequestBuildsBackendsFromItsOwnKeys(t *testing.T) {
	s, seen, _ := testServer(&fake{})
	h := s.Handler()
	do(t, h, "GET", "/api/boxes", "", map[string]string{HeaderRunPodKey: "key-a"})
	do(t, h, "GET", "/api/boxes", "", map[string]string{HeaderRunPodKey: "key-b", HeaderTSID: "id-b", HeaderTSSecret: "sec-b"})
	if len(*seen) != 2 || (*seen)[0].RunPod != "key-a" || (*seen)[1].RunPod != "key-b" || (*seen)[0].TSClientID != "" || (*seen)[1].TSClientSecret != "sec-b" {
		t.Errorf("keys per request = %+v", *seen)
	}
}

func TestNoKeyIs401AndNeverReachesABackend(t *testing.T) {
	s, seen, _ := testServer(&fake{})
	w := do(t, s.Handler(), "GET", "/api/boxes", "", nil)
	if w.Code != http.StatusUnauthorized || len(*seen) != 0 {
		t.Errorf("code=%d backends built=%d", w.Code, len(*seen))
	}
}

// A backend error that happens to echo the key must not carry it back to the
// page, and nothing the server logs may contain it either.
func TestTheKeyNeverAppearsInAResponseOrTheLog(t *testing.T) {
	f := &fake{listErr: errors.New("runpod: HTTP 401: bad key " + runpodKey)}
	s, _, logs := testServer(f)
	w := do(t, s.Handler(), "GET", "/api/boxes", "", withKey)
	if strings.Contains(w.Body.String(), runpodKey) {
		t.Errorf("response leaks the key: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "[redacted]") {
		t.Errorf("expected the echoed key to be redacted: %s", w.Body.String())
	}
	if strings.Contains(logs.String(), runpodKey) {
		t.Errorf("log leaks the key: %s", logs.String())
	}
}

func TestEveryResponseCarriesTheSecurityHeadersAndNoCORS(t *testing.T) {
	s, _, _ := testServer(&fake{})
	h := s.Handler()
	for _, c := range []struct{ method, path string }{
		{"GET", "/"}, {"GET", "/app.js"}, {"GET", "/api/boxes"}, {"GET", "/nope"},
		{"POST", "/api/up"}, {"OPTIONS", "/api/up"},
	} {
		w := do(t, h, c.method, c.path, "", nil)
		if got := w.Header().Get("Content-Security-Policy"); got != csp {
			t.Errorf("%s %s: CSP = %q", c.method, c.path, got)
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s %s: missing no-store/nosniff", c.method, c.path)
		}
		for k := range w.Header() {
			if strings.HasPrefix(k, "Access-Control-") {
				t.Errorf("%s %s: CORS header %s", c.method, c.path, k)
			}
		}
	}
}

func TestPostMustBeJSON(t *testing.T) {
	s, seen, _ := testServer(&fake{})
	w := do(t, s.Handler(), "POST", "/api/up", `{"name":"ab"}`, map[string]string{HeaderRunPodKey: runpodKey, "Content-Type": "text/plain"})
	if w.Code != http.StatusUnsupportedMediaType || len(*seen) != 0 {
		t.Errorf("code=%d", w.Code)
	}
}

func TestUpValidatesTheNameAndPassesTheFlavor(t *testing.T) {
	f := &fake{}
	s, _, _ := testServer(f)
	h := s.Handler()
	for _, bad := range []string{`{"name":"Bad_Name"}`, `{"name":""}`, `{"name":"ab","flavor":"huge"}`, `{"name":"ab","extra":1}`} {
		if w := do(t, h, "POST", "/api/up", bad, withKey); w.Code != http.StatusBadRequest {
			t.Errorf("%s: code=%d body=%s", bad, w.Code, w.Body.String())
		}
	}
	if f.upOpts != nil {
		t.Fatal("an invalid request must not launch")
	}
	w := do(t, h, "POST", "/api/up", `{"name":"ab","flavor":"full"}`, withKey)
	if w.Code != http.StatusOK || f.upOpts == nil {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if f.upOpts.Name != "megh-ab" || !strings.Contains(f.upOpts.Image, "megh-full") {
		t.Errorf("launched %q from %q", f.upOpts.Name, f.upOpts.Image)
	}
}

func TestDownNeedsANameAndAnExistingBox(t *testing.T) {
	f := &fake{boxes: []providers.Box{{ID: "p1", Name: "megh-ab", Status: "RUNNING"}}}
	s, _, _ := testServer(f)
	h := s.Handler()
	if w := do(t, h, "POST", "/api/down", `{"name":""}`, withKey); w.Code != http.StatusBadRequest {
		t.Errorf("nameless down: code=%d", w.Code)
	}
	if w := do(t, h, "POST", "/api/down", `{"name":"zz"}`, withKey); w.Code != http.StatusNotFound {
		t.Errorf("unknown box: code=%d", w.Code)
	}
	if f.killed != "" {
		t.Fatal("nothing should have been terminated yet")
	}
	if w := do(t, h, "POST", "/api/down", `{"name":"ab"}`, withKey); w.Code != http.StatusOK || f.killed != "p1" {
		t.Errorf("code=%d killed=%q", w.Code, f.killed)
	}
}

func TestBoxesListsLinksAndSSH(t *testing.T) {
	f := &fake{boxes: []providers.Box{{ID: "p1", Name: "megh-ab", Status: "RUNNING", PublicIP: "203.0.113.7", SSHPort: 41234}}}
	s, _, _ := testServer(f)
	w := do(t, s.Handler(), "GET", "/api/boxes", "", withKey)
	body := w.Body.String()
	for _, want := range []string{`"name":"ab"`, `"ssh":"ssh -p 41234 root@203.0.113.7"`, `:7682/`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
}

func TestARefusedRequestReachesNothing(t *testing.T) {
	s, seen, _ := testServer(&fake{})
	s.Authorize = func(*http.Request) (string, error) { return "", errors.New("no") }
	for _, p := range []string{"/", "/api/boxes"} {
		if w := do(t, s.Handler(), "GET", p, "", withKey); w.Code != http.StatusForbidden {
			t.Errorf("%s: code=%d", p, w.Code)
		}
	}
	if len(*seen) != 0 {
		t.Error("a refused request built backends")
	}
}
