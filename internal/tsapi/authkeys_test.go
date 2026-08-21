package tsapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, key, base string) *Client {
	t.Helper()
	c, err := New(key, "example.ts.net")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.base = base
	return c
}

// The three properties of a minted key are the whole point of minting, so pin
// the wire format: single-use so a leaked pod env is worthless, ephemeral so
// Tailscale reaps the node by itself, pre-authorized so boot does not stall on
// manual approval.
func TestMintAuthKeyRequestShape(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tailnet/example.ts.net/keys" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q", r.Method)
		}
		if a := r.Header.Get("Authorization"); a != "Bearer tskey-api-plain" {
			t.Errorf("Authorization = %q, want the PAT used directly", a)
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		w.Write([]byte(`{"id":"k1","key":"tskey-auth-minted"}`))
	}))
	defer srv.Close()

	c := testClient(t, "tskey-api-plain", srv.URL)
	key, err := c.MintAuthKey(context.Background(), AuthKeyOptions{
		Box: "devbox", Tags: []string{"tag:megh"}, Expiry: 15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("MintAuthKey: %v", err)
	}
	if key != "tskey-auth-minted" {
		t.Errorf("key = %q", key)
	}

	create := got["capabilities"].(map[string]any)["devices"].(map[string]any)["create"].(map[string]any)
	if create["reusable"] != false {
		t.Error("key must be single-use: a leaked pod env must not enrol more machines")
	}
	if create["ephemeral"] != true {
		t.Error("key must be ephemeral: this is the root fix for nodes outliving their box")
	}
	if create["preauthorized"] != true {
		t.Error("key must be pre-authorized so boot does not wait on device approval")
	}
	tags := create["tags"].([]any)
	if len(tags) != 1 || tags[0] != "tag:megh" {
		t.Errorf("tags = %v", tags)
	}
	if got["expirySeconds"].(float64) != 900 {
		t.Errorf("expirySeconds = %v, want 900", got["expirySeconds"])
	}
	if d, _ := got["description"].(string); !strings.Contains(d, "devbox") {
		t.Errorf("description = %q, should name the box", d)
	}
}

// An untagged key is legal when minting with a PAT, and an empty tags array is
// not the same as omitting the field, so it must be absent rather than [].
func TestMintAuthKeyOmitsTagsWhenThereAreNone(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		w.Write([]byte(`{"id":"k1","key":"tskey-auth-x"}`))
	}))
	defer srv.Close()

	c := testClient(t, "tskey-api-plain", srv.URL)
	if _, err := c.MintAuthKey(context.Background(), AuthKeyOptions{Box: "b"}); err != nil {
		t.Fatalf("MintAuthKey: %v", err)
	}
	create := got["capabilities"].(map[string]any)["devices"].(map[string]any)["create"].(map[string]any)
	if _, present := create["tags"]; present {
		t.Errorf("tags should be absent, not empty: got %v", create["tags"])
	}
	// Default expiry applies when the caller does not choose one.
	if got["expirySeconds"].(float64) != 600 {
		t.Errorf("default expirySeconds = %v, want 600", got["expirySeconds"])
	}
}

// A PAT is a bearer token as-is; only an OAuth client secret needs exchanging.
// Getting this wrong would send the secret straight to the API and 401.
func TestOAuthExchangeHappensOnceAndIsCached(t *testing.T) {
	var exchanges, calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			atomic.AddInt32(&exchanges, 1)
			r.ParseForm()
			if id := r.PostFormValue("client_id"); id != "abc123" {
				t.Errorf("client_id = %q, want it derived from the secret", id)
			}
			if sec := r.PostFormValue("client_secret"); sec != "tskey-client-abc123-xyz" {
				t.Errorf("client_secret = %q", sec)
			}
			w.Write([]byte(`{"access_token":"tskey-api-exchanged","expires_in":3600}`))
			return
		}
		atomic.AddInt32(&calls, 1)
		if a := r.Header.Get("Authorization"); a != "Bearer tskey-api-exchanged" {
			t.Errorf("Authorization = %q, want the exchanged token", a)
		}
		w.Write([]byte(`{"devices":[]}`))
	}))
	defer srv.Close()

	c := testClient(t, "tskey-client-abc123-xyz", srv.URL)
	for range 3 {
		if _, err := c.Devices(context.Background()); err != nil {
			t.Fatalf("Devices: %v", err)
		}
	}
	if calls != 3 {
		t.Errorf("api calls = %d, want 3", calls)
	}
	if exchanges != 1 {
		t.Errorf("token exchanges = %d, want 1 (the token must be cached)", exchanges)
	}
}

func TestClientIDDerivation(t *testing.T) {
	cases := map[string]string{
		"tskey-client-abc123-secretpart": "abc123",
		"tskey-client-onlyid":            "onlyid",
	}
	for secret, want := range cases {
		c := testClient(t, secret, "http://x")
		if got := c.clientID(); got != want {
			t.Errorf("clientID(%q) = %q, want %q", secret, got, want)
		}
	}
	c := testClient(t, "tskey-client-abc123-secret", "http://x")
	c.oauthClientID = "explicit"
	if got := c.clientID(); got != "explicit" {
		t.Errorf("an explicitly configured id must win, got %q", got)
	}
}

func TestUsesOAuthOnlyForClientSecrets(t *testing.T) {
	if testClient(t, "tskey-api-plain", "http://x").usesOAuth() {
		t.Error("a PAT must not be treated as an OAuth secret")
	}
	if !testClient(t, "tskey-client-a-b", "http://x").usesOAuth() {
		t.Error("a client secret must be exchanged")
	}
}

// An expired cached token must be re-exchanged rather than sent as-is.
func TestExpiredTokenIsRefreshed(t *testing.T) {
	var exchanges int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			atomic.AddInt32(&exchanges, 1)
			w.Write([]byte(`{"access_token":"tskey-api-fresh","expires_in":3600}`))
			return
		}
		w.Write([]byte(`{"devices":[]}`))
	}))
	defer srv.Close()

	c := testClient(t, "tskey-client-a-b", srv.URL)
	c.token, c.tokenExpiry = "tskey-api-stale", time.Now().Add(-time.Minute)
	if _, err := c.Devices(context.Background()); err != nil {
		t.Fatalf("Devices: %v", err)
	}
	if exchanges != 1 {
		t.Errorf("exchanges = %d, want 1 (stale token must be replaced)", exchanges)
	}
}

// Tailscale rejects a description containing punctuation, and box names come
// from the user, so an odd name must not silently cost the box its minted key.
func TestSanitizeDescription(t *testing.T) {
	cases := map[string]string{
		"devbox":        "devbox",
		"dev-box-1":     "dev-box-1",
		"my box":        "my box",
		"box(1)":        "box-1-",
		"weird_name.v2": "weird-name-v2",
		"emoji🚀box":     "emoji-box", // one rune in, one hyphen out
	}
	for in, want := range cases {
		if got := sanitizeDescription(in); got != want {
			t.Errorf("sanitizeDescription(%q) = %q, want %q", in, got, want)
		}
	}
}
