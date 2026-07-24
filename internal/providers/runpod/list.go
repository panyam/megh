package runpod

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Pod is a summarized provisioned pod, enough for `megh list` and `megh ssh`.
type Pod struct {
	ID         string
	Name       string
	Status     string
	DataCenter string
	CostPerHr  float64
	PublicIP   string
	SSHPort    int // public port mapped to container port 22 (0 until initialized)
}

// SSHReady reports whether the pod has a resolvable public SSH endpoint yet.
func (p Pod) SSHReady() bool { return p.PublicIP != "" && p.SSHPort != 0 }

// List returns all provisioned pods on the account.
func List(ctx context.Context) ([]Pod, error) {
	apiKey := os.Getenv("RUNPOD_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("RUNPOD_API_KEY is not set")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("runpod: HTTP %d: %s", resp.StatusCode, string(body))
	}
	var raw []struct {
		ID            string         `json:"id"`
		Name          string         `json:"name"`
		DesiredStatus string         `json:"desiredStatus"`
		CostPerHr     float64        `json:"costPerHr"`
		PublicIP      string         `json:"publicIp"`
		PortMappings  map[string]int `json:"portMappings"`
		Machine       struct {
			DataCenterID string `json:"dataCenterId"`
		} `json:"machine"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("runpod: parse pods list: %w", err)
	}
	pods := make([]Pod, 0, len(raw))
	for _, p := range raw {
		pods = append(pods, Pod{
			ID:         p.ID,
			Name:       p.Name,
			Status:     p.DesiredStatus,
			DataCenter: p.Machine.DataCenterID,
			CostPerHr:  p.CostPerHr,
			PublicIP:   p.PublicIP,
			SSHPort:    p.PortMappings["22"],
		})
	}
	return pods, nil
}

// ManagedPods keeps only megh-managed pods (name-prefixed). RunPod has no pod
// labels, so the name prefix is how megh tells its boxes apart from anything
// else on the same account.
func ManagedPods(pods []Pod) []Pod {
	out := make([]Pod, 0, len(pods))
	for _, p := range pods {
		if strings.HasPrefix(p.Name, NamePrefix) {
			out = append(out, p)
		}
	}
	return out
}

// Find resolves a megh-managed pod by exact id or name. Errors on no match or
// ambiguity.
func Find(ctx context.Context, idOrName string) (*Pod, error) {
	all, err := List(ctx)
	if err != nil {
		return nil, err
	}
	pods := ManagedPods(all)
	var matches []Pod
	for _, p := range pods {
		if p.ID == idOrName || p.Name == idOrName {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no box matching %q (try `megh list`)", idOrName)
	case 1:
		return &matches[0], nil
	default:
		ids := make([]string, len(matches))
		for i, m := range matches {
			ids[i] = m.ID
		}
		return nil, fmt.Errorf("%q is ambiguous across %s; pass an id", idOrName, strings.Join(ids, ", "))
	}
}

// Sole returns the only megh-managed pod when exactly one exists.
func Sole(ctx context.Context) (*Pod, error) {
	all, err := List(ctx)
	if err != nil {
		return nil, err
	}
	pods := ManagedPods(all)
	switch len(pods) {
	case 0:
		return nil, fmt.Errorf("no boxes (run `megh up` first)")
	case 1:
		return &pods[0], nil
	default:
		return nil, fmt.Errorf("%d boxes; name one: `megh ssh <name>`", len(pods))
	}
}
