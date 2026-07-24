// Package runpod is the megh RunPod backend. It talks to the RunPod REST API
// directly (POST /v1/pods) because RunPod's Terraform provider is currently too
// flaky to depend on. The provider abstraction is the megh CLI, not this file;
// a second provider is a sibling package.
package runpod

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"time"
)

const endpoint = "https://rest.runpod.io/v1/pods"

// Ports exposes SSH over raw TCP and the two web surfaces over the HTTP proxy.
const ports = "22/tcp,7681/http,6080/http"

// Options configures a RunPod CPU pod launch.
type Options struct {
	Name       string
	VCPU       int
	RAMGiB     int
	DiskGiB    int
	Image      string
	VolumeID   string
	DataCenter string
	PubKey     string
}

// Result is a successful launch.
type Result struct {
	ID string `json:"id"`
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// Up creates a CPU pod from a megh image, attaches the network volume, and
// exposes the web shell / noVNC / SSH ports.
func Up(ctx context.Context, o Options) (*Result, error) {
	apiKey := os.Getenv("RUNPOD_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("RUNPOD_API_KEY is not set")
	}
	if o.Image == "" {
		return nil, fmt.Errorf("image is required (--image or $MEGH_IMAGE)")
	}
	if o.VolumeID == "" || o.DataCenter == "" {
		return nil, fmt.Errorf("volume id and data center are required (--volume/--dc or $MEGH_VOLUME_ID/$MEGH_DC)")
	}
	if o.Name == "" {
		o.Name = defaultName()
	}

	payload := map[string]any{
		"name":              o.Name,
		"imageName":         o.Image,
		"cpuCount":          o.VCPU,
		"memoryInGb":        o.RAMGiB,
		"containerDiskInGb": o.DiskGiB,
		"networkVolumeId":   o.VolumeID,
		"dataCenterId":      o.DataCenter,
		"ports":             ports,
		"env": []map[string]string{
			{"key": "PUBLIC_KEY", "value": o.PubKey},
			{"key": "WORK_MOUNT", "value": "/workspace"},
			{"key": "ARCH_TAG", "value": "x86_64"},
		},
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
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

	// Tolerate id at the top level or nested under "pod" across API versions.
	var res Result
	if err := json.Unmarshal(body, &res); err != nil || res.ID == "" {
		var alt struct {
			Pod Result `json:"pod"`
		}
		if json.Unmarshal(body, &alt) == nil {
			res = alt.Pod
		}
	}
	if res.ID == "" {
		return nil, fmt.Errorf("runpod: could not parse pod id from response: %s", string(body))
	}
	return &res, nil
}

// Summary is the human-facing connection info for a launched pod.
func (r *Result) Summary() string {
	return fmt.Sprintf(`pod created: %s

  web shell : https://%[1]s-7681.proxy.runpod.net
  headed vnc: https://%[1]s-6080.proxy.runpod.net/vnc.html
  ssh       : RunPod console > Connect > TCP for ip:port, then
              ssh -A root@<ip> -p <port>

Give it a minute to pull the image and start services.
`, r.ID)
}

func defaultName() string {
	name := "user"
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	return "megh-" + name + "-box"
}
