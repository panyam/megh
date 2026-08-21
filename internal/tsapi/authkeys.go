package tsapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Tailscale takes two kinds of credential on the same header. A personal access
// token (tskey-api-...) is a bearer token as-is. An OAuth client secret
// (tskey-client-...) is not: it must first be exchanged for a short-lived
// access token. megh accepts either, and tells them apart by prefix, so one
// config pointer covers both and switching is a change of value rather than a
// change of setup.
const (
	oauthSecretPrefix = "tskey-client-"
	// Refresh a little before the token actually dies, so a long command cannot
	// have one expire mid-flight.
	tokenSkew = 60 * time.Second
)

// usesOAuth reports whether the configured credential is an OAuth client secret
// rather than a directly usable API token.
func (c *Client) usesOAuth() bool { return strings.HasPrefix(c.key, oauthSecretPrefix) }

// bearer returns the token to put on the Authorization header, exchanging the
// OAuth secret and caching the result when needed.
func (c *Client) bearer(ctx context.Context) (string, error) {
	if !c.usesOAuth() {
		return c.key, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenExpiry.Add(-tokenSkew)) {
		return c.token, nil
	}
	tok, ttl, err := c.exchange(ctx)
	if err != nil {
		return "", err
	}
	c.token, c.tokenExpiry = tok, time.Now().Add(ttl)
	return tok, nil
}

// exchange performs the client-credentials grant.
func (c *Client) exchange(ctx context.Context) (string, time.Duration, error) {
	form := url.Values{
		"client_id":     {c.clientID()},
		"client_secret": {c.key},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", 0, fmt.Errorf("tailscale oauth: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", 0, fmt.Errorf("tailscale oauth: parse token: %w", err)
	}
	if out.AccessToken == "" {
		return "", 0, fmt.Errorf("tailscale oauth: no access_token in response")
	}
	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour // Tailscale's documented default; never cache forever.
	}
	return out.AccessToken, ttl, nil
}

// clientID is the OAuth client id. Tailscale embeds it in the secret itself
// (tskey-client-<id>-<random>), so a user only has to configure the secret,
// but an explicitly configured id wins if one is set.
func (c *Client) clientID() string {
	if c.oauthClientID != "" {
		return c.oauthClientID
	}
	rest := strings.TrimPrefix(c.key, oauthSecretPrefix)
	if i := strings.IndexByte(rest, '-'); i > 0 {
		return rest[:i]
	}
	return rest
}

// AuthKeyOptions describes the node auth key to mint for one box.
type AuthKeyOptions struct {
	Box    string        // box name, for the key's description
	Tags   []string      // required when minting through an OAuth client
	Expiry time.Duration // how long the key stays usable (not the node's lifetime)
}

// MintAuthKey creates a single-use, ephemeral, pre-authorized node auth key for
// one box, and returns it. The three properties matter for different reasons:
//
//   - single-use, so a key that leaks out of a pod's env cannot enrol anything
//     after the box it was made for has consumed it;
//   - ephemeral, so Tailscale removes the node by itself once the box is gone,
//     which is the root fix for names drifting to <name>-1 (a persistent key
//     leaves a node record behind even after a clean `tailscale logout`);
//   - pre-authorized, so a box joins without waiting on manual device approval.
//
// The expiry bounds how long the key can be redeemed, not how long the box
// lives. Minutes is plenty: the entrypoint runs `tailscale up` at boot.
func (c *Client) MintAuthKey(ctx context.Context, o AuthKeyOptions) (string, error) {
	if o.Expiry <= 0 {
		o.Expiry = 10 * time.Minute
	}
	desc := "megh box"
	if o.Box != "" {
		desc += " " + sanitizeDescription(o.Box)
	}
	create := map[string]any{
		"reusable":      false,
		"ephemeral":     true,
		"preauthorized": true,
	}
	// Only send tags when we have them: an empty array is not the same as
	// absent, and a PAT-minted key is allowed to be untagged.
	if len(o.Tags) > 0 {
		create["tags"] = o.Tags
	}
	payload := map[string]any{
		"capabilities":  map[string]any{"devices": map[string]any{"create": create}},
		"expirySeconds": int(o.Expiry.Seconds()),
		"description":   desc,
	}
	var out struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("%s/tailnet/%s/keys", c.base, c.tailnet), payload, &out); err != nil {
		return "", err
	}
	if out.Key == "" {
		return "", fmt.Errorf("tailscale: key created but no secret returned")
	}
	return out.Key, nil
}

// sanitizeDescription strips anything Tailscale refuses in a key description.
// Measured against the live API: letters, digits, spaces and hyphens are fine,
// while punctuation such as parentheses is rejected with
// "keys: description had invalid characters". Box names are user-supplied, so
// an odd one would otherwise fail the mint and silently drop the box back to
// the shared static key.
func sanitizeDescription(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == ' ', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}
