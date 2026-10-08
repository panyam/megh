package cmd

import (
	"strings"
	"testing"
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
	for _, want := range []string{"--name megh-gw", "--entrypoint sh", "megh-gw-tailscale:/var/lib/tailscale", "MEGH_GATEWAY_TAILNET=tail123.ts.net", "--tun=userspace-networking", "--socks5-server=127.0.0.1:1055", "megh gateway serve"} {
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
