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
	SSHPort    int    // public port mapped to container port 22 (0 until initialized)
	Image      string // imageName the pod was created from
}

// SSHReady reports whether the pod has a resolvable public SSH endpoint yet.
func (p Pod) SSHReady() bool { return p.PublicIP != "" && p.SSHPort != 0 }

// ShortName is a pod's display and tailnet name: the RunPod pod name minus the
// megh- discovery prefix. The prefix stays on the RunPod pod (the only durable
// marker megh has to filter its own boxes), but the user never types it or sees
// it, and it is not the Tailscale hostname. Foreign pods (shown under
// `list --all`) have no prefix, so this passes them through unchanged.
func ShortName(name string) string { return strings.TrimPrefix(name, NamePrefix) }

// DisplayName is ShortName of this pod: what the user typed at `up`, its
// Tailscale MagicDNS hostname, and how megh prints it.
func (p Pod) DisplayName() string { return ShortName(p.Name) }

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
		ImageName     string         `json:"imageName"`
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
			Image:      p.ImageName,
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

// Find resolves a megh-managed pod by exact id or name. The name may be given
// with or without the megh- prefix (the bare name is what the user typed), so
// `megh down tsdiag` and `megh down megh-tsdiag` both resolve. Errors on no
// match or ambiguity.
func Find(ctx context.Context, idOrName string) (*Pod, error) {
	all, err := List(ctx)
	if err != nil {
		return nil, err
	}
	pods := ManagedPods(all)
	var matches []Pod
	for _, p := range pods {
		if p.ID == idOrName || p.Name == idOrName || p.DisplayName() == idOrName {
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

// Terminate deletes a pod by id. The attached network volume and its contents
// are not affected (they persist independently of the pod).
func Terminate(ctx context.Context, id string) error {
	key := os.Getenv("RUNPOD_API_KEY")
	if key == "" {
		return fmt.Errorf("RUNPOD_API_KEY is not set")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint+"/"+id, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("runpod: HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
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
