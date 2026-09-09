package runpod

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/panyam/megh/internal/providers"
)

const volumesEndpoint = "https://rest.runpod.io/v1/networkvolumes"

// wireVolume is a RunPod network volume as the API returns it: the
// per-provider, per-data-center scratch store megh boxes mount at /mnt/work.
// Multiple pods in the same data center can mount one simultaneously.
//
// It stays private and converts to providers.Volume, so RunPod's field names
// never become the shape every other backend has to return.
type wireVolume struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	DataCenter string `json:"dataCenterId"`
	Size       int    `json:"size"`
}

func (w wireVolume) box() providers.Volume {
	return providers.Volume{Provider: "runpod", ID: w.ID, Name: w.Name, DataCenter: w.DataCenter, Size: w.Size}
}

func authKey() (string, error) {
	k := os.Getenv("RUNPOD_API_KEY")
	if k == "" {
		return "", fmt.Errorf("RUNPOD_API_KEY is not set")
	}
	return k, nil
}

// Volumes lists all network volumes on the account.
func Volumes(ctx context.Context) ([]providers.Volume, error) {
	key, err := authKey()
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, volumesEndpoint, nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("runpod: HTTP %d: %s", resp.StatusCode, string(body))
	}
	var vols []wireVolume
	if err := json.Unmarshal(body, &vols); err != nil {
		return nil, fmt.Errorf("runpod: parse volumes: %w", err)
	}
	out := make([]providers.Volume, 0, len(vols))
	for _, v := range vols {
		out = append(out, v.box())
	}
	return out, nil
}

// CreateVolume creates a network volume in a data center.
func CreateVolume(ctx context.Context, name string, sizeGiB int, dc string) (*providers.Volume, error) {
	key, err := authKey()
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]any{
		"name":         name,
		"size":         sizeGiB,
		"dataCenterId": dc,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, volumesEndpoint, bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("runpod: HTTP %d: %s", resp.StatusCode, string(body))
	}
	var v wireVolume
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("runpod: parse created volume: %w", err)
	}
	out := v.box()
	return &out, nil
}

// DeleteVolume removes a network volume by id. It errors if a pod still has it
// attached (RunPod refuses the delete).
func DeleteVolume(ctx context.Context, id string) error {
	key, err := authKey()
	if err != nil {
		return err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, volumesEndpoint+"/"+id, nil)
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
