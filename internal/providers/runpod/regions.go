package runpod

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

const openapiEndpoint = "https://rest.runpod.io/v1/openapi.json"

// ProbePrefix marks a pod created only to test capacity. It sits under
// NamePrefix so a probe that outlives its process is still discoverable as
// megh-managed (see CONSTRAINTS C1) and shows up in `megh list --all`.
const ProbePrefix = NamePrefix + "probe-"

// bakedDataCenters is the fallback list of data centers that accept a CPU pod,
// read from the pods enum in RunPod's OpenAPI spec on 2026-08-20. DataCenters
// prefers the live spec; this is what we use when that fetch or parse fails.
// Note it is where a pod may be *placed*, not where one is rentable right now.
// RunPod publishes no availability API, so only Probe answers that.
var bakedDataCenters = []string{
	"AP-IN-1", "AP-JP-1",
	"CA-MTL-1", "CA-MTL-2", "CA-MTL-3",
	"EU-CZ-1", "EU-FR-1", "EU-NL-1", "EU-RO-1", "EU-SE-1",
	"EUR-IS-1", "EUR-IS-2", "EUR-IS-3", "EUR-NO-1",
	"OC-AU-1",
	"US-CA-2", "US-DE-1", "US-GA-1", "US-GA-2", "US-IL-1", "US-KS-2", "US-KS-3",
	"US-MD-1", "US-NC-1", "US-TX-1", "US-TX-3", "US-TX-4", "US-WA-1",
}

// DataCenters returns every data center id that RunPod will accept for a CPU
// pod. There is no datacenters endpoint, but the pods request schema in the
// published OpenAPI document carries the ids as an enum, which makes it the
// authoritative list and one that tracks RunPod adding a region. Any failure
// falls back to bakedDataCenters rather than erroring, so region search still
// works offline-ish or if the spec moves.
func DataCenters(ctx context.Context) []string {
	dcs, err := fetchDataCenters(ctx)
	if err != nil || len(dcs) == 0 {
		return append([]string(nil), bakedDataCenters...)
	}
	sort.Strings(dcs)
	return dcs
}

func fetchDataCenters(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openapiEndpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("runpod: openapi HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseDataCenters(body)
}

// parseDataCenters pulls the data center enum out of the OpenAPI document.
// Only the one enum is needed, so it decodes the narrow path to it rather than
// the whole 150 KB.
func parseDataCenters(body []byte) ([]string, error) {
	var spec struct {
		Paths struct {
			Pods struct {
				Post struct {
					RequestBody struct {
						Content struct {
							JSON struct {
								Schema struct {
									Properties struct {
										DataCenterIDs struct {
											Items struct {
												Enum []string `json:"enum"`
											} `json:"items"`
										} `json:"dataCenterIds"`
									} `json:"properties"`
								} `json:"schema"`
							} `json:"application/json"`
						} `json:"content"`
					} `json:"requestBody"`
				} `json:"post"`
			} `json:"/pods"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(body, &spec); err != nil {
		return nil, err
	}
	return spec.Paths.Pods.Post.RequestBody.Content.JSON.Schema.Properties.DataCenterIDs.Items.Enum, nil
}

// USDataCenters filters to the US regions in dcs.
func USDataCenters(dcs []string) []string {
	var out []string
	for _, d := range dcs {
		if strings.HasPrefix(d, "US-") {
			out = append(out, d)
		}
	}
	return out
}

// Probe reports whether a data center will actually rent the requested CPU
// shape right now. RunPod exposes no availability API and a flavor being
// *defined* in a region does not mean it is free, so the only definitive test
// is a real rent: create the pod, then terminate it immediately. A rejected
// create leaves nothing behind, and an accepted one lives for about a second,
// which costs a fraction of a cent and never pulls the image.
//
// The probe attaches no network volume, so it answers only the CPU half of
// placement. It does test the caller's exact vCPU/RAM/disk shape, since the
// container disk cap scales with instance size and a shape can be refused on
// disk alone.
func Probe(ctx context.Context, o Options) ProbeResult {
	o.Name = probeName(o.DataCenter)
	o.VolumeID = ""
	o.ExposeSSH = false
	// A probe is terminated about a second after it is created, long before the
	// image pull finishes, so it never runs the entrypoint. If a terminate ever
	// fails, though, the survivor would boot and join the tailnet, leaving
	// exactly the kind of stale node that makes the next real box come up as
	// <name>-1. Blank the credentials so an orphan is inert: ExtraEnv is applied
	// over the base pod env, and an empty TS_AUTHKEY means "do not join".
	o.ExtraEnv = map[string]string{
		"TS_AUTHKEY":          "",
		"MEGH_SESSIONS_TOKEN": "",
		"MEGH_SESSIONS_REPO":  "",
	}

	name := ShortName(o.Name)
	res, err := Up(ctx, o)
	if err != nil {
		return ProbeResult{DC: o.DataCenter, Name: name, Err: err}
	}
	// Terminate on a fresh context: if the caller's was cancelled between the
	// create and here, we still must not leave a pod billing.
	if terr := Terminate(context.WithoutCancel(ctx), res.ID); terr != nil {
		return ProbeResult{DC: o.DataCenter, Name: name, Rentable: true, PodID: res.ID, Orphan: terr}
	}
	return ProbeResult{DC: o.DataCenter, Name: name, Rentable: true, PodID: res.ID}
}

// probeName is the pod name for a region's probe. It carries the megh- marker so
// a probe that outlives its process is still discoverable, and callers show
// users ShortName of it (CONSTRAINTS C1).
func probeName(dc string) string { return ProbePrefix + strings.ToLower(dc) }

// ProbeResult is one data center's answer.
type ProbeResult struct {
	DC       string
	Name     string // the probe box's bare name, for anything user-facing
	Rentable bool
	PodID    string // the probe pod, already terminated unless Orphan is set
	Err      error  // why the rent was refused (capacity, or a real API error)
	Orphan   error  // non-nil if the probe pod was created but could not be terminated
}

// OutOfCapacity distinguishes "this region is dry", which is the expected
// answer for most regions, from a misconfiguration worth reporting loudly.
func (p ProbeResult) OutOfCapacity() bool {
	if p.Err == nil {
		return false
	}
	m := strings.ToLower(p.Err.Error())
	for _, s := range []string{"no longer any instances", "no instances", "not enough free", "unavailable", "out of capacity", "insufficient"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

// Reason is a short human-facing verdict for one region.
func (p ProbeResult) Reason() string {
	switch {
	case p.Orphan != nil:
		return "rentable, BUT the probe pod could not be terminated: " + p.Orphan.Error()
	case p.Rentable:
		return "rentable"
	case p.OutOfCapacity():
		return "no capacity"
	default:
		return firstLine(p.Err.Error())
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}
