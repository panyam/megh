// Package tsapi is a minimal Tailscale control-plane client: list the tailnet's
// devices and delete one.
//
// It is the counterpart to tsops, which acts on a box from the inside. The two
// exist for different failure modes. tsops runs `tailscale logout` over SSH,
// which is the clean path but only works while the box is reachable, and a node
// goes stale in exactly the case where it is not: the box was killed out of
// band, or SSH was already gone. Only the control plane can remove those, so
// this talks to Tailscale directly from the control machine.
//
// The API key never leaves the control machine. It is not box env, not in
// box_envs, and not copied by files: (see CONSTRAINTS.md C3, and C5 for this
// key specifically).
package tsapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	apiBase = "https://api.tailscale.com/api/v2"
	// KeyEnv holds either a personal access token or, for the single-value form,
	// an OAuth client secret. megh.yaml points at these by name, never by value.
	KeyEnv = "MEGH_TAILSCALE_API_KEY"
	// ClientIDEnv / ClientSecretEnv are the two-value OAuth form, which is what
	// the Tailscale console hands you when you generate a trust credential: it
	// shows a client id and a secret as separate fields. Supporting both shapes
	// means you can paste what the console gave you rather than reverse the id
	// out of the secret.
	ClientIDEnv     = "MEGH_TAILSCALE_CLIENT_ID"
	ClientSecretEnv = "MEGH_TAILSCALE_CLIENT_SECRET"
)

// Creds is how the control machine authenticates to Tailscale. ClientID +
// ClientSecret is the preferred form and wins when both are set; APIKey is the
// single-value fallback and may itself hold either a PAT or an OAuth secret.
type Creds struct {
	APIKey       string
	ClientID     string
	ClientSecret string
}

// CredsFromEnv reads the credential from the environment, preferring the
// explicit OAuth pair.
func CredsFromEnv() Creds {
	return Creds{
		APIKey:       os.Getenv(KeyEnv),
		ClientID:     os.Getenv(ClientIDEnv),
		ClientSecret: os.Getenv(ClientSecretEnv),
	}
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// Client talks to one tailnet. It is safe for concurrent use: the only mutable
// state is the cached OAuth access token.
type Client struct {
	key     string
	tailnet string

	// base is the API root. Overridable so tests can drive a local server.
	base string

	// OAuth only. oauthClientID is optional; clientID() derives it from the
	// secret when unset. forceOAuth marks the credential as an OAuth secret
	// regardless of its shape, set when the caller supplied the explicit pair,
	// so a secret that does not carry the usual prefix is still exchanged rather
	// than sent as a bearer token.
	oauthClientID string
	forceOAuth    bool
	mu            sync.Mutex
	token         string
	tokenExpiry   time.Time
}

// New builds a client from a single credential value, falling back to the
// environment. Kept for callers that only have one string (the --api-key flag).
func New(key, tailnet string) (*Client, error) {
	c := CredsFromEnv()
	if key != "" {
		// An explicit value overrides the environment entirely, including the
		// OAuth pair, so --api-key means what it says.
		c = Creds{APIKey: key}
	}
	return NewWithCreds(c, tailnet)
}

// NewWithCreds builds a client. An empty tailnet becomes "-", which Tailscale
// reads as "the token's default tailnet".
func NewWithCreds(cr Creds, tailnet string) (*Client, error) {
	if tailnet == "" {
		tailnet = "-"
	}
	c := &Client{tailnet: tailnet, base: apiBase}
	switch {
	case cr.ClientSecret != "":
		c.key = cr.ClientSecret
		c.oauthClientID = cr.ClientID
		c.forceOAuth = true
	case cr.APIKey != "":
		c.key = cr.APIKey
	default:
		return nil, fmt.Errorf("no Tailscale credential (set %s and %s, or %s)",
			ClientIDEnv, ClientSecretEnv, KeyEnv)
	}
	return c, nil
}

// Device is one node on the tailnet, trimmed to the fields megh reasons about.
type Device struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`     // FQDN, e.g. devbox-1.example.ts.net
	Hostname string   `json:"hostname"` // the OS hostname it reported
	OS       string   `json:"os"`
	Tags     []string `json:"tags"`
	// Tailscale does not document online as guaranteed on the list response, so
	// staleness is decided from LastSeen and Online is only ever used to rule a
	// device OUT of being stale.
	Online   bool      `json:"online"`
	LastSeen time.Time `json:"lastSeen"`
}

// BareName is the device's first DNS label, which is the name megh gave the box
// (see CONSTRAINTS.md C1: the tailnet hostname is the bare, unprefixed name).
func (d Device) BareName() string {
	n := d.Name
	if n == "" {
		n = d.Hostname
	}
	if i := strings.IndexByte(n, '.'); i >= 0 {
		n = n[:i]
	}
	return n
}

// Stale reports whether a device has been offline long enough to be debris
// rather than a box that is merely booting or briefly unreachable. Unknown is
// treated as not stale: a device with no LastSeen is never swept.
func (d Device) Stale(now time.Time, after time.Duration) bool {
	if d.Online || d.LastSeen.IsZero() {
		return false
	}
	return now.Sub(d.LastSeen) > after
}

// Devices lists the tailnet's devices.
func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	var body struct {
		Devices []Device `json:"devices"`
	}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/tailnet/%s/devices", c.base, c.tailnet), &body); err != nil {
		return nil, err
	}
	return body.Devices, nil
}

// Delete removes one device from the tailnet.
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("%s/device/%s", c.base, id), nil)
}

func (c *Client) do(ctx context.Context, method, url string, out any) error {
	return c.doJSON(ctx, method, url, nil, out)
}

// doJSON is the one request path. body is JSON-encoded when non-nil, and the
// Authorization header comes from bearer() so an OAuth secret is exchanged
// (and the resulting token reused) transparently.
func (c *Client) doJSON(ctx context.Context, method, url string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return err
	}
	tok, err := c.bearer(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("tailscale: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

// NameMatcher matches a box name and the -N variants Tailscale appends when the
// bare name is still held by an offline node. Clearing "devbox" therefore also
// clears the devbox-1 / devbox-2 debris that made the name drift in the first
// place.
func NameMatcher(box string) *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(box) + `(-\d+)?$`)
}

// MatchName reports whether a device is box, or a -N variant of it.
func MatchName(d Device, box string) bool {
	return NameMatcher(box).MatchString(d.BareName())
}
