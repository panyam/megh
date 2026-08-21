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
		{Device{Name: "devbox-2.example.ts.net"}, "devbox-2"},
		{Device{Name: "vnclab.example.ts.net"}, "vnclab"},
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

// isolateCreds clears every credential env var. Without this a developer's real
// environment leaks into the test, which both makes results depend on the
// machine and prints a live secret into the failure output.
func isolateCreds(t *testing.T) {
	t.Helper()
	for _, v := range []string{KeyEnv, ClientIDEnv, ClientSecretEnv} {
		t.Setenv(v, "")
	}
}

func TestNewRequiresACredentialAndDefaultsTheTailnet(t *testing.T) {
	isolateCreds(t)
	if _, err := New("", "example.ts.net"); err == nil {
		t.Error("New with no credential anywhere should fail")
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

// The console hands out a client id and secret as separate fields, so that pair
// is the form people actually have. It must win over a leftover PAT, which is
// exactly the state a tailnet is in mid-migration.
func TestOAuthPairWinsOverAStaleAPIKey(t *testing.T) {
	isolateCreds(t)
	t.Setenv(KeyEnv, "tskey-api-stale-pat")
	t.Setenv(ClientIDEnv, "kABC")
	t.Setenv(ClientSecretEnv, "tskey-client-kABC-secret")

	c, err := New("", "example.ts.net")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.key != "tskey-client-kABC-secret" {
		t.Errorf("key = %q, want the OAuth secret", c.key)
	}
	if !c.usesOAuth() {
		t.Error("the pair must take the OAuth exchange path")
	}
	if got := c.clientID(); got != "kABC" {
		t.Errorf("clientID = %q, want the explicitly configured id", got)
	}
}

// A secret supplied through the explicit pair must be exchanged even if it does
// not carry the usual prefix, otherwise it would be sent as a bearer token and
// 401 in a way that looks like a bad credential rather than a wrong code path.
func TestExplicitPairForcesOAuthRegardlessOfPrefix(t *testing.T) {
	isolateCreds(t)
	c, err := NewWithCreds(Creds{ClientID: "kABC", ClientSecret: "no-prefix-secret"}, "example.ts.net")
	if err != nil {
		t.Fatalf("NewWithCreds: %v", err)
	}
	if !c.usesOAuth() {
		t.Error("an explicitly supplied client secret must always be exchanged")
	}
}

// --api-key is an override, so it must not be silently outranked by an OAuth
// pair sitting in the environment.
func TestExplicitKeyArgumentOverridesTheEnvironment(t *testing.T) {
	isolateCreds(t)
	t.Setenv(ClientIDEnv, "kABC")
	t.Setenv(ClientSecretEnv, "tskey-client-kABC-secret")

	c, err := New("tskey-api-explicit", "example.ts.net")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.key != "tskey-api-explicit" {
		t.Errorf("key = %q, want the explicitly passed one", c.key)
	}
	if c.usesOAuth() {
		t.Error("an explicit PAT must not take the OAuth path")
	}
}
