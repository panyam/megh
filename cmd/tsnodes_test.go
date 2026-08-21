package cmd

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/panyam/megh/internal/tsapi"
)

func dev(name string, lastSeen time.Time, tags ...string) tsapi.Device {
	return tsapi.Device{ID: "id-" + name, Name: name + ".example.ts.net", OS: "linux", LastSeen: lastSeen, Tags: tags}
}

func names(ds []tsapi.Device) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.BareName()
	}
	return out
}

// The one rule that must never break: a node whose box is still running is not
// debris, however stale it looks or however the name matches.
func TestGCNeverTouchesALiveBox(t *testing.T) {
	now := time.Now()
	long := now.Add(-24 * time.Hour)
	devices := []tsapi.Device{
		dev("devbox", long),
		dev("devbox-1", long),
		dev("devbox-2", long),
	}
	live := map[string]bool{"devbox-1": true}

	got := gcCandidates(devices, []string{"devbox"}, live, now, 15*time.Minute, "")
	want := []string{"devbox", "devbox-2"}
	if !slices.Equal(names(got), want) {
		t.Errorf("named gc = %v, want %v", names(got), want)
	}

	got = gcCandidates(devices, nil, live, now, 15*time.Minute, "")
	if !slices.Equal(names(got), want) {
		t.Errorf("sweep = %v, want %v", names(got), want)
	}
}

// Naming a box is the precise form: it must not sweep up unrelated nodes even
// when they are far staler than the ones asked for.
func TestGCByNameIgnoresEverythingElse(t *testing.T) {
	now := time.Now()
	devices := []tsapi.Device{
		dev("devbox", now.Add(-time.Hour)),
		dev("devbox-1", now.Add(-time.Hour)),
		dev("laptop", now.Add(-90*24*time.Hour)),
		dev("phone", now.Add(-90*24*time.Hour)),
	}
	got := gcCandidates(devices, []string{"devbox"}, nil, now, 15*time.Minute, "")
	if want := []string{"devbox", "devbox-1"}; !slices.Equal(names(got), want) {
		t.Errorf("got %v, want %v", names(got), want)
	}
}

// The bare sweep cannot prove a node was ever a megh box, so it must at least
// leave anything recently seen alone.
func TestGCSweepOnlyTakesStaleNodes(t *testing.T) {
	now := time.Now()
	devices := []tsapi.Device{
		dev("gone", now.Add(-time.Hour)),
		dev("laptop", now.Add(-time.Minute)),
		{ID: "id-online", Name: "phone.ts.net", Online: true, LastSeen: now.Add(-72 * time.Hour)},
		{ID: "id-unknown", Name: "mystery.ts.net"}, // no LastSeen at all
	}
	got := gcCandidates(devices, nil, nil, now, 15*time.Minute, "")
	if want := []string{"gone"}; !slices.Equal(names(got), want) {
		t.Errorf("got %v, want %v", names(got), want)
	}
}

func TestGCTagNarrowsTheSweep(t *testing.T) {
	now := time.Now()
	long := now.Add(-24 * time.Hour)
	devices := []tsapi.Device{
		dev("boxa", long, "tag:megh"),
		dev("boxb", long),
	}
	got := gcCandidates(devices, nil, nil, now, 15*time.Minute, "tag:megh")
	if want := []string{"boxa"}; !slices.Equal(names(got), want) {
		t.Errorf("got %v, want %v", names(got), want)
	}
}

func TestUnitsReadCoarsely(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second: "30s",
		5 * time.Minute:  "5m",
		3 * time.Hour:    "3h",
		50 * time.Hour:   "2d",
	}
	for d, want := range cases {
		if got := units(d); got != want {
			t.Errorf("units(%v) = %q, want %q", d, got, want)
		}
	}
}

// CONSTRAINTS.md C5. The MEGH_ prefix is an allowlist for a feature's own
// knobs, and this key is not one: in a box's hands it can delete every node on
// the tailnet, including machines megh never created. Enforced here rather than
// left to review, because the prefix rule makes forwarding it the DEFAULT.
func TestMeghEnvNeverForwardsTheTailscaleAPIKey(t *testing.T) {
	t.Setenv("MEGH_TAILSCALE_API_KEY", "tskey-api-secret")
	t.Setenv("MEGH_HARMLESS_KNOB", "keepme")

	got := string(meghEnv())
	if strings.Contains(got, "tskey-api-secret") || strings.Contains(got, "MEGH_TAILSCALE_API_KEY") {
		t.Errorf("meghEnv forwarded the Tailscale API key to a box:\n%s", got)
	}
	if !strings.Contains(got, "MEGH_HARMLESS_KNOB") {
		t.Errorf("meghEnv dropped an ordinary MEGH_ knob:\n%s", got)
	}
}
