package cmd

import (
	"strings"
	"testing"

	"github.com/panyam/megh/internal/providers"
)

// fakeRoute pins what the dial path sees: whether a name resolves to a
// tailnet address here, and whether a gateway container is running.
func fakeRoute(t *testing.T, onTailnet, gwRunning bool) {
	t.Helper()
	savedLookup, savedUp, savedTailnet := lookupHost, gatewayUp, cfg.Tailnet
	t.Cleanup(func() { lookupHost, gatewayUp, cfg.Tailnet = savedLookup, savedUp, savedTailnet })
	cfg.Tailnet = "tail123.ts.net"
	lookupHost = func(string) ([]string, error) {
		if onTailnet {
			return []string{"100.101.102.103"}, nil
		}
		return nil, &dnsErr{}
	}
	gatewayUp = func() bool { return gwRunning }
}

type dnsErr struct{}

func (*dnsErr) Error() string { return "no such host" }

func tailnetBox() *providers.Box { return &providers.Box{Name: "megh-dev", Status: "RUNNING"} }

func TestTailnetDialGoesThroughTheGatewayWhenThisMachineIsOff(t *testing.T) {
	fakeRoute(t, false, true)
	pod := tailnetBox()
	d := dialFor(pod)
	if err := d.preflight(pod); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(append(d.opts("-A"), d.userHost()), " ")
	for _, want := range []string{
		"ProxyCommand=docker exec -i megh-gw megh gw nc %h %p",
		"root@dev.tail123.ts.net", // the full name: tailscaled in the gateway resolves it
		"-A",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("ssh args %q lack %q", args, want)
		}
	}
}

func TestTailnetDialStaysDirectWhenThisMachineIsOn(t *testing.T) {
	fakeRoute(t, true, true)
	pod := tailnetBox()
	d := dialFor(pod)
	if err := d.preflight(pod); err != nil {
		t.Fatal(err)
	}
	if args := strings.Join(d.opts(), " "); strings.Contains(args, "ProxyCommand") {
		t.Errorf("on the tailnet, yet routed through the gateway: %s", args)
	}
}

// A box with public SSH still goes through a running gateway: one launched
// from another machine (meghplane) trusts none of this machine's keys, while
// Tailscale SSH on the worker authorizes the gateway node.
func TestPublicDialPrefersARunningGateway(t *testing.T) {
	fakeRoute(t, false, true)
	pod := &providers.Box{Name: "megh-dev", PublicIP: "203.0.113.9", SSHPort: 22022, Status: "RUNNING"}
	d := dialFor(pod)
	if err := d.preflight(pod); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(append(d.opts(), d.userHost()), " ")
	if !strings.Contains(args, "ProxyCommand=") || !strings.HasSuffix(args, "root@dev.tail123.ts.net") || strings.Contains(args, "22022") || d.keyFor("box.key") != "" {
		t.Errorf("not routed through the gateway as a tailnet dial: %s (key %q)", args, d.keyFor("box.key"))
	}
}

func TestPublicDialIsDirectWithNoGateway(t *testing.T) {
	fakeRoute(t, false, false)
	pod := &providers.Box{Name: "megh-dev", PublicIP: "203.0.113.9", SSHPort: 22022, Status: "RUNNING"}
	d := dialFor(pod)
	if err := d.preflight(pod); err != nil || strings.Contains(strings.Join(d.opts(), " "), "ProxyCommand") || d.host != "203.0.113.9" {
		t.Fatalf("err %v, host %s, opts %v", err, d.host, d.opts())
	}
}

// A local box is on this machine; the gateway has no business with it, and
// docker is not even asked.
func TestLocalBoxNeverConsultsTheGateway(t *testing.T) {
	fakeRoute(t, false, true)
	gatewayUp = func() bool { t.Error("asked about the gateway for a local box"); return true }
	pod := &providers.Box{Name: "megh-dev", PublicIP: "127.0.0.1", SSHPort: 2222, Status: "RUNNING"}
	d := dialFor(pod)
	if err := d.preflight(pod); err != nil || strings.Contains(strings.Join(d.opts(), " "), "ProxyCommand") {
		t.Fatalf("err %v, opts %v", err, d.opts())
	}
}

func TestUnreachableTailnetBoxSuggestsTheGateway(t *testing.T) {
	fakeRoute(t, false, false)
	pod := tailnetBox()
	d := dialFor(pod)
	if err := d.preflight(pod); err == nil || !strings.Contains(err.Error(), "megh gw up") {
		t.Fatalf("got %v", err)
	}
}

// With no provider key that knows the box, a running gateway still reaches
// it by name.
func TestTailnetFallbackUsesARunningGateway(t *testing.T) {
	fakeRoute(t, false, true)
	if _, host, err := tailnetOnlyBox("dev"); err != nil || host != "dev.tail123.ts.net" {
		t.Fatalf("got %q %v", host, err)
	}
}

func TestGatewayProxyCommandUsesThePinnedDockerContext(t *testing.T) {
	fakeRoute(t, false, true)
	if got := gatewayProxyCommand("colima"); got != "docker --context colima exec -i megh-gw megh gw nc %h %p" {
		t.Errorf("got %q", got)
	}
}
