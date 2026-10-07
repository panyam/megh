package cmd

import (
	"strings"
	"testing"
)

// `megh serve` has no sign-in, so anything but loopback must be refused before
// it listens.
func TestServeRefusesNonLoopbackAddresses(t *testing.T) {
	old := serveAddr
	t.Cleanup(func() { serveAddr = old })
	for _, addr := range []string{"0.0.0.0:8080", ":8080", "192.168.1.5:8080", "[::]:8080", "example.com:80"} {
		serveAddr = addr
		err := serveCmd.RunE(serveCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "not loopback") {
			t.Errorf("%s: got %v", addr, err)
		}
	}
}
