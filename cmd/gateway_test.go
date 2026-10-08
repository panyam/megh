package cmd

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/panyam/megh/internal/config"
)

// Every port the gateway publishes is bound to the host's 127.0.0.1: the proxy
// listens on the container's 0.0.0.0 (a docker publish forwards to eth0), and
// this is the only thing keeping it off the LAN. That is the C4 exception.
func TestGatewayPublishesOnlyToTheHostsLoopback(t *testing.T) {
	args := gatewayRunArgs("ghcr.io/acme/megh-slim:latest", "tail123.ts.net")
	publishes := 0
	for i, a := range args {
		if a == "-p" {
			publishes++
			if !strings.HasPrefix(args[i+1], "127.0.0.1:") {
				t.Errorf("publish %q is not bound to 127.0.0.1", args[i+1])
			}
		}
	}
	if publishes != 4 {
		t.Errorf("published %d ports, want the 4 surfaces (SOCKS stays inside)", publishes)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--name megh-gw", "--entrypoint sh", "megh-gw-tailscale:/var/lib/tailscale", "MEGH_GATEWAY_TAILNET=tail123.ts.net", "--tun=userspace-networking", "--socks5-server=127.0.0.1:1055", "megh gw serve"} {
		if !strings.Contains(joined, want) {
			t.Errorf("run args lack %q:\n%s", want, joined)
		}
	}
}

// The node key reaches tailscale on stdin, never on a command line or in the
// container's env, where `docker inspect` and `ps` would keep it.
func TestGatewayJoinTakesTheKeyOnStdin(t *testing.T) {
	args := gatewayJoinArgs()
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "exec -i megh-gw") || !strings.Contains(joined, `--authkey="$(cat)"`) || strings.Contains(joined, "tskey") {
		t.Errorf("join args: %s", joined)
	}
	if strings.Contains(strings.Join(gatewayRunArgs("img", "t.ts.net"), " "), "tskey") {
		t.Error("a key in the run args")
	}
}

func TestGatewayURLsNameEachBoxAtLocalhost(t *testing.T) {
	got := gatewayURLs([]string{"dev", "book"})
	for _, want := range []string{"http://dev.localhost:7682/", "http://dev.localhost:7681/", "http://book.localhost:8080/"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
}

// The gateway runs its own small image, never the dev image a local box uses.
func TestGatewayImageIsTheGatewayImageNotTheDevImage(t *testing.T) {
	saved := cfg
	t.Cleanup(func() { cfg = saved })
	cfg.Registries = []config.Registry{{Host: "ghcr.io", Namespace: "acme"}}
	cfg.Providers = map[string]config.Provider{"local": {Image: "megh-local-slim:arm64"}}
	cfg.Tailscale.GatewayImage = ""
	if got := gatewayImage(); got != "ghcr.io/acme/megh-gw:latest" {
		t.Errorf("default: got %q", got)
	}
	cfg.Tailscale.GatewayImage = "megh-local-gw:arm64"
	if got := gatewayImage(); got != "megh-local-gw:arm64" {
		t.Errorf("override: got %q", got)
	}
}

// The gateway image is Alpine (busybox sh, no bash or zsh), so everything run
// inside it must be plain sh.
func TestGatewayRunsUnderPlainSh(t *testing.T) {
	args := gatewayRunArgs("img", "tail123.ts.net")
	script := args[len(args)-1]
	if out, err := exec.Command("sh", "-n", "-c", script).CombinedOutput(); err != nil {
		t.Errorf("container script is not valid sh: %v %s", err, out)
	}
	if got := strings.Join(gatewayShellArgs(), " "); got != "exec -it megh-gw sh" {
		t.Errorf("shell: got %q", got)
	}
}
