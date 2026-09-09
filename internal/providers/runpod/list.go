package runpod

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/panyam/megh/internal/providers"
)

// List returns all provisioned pods on the account.
func List(ctx context.Context) ([]providers.Box, error) {
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
	pods := make([]providers.Box, 0, len(raw))
	for _, p := range raw {
		pods = append(pods, providers.Box{
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
