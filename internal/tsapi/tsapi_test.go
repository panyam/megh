package tsapi

import (
	"testing"
	"time"
)

func TestBareNamePrefersTheFirstLabel(t *testing.T) {
	cases := []struct {
		in   Device
		want string
	}{
		{Device{Name: "devbox-2.taild311d3.ts.net"}, "devbox-2"},
		{Device{Name: "vnclab.taild311d3.ts.net"}, "vnclab"},
		{Device{Name: "", Hostname: "fallback.example.com"}, "fallback"},
		{Device{Name: "nodots"}, "nodots"},
	}
	for _, c := range cases {
		if got := c.in.BareName(); got != c.want {
			t.Errorf("BareName(%q/%q) = %q, want %q", c.in.Name, c.in.Hostname, got, c.want)
		}
	}
}

// Clearing a box must also clear the -N debris that made its name drift, and
// must not reach a different box that merely shares a prefix.
func TestMatchName(t *testing.T) {
	cases := []struct {
		device string
		box    string
		want   bool
	}{
		{"devbox", "devbox", true},
		{"devbox-1", "devbox", true},
		{"devbox-12", "devbox", true},
		{"devbox-dev", "devbox", false},
		{"devboxy", "devbox", false},
		{"otherbox", "devbox", false},
		{"devbox", "box", false},
	}
	for _, c := range cases {
		got := MatchName(Device{Name: c.device + ".ts.net"}, c.box)
		if got != c.want {
			t.Errorf("MatchName(%q, %q) = %v, want %v", c.device, c.box, got, c.want)
		}
	}
}

// Staleness decides what the bare sweep is allowed to touch, so "I do not know"
// must never read as "safe to delete".
func TestStaleIsConservative(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		d    Device
		want bool
	}{
		{"online is never stale", Device{Online: true, LastSeen: now.Add(-72 * time.Hour)}, false},
		{"no LastSeen is never stale", Device{}, false},
		{"recently seen", Device{LastSeen: now.Add(-time.Minute)}, false},
		{"long gone", Device{LastSeen: now.Add(-2 * time.Hour)}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.d.Stale(now, 15*time.Minute); got != c.want {
				t.Errorf("Stale() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestNewRequiresAKeyAndDefaultsTheTailnet(t *testing.T) {
	t.Setenv(KeyEnv, "")
	if _, err := New("", "example.ts.net"); err == nil {
		t.Error("New with no key anywhere should fail")
	}
	c, err := New("tskey-api-x", "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.tailnet != "-" {
		t.Errorf("empty tailnet = %q, want %q", c.tailnet, "-")
	}
	t.Setenv(KeyEnv, "from-env")
	c, err = New("", "example.ts.net")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.key != "from-env" {
		t.Errorf("key = %q, want it read from %s", c.key, KeyEnv)
	}
}
