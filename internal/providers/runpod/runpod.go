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
	"strings"
	"time"
)

const endpoint = "https://rest.runpod.io/v1/pods"

// NamePrefix marks a pod/volume as megh-managed. RunPod has no pod tags/labels,
// so the name is the only durable identifier the CLI can filter on without a
// local state file. `megh up` enforces it; list/ssh filter to it by default.
const NamePrefix = "megh-"

// ports exposes ONLY SSH over raw TCP as a key-auth break-glass path. The web
// surfaces (ttyd, noVNC) are not exposed on RunPod's public proxy; they bind to
// localhost and are reached over Tailscale (or an SSH tunnel). The RunPod REST
// API wants an array of "<port>/<proto>" strings.
var ports = []string{"22/tcp"}

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
	ExtraEnv   map[string]string // copied into the pod env (e.g. box_envs)
}

// Result is a successful launch. Name is the Tailscale hostname the box comes up
// as (set from the requested pod name), used for the tailnet URLs.
type Result struct {
	ID   string `json:"id"`
	Name string `json:"-"`
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
	if o.DataCenter == "" {
		return nil, fmt.Errorf("data center is required (--dc or $MEGH_DC)")
	}
	// A volume is optional: without one the box is ephemeral (scratch on the
	// container disk, lost on termination). The volume is pinned to its data
	// center, so if it is set the DC must match it.
	if o.Name == "" {
		o.Name = defaultName()
	} else if !strings.HasPrefix(o.Name, NamePrefix) {
		// Enforce the marker so the box is discoverable as megh-managed.
		o.Name = NamePrefix + o.Name
	}

	// Base megh env, then copy any extra (box_envs) over it.
	podEnv := map[string]string{
		"PUBLIC_KEY":          o.PubKey,
		"WORK_MOUNT":          "/workspace",
		"ARCH_TAG":            "x86_64",
		"TS_AUTHKEY":          os.Getenv("TS_AUTHKEY"),
		"TS_HOSTNAME":         o.Name,
		"MEGH_SESSIONS_REPO":  os.Getenv("MEGH_SESSIONS_REPO"),
		"MEGH_SESSIONS_TOKEN": os.Getenv("MEGH_SESSIONS_TOKEN"),
	}
	for k, v := range o.ExtraEnv {
		podEnv[k] = v
	}

	payload := map[string]any{
		"name":              o.Name,
		"imageName":         o.Image,
		"computeType":       "CPU",
		"vcpuCount":         o.VCPU,
		"cpuFlavorIds":      cpuFlavorIDs(o.VCPU, o.RAMGiB),
		"cpuFlavorPriority": "availability",
		"containerDiskInGb": o.DiskGiB,
		"dataCenterIds":     []string{o.DataCenter},
		"ports":             ports,
		"env":               podEnv,
	}
	// Attach the network volume when provided; otherwise the box is ephemeral.
	if o.VolumeID != "" {
		payload["networkVolumeId"] = o.VolumeID
		payload["volumeMountPath"] = "/workspace"
	}
	// Attach the registry credential so the private image pulls on first boot.
	if authID := registryAuthID(ctx, apiKey, o.Image); authID != "" {
		payload["containerRegistryAuthId"] = authID
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
	res.Name = o.Name
	return &res, nil
}

// Summary is the human-facing connection info for a launched pod.
func (r *Result) Summary() string {
	return fmt.Sprintf(`pod created: %s

Access (after ~1-2 min to pull the image and bring up Tailscale):
  tailnet   : http://%[2]s:7681            web shell (tmux)
              http://%[2]s:6080/vnc.html   headed browser
  ssh       : RunPod console > Connect > TCP for ip:port, then
              ssh -A root@<ip> -p <port>   (break-glass; works even if Tailscale is down)

The web surfaces are private (Tailscale only). Nothing is exposed on the public
proxy; only SSH (key auth) is public.
`, r.ID, r.Name)
}

// cpuFlavorIDs maps a requested RAM-per-vCPU ratio to RunPod CPU flavor classes.
// RunPod ties RAM to the flavor class, not a free field: c=2GB, g=4GB, m=8GB per
// vCPU. We offer gen5 then gen3 of the chosen class and let availability decide,
// so effective RAM is vcpu*classRatio (e.g. 4 vCPU general = 16 GB).
func cpuFlavorIDs(vcpu, ramGiB int) []string {
	if vcpu <= 0 {
		vcpu = 1
	}
	cls := "g"
	switch ratio := ramGiB / vcpu; {
	case ratio <= 2:
		cls = "c"
	case ratio <= 4:
		cls = "g"
	default:
		cls = "m"
	}
	// Preferred class first, then every other flavor as a fallback. With
	// cpuFlavorPriority=availability RunPod rents the first one actually free in
	// the (volume-pinned) data center, so a capacity gap in one class does not
	// block the launch. Effective RAM follows whichever class is rented.
	out := []string{"cpu5" + cls, "cpu3" + cls}
	for _, f := range []string{"cpu5g", "cpu3g", "cpu5c", "cpu3c", "cpu5m", "cpu3m"} {
		if f != out[0] && f != out[1] {
			out = append(out, f)
		}
	}
	return out
}

// registryAuthID finds the RunPod container-registry credential whose name
// matches the image's registry host (e.g. ghcr.io), so private images pull on
// first boot. $MEGH_REGISTRY_AUTH_ID overrides the lookup. Returns "" when there
// is no host (Docker Hub short names) or no matching credential; the caller then
// omits the field and relies on a public image or account-level defaults.
func registryAuthID(ctx context.Context, apiKey, image string) string {
	if id := os.Getenv("MEGH_REGISTRY_AUTH_ID"); id != "" {
		return id
	}
	host := image
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	if !strings.Contains(host, ".") {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://rest.runpod.io/v1/containerregistryauth", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return ""
	}
	var auths []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	body, _ := io.ReadAll(resp.Body)
	if json.Unmarshal(body, &auths) != nil {
		return ""
	}
	for _, a := range auths {
		if a.Name == host {
			return a.ID
		}
	}
	return ""
}

func defaultName() string {
	name := "user"
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	return "megh-" + name + "-box"
}
