package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func providerCmd() *cobra.Command {
	c := &cobra.Command{Use: "x"}
	c.Flags().String("provider", "", "")
	return c
}

// Only an explicit choice pins a lookup to one backend; default_provider, which
// says where new boxes go, does not.
func TestOnlyAnExplicitProviderPinsTheLookup(t *testing.T) {
	t.Setenv("MEGH_PROVIDER", "")
	saved := cfg.DefaultProvider
	cfg.DefaultProvider = "runpod"
	defer func() { cfg.DefaultProvider = saved }()

	if providerPinned(providerCmd()) {
		t.Error("default_provider alone pinned the lookup")
	}
	c := providerCmd()
	c.Flags().Set("provider", "vultr")
	if !providerPinned(c) {
		t.Error("--provider did not pin the lookup")
	}
	t.Setenv("MEGH_PROVIDER", "vultr")
	if !providerPinned(providerCmd()) {
		t.Error("MEGH_PROVIDER did not pin the lookup")
	}
}

// The tailnet fallback only stands in for a box whose name resolves here;
// otherwise it says what to do rather than handing ssh a dead name.
func TestTailnetFallbackRefusesANameThatDoesNotResolve(t *testing.T) {
	saved := gatewayUp
	gatewayUp = func() bool { return false } // a running gateway on this machine would route it
	defer func() { gatewayUp = saved }()
	_, _, err := tailnetOnlyBox("no-such-box-zz9")
	if err == nil || !strings.Contains(err.Error(), "--provider") || !strings.Contains(err.Error(), "does not resolve") {
		t.Fatalf("got %v", err)
	}
	// A name that resolves to something other than a tailnet node (here the
	// machine itself, as a local box named like the remote one would) is not
	// the box: connecting there would reach the wrong machine.
	if _, _, err := tailnetOnlyBox("localhost"); err == nil {
		t.Fatal("accepted a name that resolves outside the tailnet")
	}
}

func TestTailnetAddressesAreTheCGNATAndULARanges(t *testing.T) {
	for addr, want := range map[string]bool{
		"100.107.19.115": true, "100.64.0.1": true, "fd7a:115c:a1e0::1": true,
		"172.17.0.2": false, "127.0.0.1": false, "100.128.0.1": false, "::1": false,
	} {
		if got := isTailnetAddr(addr); got != want {
			t.Errorf("%s: got %v, want %v", addr, got, want)
		}
	}
}

// "docker" is the local backend's old name: it still works for --provider and
// MEGH_PROVIDER, and means local.
func TestDockerProviderNameMeansLocal(t *testing.T) {
	t.Setenv("MEGH_PROVIDER", "")
	c := providerCmd()
	c.Flags().Set("provider", "docker")
	if got := resolveProviderName(c, "docker"); got != "local" {
		t.Errorf("--provider docker: got %q", got)
	}
	t.Setenv("MEGH_PROVIDER", "docker")
	if got := resolveProviderName(providerCmd(), ""); got != "local" {
		t.Errorf("MEGH_PROVIDER=docker: got %q", got)
	}
	t.Setenv("MEGH_PROVIDER", "vultr")
	if got := resolveProviderName(providerCmd(), ""); got != "vultr" {
		t.Errorf("other names pass through: got %q", got)
	}
}
