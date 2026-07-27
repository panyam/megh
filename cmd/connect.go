package cmd

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/panyam/megh/internal/providers/runpod"
)

// awaitSSHReady polls for a box's public SSH endpoint when it isn't mapped yet
// (RunPod maps 22/tcp a little after the pod goes RUNNING). Without this, a box
// caught mid-init has no public SSH, so dialFor falls back to the tailnet name —
// useless from a control machine that isn't on the tailnet. Returns the refreshed
// pod; gives up after ~30s (a genuinely tailnet-only box never gets a port), and
// the caller then falls back to the tailnet with a clear message.
func awaitSSHReady(ctx context.Context, pod *runpod.Pod) *runpod.Pod {
	if pod.SSHReady() {
		return pod
	}
	fmt.Fprintf(os.Stderr, "megh: %s has no public SSH endpoint yet; waiting…\n", pod.DisplayName())
	for i := 0; i < 10; i++ {
		time.Sleep(3 * time.Second)
		if p, err := runpod.Find(ctx, pod.ID); err == nil && p.SSHReady() {
			return p
		}
	}
	return pod
}

// dial describes how to reach a box over SSH.
//
//   - Public path: the box has a mapped public 22/tcp; connect to its IP on that
//     port and authenticate with the profile box key.
//   - Tailnet path: no public SSH (expose_ssh: false, or not yet mapped); connect
//     to the box's MagicDNS hostname over Tailscale SSH, which authenticates by
//     tailnet identity (no box key). Requires this machine on the tailnet.
type dial struct {
	host   string
	port   int  // 0 -> default (tailnet / MagicDNS)
	boxKey bool // authenticate with the profile box key
}

func dialFor(pod *runpod.Pod) dial {
	if pod.SSHReady() {
		return dial{host: pod.PublicIP, port: pod.SSHPort, boxKey: true}
	}
	// Tailnet path: the box's MagicDNS name is its bare (unprefixed) name, which
	// is what the entrypoint sets as TS_HOSTNAME.
	return dial{host: pod.DisplayName(), port: 0, boxKey: false}
}

func (d dial) tailnet() bool { return d.port == 0 }

func (d dial) userHost() string { return "root@" + d.host }

// opts returns the base ssh options (host-key policy + port), with any extra
// options appended. The user@host and remote command are appended by the caller.
func (d dial) opts(extra ...string) []string {
	a := []string{"-o", "StrictHostKeyChecking=accept-new"}
	if d.port != 0 {
		a = append(a, "-p", strconv.Itoa(d.port))
	}
	return append(a, extra...)
}

// keyFor returns the box key to pass to runSSH for this dial (empty on the
// tailnet path, where Tailscale SSH handles authentication).
func (d dial) keyFor(boxKey string) string {
	if d.boxKey {
		return boxKey
	}
	return ""
}
