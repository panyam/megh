// Package registry inspects OCI registries over the Docker Registry v2 HTTP API.
// It uses only the standard library so megh stays light, and it performs the
// standard bearer-token handshake so it works uniformly against GHCR now and a
// self-hosted Forgejo registry later.
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"regexp"
	"strings"
	"time"

	"github.com/panyam/megh/internal/config"
)

var client = &http.Client{Timeout: 15 * time.Second}

type tagList struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

// ListTags returns the tags for <namespace>/<image> on reg, performing the
// registry v2 bearer-token handshake if the registry challenges the request.
func ListTags(ctx context.Context, reg config.Registry, image string) ([]string, error) {
	repo := fmt.Sprintf("%s/%s", reg.Namespace, image)
	url := fmt.Sprintf("https://%s/v2/%s/tags/list", reg.Host, repo)

	body, status, hdr, err := do(ctx, url, "")
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized {
		token, terr := negotiateToken(ctx, reg, hdr.Get("Www-Authenticate"), repo)
		if terr != nil {
			return nil, terr
		}
		body, status, _, err = do(ctx, url, token)
		if err != nil {
			return nil, err
		}
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", status, snippet(body))
	}

	var tl tagList
	if err := json.Unmarshal(body, &tl); err != nil {
		return nil, fmt.Errorf("parse tags: %w", err)
	}
	return tl.Tags, nil
}

func do(ctx context.Context, url, bearer string) ([]byte, int, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, nil, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return b, resp.StatusCode, resp.Header, err
}

var challengeRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

// negotiateToken parses a Bearer challenge (realm/service/scope) and fetches a
// pull token, using basic auth with the registry's username + token env when
// credentials are configured (required for private images).
func negotiateToken(ctx context.Context, reg config.Registry, challenge, repo string) (string, error) {
	params := map[string]string{}
	for _, m := range challengeRe.FindAllStringSubmatch(challenge, -1) {
		params[m[1]] = m[2]
	}
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("no bearer realm in challenge %q", challenge)
	}
	u, err := neturl.Parse(realm)
	if err != nil {
		return "", err
	}
	q := u.Query()
	if svc := params["service"]; svc != "" {
		q.Set("service", svc)
	}
	scope := params["scope"]
	if scope == "" {
		scope = "repository:" + repo + ":pull"
	}
	q.Set("scope", scope)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	if tok := reg.Token(); tok != "" {
		req.SetBasicAuth(reg.Username, tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint HTTP %d: %s", resp.StatusCode, snippet(b))
	}
	var tr struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(b, &tr); err != nil {
		return "", err
	}
	if tr.Token != "" {
		return tr.Token, nil
	}
	return tr.AccessToken, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}
