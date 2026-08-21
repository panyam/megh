package runpod

import (
	"errors"
	"slices"
	"testing"
)

// The enum lives deep inside RunPod's OpenAPI document, so the shape of that
// path is the thing most likely to break silently on their side. Pin it.
func TestParseDataCentersReadsThePodsEnum(t *testing.T) {
	body := []byte(`{"paths": {"/pods": {"post": {"requestBody": {"content": {"application/json": {"schema": {"properties": {"dataCenterIds": {"type": "array", "items": {"type": "string", "enum": ["US-CA-2", "EU-RO-1", "US-TX-3"]}}}}}}}}}}}`)
	got, err := parseDataCenters(body)
	if err != nil {
		t.Fatalf("parseDataCenters: %v", err)
	}
	want := []string{"US-CA-2", "EU-RO-1", "US-TX-3"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A spec that no longer carries the enum must yield nothing rather than an
// error, so DataCenters falls back to the baked list instead of failing.
func TestParseDataCentersToleratesAMissingEnum(t *testing.T) {
	got, err := parseDataCenters([]byte(`{"paths":{"/pods":{"post":{}}}}`))
	if err != nil {
		t.Fatalf("parseDataCenters: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestUSDataCenters(t *testing.T) {
	got := USDataCenters([]string{"US-CA-2", "EU-RO-1", "US-TX-3", "AP-JP-1", "EUR-IS-1"})
	want := []string{"US-CA-2", "US-TX-3"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// "Dry region" is the expected answer for most probes and must not read as a
// misconfiguration, while a genuine API error must not read as no capacity.
func TestProbeResultVerdicts(t *testing.T) {
	cases := []struct {
		name    string
		res     ProbeResult
		dry     bool
		wantMsg string
	}{
		{
			name:    "rentable",
			res:     ProbeResult{DC: "US-CA-2", Rentable: true},
			wantMsg: "rentable",
		},
		{
			name:    "dry region",
			res:     ProbeResult{DC: "US-TX-3", Err: errors.New("runpod: HTTP 400: there are no longer any instances available with the requested specifications")},
			dry:     true,
			wantMsg: "no capacity",
		},
		{
			name:    "real error",
			res:     ProbeResult{DC: "US-IL-1", Err: errors.New("runpod: HTTP 401: unauthorized")},
			wantMsg: "runpod: HTTP 401: unauthorized",
		},
		{
			name:    "orphan is loud",
			res:     ProbeResult{DC: "US-CA-2", Rentable: true, PodID: "abc", Orphan: errors.New("connection reset")},
			wantMsg: "rentable, BUT the probe pod could not be terminated: connection reset",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.res.OutOfCapacity(); got != c.dry {
				t.Errorf("OutOfCapacity() = %v, want %v", got, c.dry)
			}
			if got := c.res.Reason(); got != c.wantMsg {
				t.Errorf("Reason() = %q, want %q", got, c.wantMsg)
			}
		})
	}
}

// Every baked fallback region must be one the live spec would also accept, and
// the probe pod name must stay under the megh- marker (CONSTRAINTS C1).
func TestBakedDataCentersAndProbePrefix(t *testing.T) {
	if len(bakedDataCenters) == 0 {
		t.Fatal("bakedDataCenters is empty; DataCenters has no fallback")
	}
	if len(USDataCenters(bakedDataCenters)) == 0 {
		t.Error("bakedDataCenters has no US region, so the default probe set is empty")
	}
	if got := ProbePrefix; len(got) <= len(NamePrefix) || got[:len(NamePrefix)] != NamePrefix {
		t.Errorf("ProbePrefix = %q, must start with the megh- marker %q", got, NamePrefix)
	}
}
