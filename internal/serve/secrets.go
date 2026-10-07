package serve

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Endpoints for Secret Manager on App Engine. Both are overridable for tests.
const (
	metadataTokenURL  = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"
	secretManagerBase = "https://secretmanager.googleapis.com/v1"
	secretTTL         = 10 * time.Minute
)

// GCPSecret reads the control-plane keys from one Secret Manager secret whose
// latest version holds a note in the same shape as the one pasted into the page
// (see ParseKeyBlock). It talks to the REST API with the instance's own service
// account token from the metadata server, so it needs no Google client library
// and no credential of its own: access is whatever IAM grants that account on
// this one secret.
//
// Values are cached for secretTTL, which keeps reads around 4,400 a month and
// inside the free tier while still picking up a rotated key within minutes.
type GCPSecret struct {
	// Name is the secret's resource name, or a short name resolved in Project.
	Name    string
	Project string
	// RegistryEnv is the note's name for the registry pull token; see
	// ParseKeyBlock. Empty means DefaultRegistryEnv.
	RegistryEnv string

	HTTPClient *http.Client
	TokenURL   string // default metadataTokenURL
	APIBase    string // default secretManagerBase

	mu      sync.Mutex
	cached  Keys
	fetched time.Time
	lastErr error
}

// resource is the secret's full name, accepting either form in megh.yaml.
func (g *GCPSecret) resource() string {
	if strings.HasPrefix(g.Name, "projects/") {
		return g.Name
	}
	return fmt.Sprintf("projects/%s/secrets/%s", g.Project, g.Name)
}

// Keys returns the keys the secret holds, cached for secretTTL. A secret with
// no versions, or that does not exist, holds nothing: empty Keys and no error,
// and the page falls back to the browser's keys. Any other failure (no access,
// metadata server down) also yields empty Keys, with the error, so a broken
// secret degrades to the browser path rather than locking you out.
func (g *GCPSecret) Keys(ctx context.Context) (Keys, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.fetched.IsZero() && time.Since(g.fetched) < secretTTL {
		return g.cached, g.lastErr
	}
	k, err := g.fetch(ctx)
	g.cached, g.lastErr, g.fetched = k, err, time.Now()
	return k, err
}

func (g *GCPSecret) client() *http.Client {
	if g.HTTPClient != nil {
		return g.HTTPClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (g *GCPSecret) fetch(ctx context.Context) (Keys, error) {
	tok, err := g.token(ctx)
	if err != nil {
		return Keys{}, fmt.Errorf("secret %s: token: %w", g.Name, err)
	}
	base := g.APIBase
	if base == "" {
		base = secretManagerBase
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/"+g.resource()+"/versions/latest:access", nil)
	if err != nil {
		return Keys{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := g.client().Do(req)
	if err != nil {
		return Keys{}, fmt.Errorf("secret %s: %w", g.Name, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 128<<10))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return Keys{}, nil
	case resp.StatusCode != http.StatusOK:
		// The body is Google's error message, never the payload, so it is safe
		// to surface; it names the missing permission when that is the cause.
		return Keys{}, fmt.Errorf("secret %s: HTTP %d: %s", g.Name, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Payload struct {
			Data string `json:"data"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Keys{}, fmt.Errorf("secret %s: decode: %w", g.Name, err)
	}
	raw, err := base64.StdEncoding.DecodeString(out.Payload.Data)
	if err != nil {
		return Keys{}, fmt.Errorf("secret %s: payload: %w", g.Name, err)
	}
	return ParseKeyBlock(string(raw), g.RegistryEnv), nil
}

func (g *GCPSecret) token(ctx context.Context) (string, error) {
	url := g.TokenURL
	if url == "" {
		url = metadataTokenURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := g.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metadata server: HTTP %d", resp.StatusCode)
	}
	var t struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", err
	}
	if t.AccessToken == "" {
		return "", fmt.Errorf("metadata server: empty token")
	}
	return t.AccessToken, nil
}
