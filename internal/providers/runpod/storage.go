package runpod

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

const volumesEndpoint = "https://rest.runpod.io/v1/networkvolumes"

// Volume is a RunPod network volume: the per-provider, per-data-center scratch
// store that megh boxes mount at /mnt/work. Multiple pods in the same data
// center can mount one volume simultaneously.
type Volume struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	DataCenter string `json:"dataCenterId"`
	Size       int    `json:"size"`
}

func authKey() (string, error) {
	k := os.Getenv("RUNPOD_API_KEY")
	if k == "" {
		return "", fmt.Errorf("RUNPOD_API_KEY is not set")
	}
	return k, nil
}

// Volumes lists all network volumes on the account.
func Volumes(ctx context.Context) ([]Volume, error) {
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
	var vols []Volume
	if err := json.Unmarshal(body, &vols); err != nil {
		return nil, fmt.Errorf("runpod: parse volumes: %w", err)
	}
	return vols, nil
}

// CreateVolume creates a network volume in a data center.
func CreateVolume(ctx context.Context, name string, sizeGiB int, dc string) (*Volume, error) {
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
	var v Volume
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("runpod: parse created volume: %w", err)
	}
	return &v, nil
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
