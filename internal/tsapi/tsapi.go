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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	apiBase = "https://api.tailscale.com/api/v2"
	// KeyEnv is the canonical env var holding the Tailscale API access token.
	// megh.yaml points at it by name (tailscale.api_key_env), never by value.
	KeyEnv = "MEGH_TAILSCALE_API_KEY"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

// Client talks to one tailnet.
type Client struct {
	key     string
	tailnet string
}

// New builds a client. key falls back to $MEGH_TAILSCALE_API_KEY, and an empty
// tailnet becomes "-", which Tailscale reads as "the token's default tailnet".
func New(key, tailnet string) (*Client, error) {
	if key == "" {
		key = os.Getenv(KeyEnv)
	}
	if key == "" {
		return nil, fmt.Errorf("no Tailscale API key (set %s, or pass --api-key)", KeyEnv)
	}
	if tailnet == "" {
		tailnet = "-"
	}
	return &Client{key: key, tailnet: tailnet}, nil
}

// Device is one node on the tailnet, trimmed to the fields megh reasons about.
type Device struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`     // FQDN, e.g. devbox-1.taild311d3.ts.net
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
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/tailnet/%s/devices", apiBase, c.tailnet), &body); err != nil {
		return nil, err
	}
	return body.Devices, nil
}

// Delete removes one device from the tailnet.
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("%s/device/%s", apiBase, id), nil)
}

func (c *Client) do(ctx context.Context, method, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
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
