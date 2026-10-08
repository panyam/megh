package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/panyam/megh/internal/iterm"
	"github.com/panyam/megh/internal/lifecycle"
	"github.com/panyam/megh/internal/providers"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// awaitSSHReady polls for a box's public SSH endpoint when it isn't mapped yet
// (RunPod maps 22/tcp a little after the pod goes RUNNING). Without this, a box
// caught mid-init has no public SSH, so dialFor falls back to the tailnet name —
// useless from a control machine that isn't on the tailnet. Returns the refreshed
// pod; gives up after ~30s (a genuinely tailnet-only box never gets a port), and
// the caller then falls back to the tailnet with a clear message.
func awaitSSHReady(ctx context.Context, prov providers.Provider, pod *providers.Box) *providers.Box {
	pod = startStoppedBox(ctx, prov, pod)
	if pod.SSHReady() {
		return pod
	}
	fmt.Fprintf(os.Stderr, "megh: %s has no public SSH endpoint yet; waiting…\n", pod.DisplayName())
	for i := 0; i < 10; i++ {
		time.Sleep(3 * time.Second)
		if p, err := providers.Find(ctx, prov, pod.ID); err == nil && p.SSHReady() {
			return p
		}
	}
	return pod
}

// startStoppedBox brings back a local container that exited (Colima sleep, a
// daemon stop, etc.) without down+up. Cloud backends are no-ops here.
func startStoppedBox(ctx context.Context, prov providers.Provider, pod *providers.Box) *providers.Box {
	starter, ok := prov.(providers.StoppedBoxStarter)
	if !ok || !lifecycle.BoxStopped(pod.Status) {
		return pod
	}
	fmt.Fprintf(os.Stderr, "megh: %s is %s; starting existing container…\n", pod.DisplayName(), strings.ToLower(pod.Status))
	if _, err := starter.StartStopped(ctx, pod.ID); err != nil {
		fmt.Fprintf(os.Stderr, "megh: could not start %s: %v\n", pod.DisplayName(), err)
		return pod
	}
	if p, err := providers.Find(ctx, prov, pod.ID); err == nil {
		return p
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

func dialFor(pod *providers.Box) dial {
	if pod.SSHReady() {
		return dial{host: pod.PublicIP, port: pod.SSHPort, boxKey: true}
	}
	// Tailnet path: the box's MagicDNS name is its bare (unprefixed) name, which
	// is what the entrypoint sets as TS_HOSTNAME.
	return dial{host: pod.DisplayName(), port: 0, boxKey: false}
}

func (d dial) tailnet() bool { return d.port == 0 }

// preflight explains an unreachable box BEFORE ssh does, because ssh's own
// message names the symptom and not the cause.
//
// The tailnet path is taken whenever a box has no public SSH mapped, and it
// assumes THIS machine is on the tailnet. When it is not -- a control machine
// with no Tailscale credential, which is the normal state of a box that has just
// started driving other boxes -- every command that dials fails with
// "ssh: Could not resolve hostname <box>". That reads like the box is broken or
// the name is wrong. Both are fine: the name is MagicDNS, and this machine is
// not on the network that resolves it.
//
// A DNS lookup is the whole test, and it is honest either way: if MagicDNS
// resolves, the tailnet path really is available.
func (d dial) preflight(pod *providers.Box) error {
	if !d.tailnet() {
		return nil
	}
	if _, err := net.LookupHost(d.host); err == nil {
		return nil
	}
	return fmt.Errorf(`cannot reach %s: it has no public SSH endpoint, and %q does not resolve.

That name is MagicDNS, so it only resolves for a machine ON the tailnet. Either:
  - this machine is not on the tailnet (check: megh doctor control-plane), or
  - the box has not finished joining yet (Tailscale comes up 1-2 min after RUNNING), or
  - the box was launched with expose_ssh: false and never joined (check: megh doctor ts logs %s)`,
		pod.DisplayName(), d.host, pod.DisplayName())
}

func (d dial) userHost() string { return "root@" + d.host }

// opts returns the base ssh options (host-key policy + port), with any extra
// options appended. The user@host and remote command are appended by the caller.
func (d dial) opts(extra ...string) []string {
	a := []string{"-o", "StrictHostKeyChecking=accept-new"}
	if d.loopback() {
		// A local box is 127.0.0.1 on a port docker allocates, and docker reuses
		// ports freely, so a recreated box behind a recycled port trips a host-key
		// MISMATCH and ssh refuses to connect until known_hosts is edited by hand.
		//
		// Not laziness: a loopback container's host key authenticates nothing that
		// the mount namespace has not already decided. Pinning it would mean
		// persisting /etc/ssh across rebuilds to defend a boundary that is not
		// there.
		a = []string{
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "LogLevel=ERROR",
		}
	}
	if d.port != 0 {
		a = append(a, "-p", strconv.Itoa(d.port))
	}
	return append(a, extra...)
}

// loopback reports whether the box is reached over the local interface, which
// is true exactly for a container on this machine.
func (d dial) loopback() bool {
	return d.host == "127.0.0.1" || d.host == "localhost" || d.host == "::1"
}

// keyFor returns the box key to pass to runSSH for this dial (empty on the
// tailnet path, where Tailscale SSH handles authentication).
func (d dial) keyFor(boxKey string) string {
	if d.boxKey {
		return boxKey
	}
	return ""
}

// runInteractiveSSH is runSSH for an attached shell (megh ssh / tmux attach). On
// an unexpected disconnect it waits for Enter on /dev/tty so iTerm2 (and other
// terminals set to close when the command exits) keep scrollback visible.
func runInteractiveSSH(boxKey string, fwdKeys []string, sshArgs []string) error {
	err := runSSH(boxKey, fwdKeys, sshArgs, nil)
	if err != nil {
		sshStayOpenAfterDisconnect(err)
	}
	return err
}

func itermSettings(profileOverride string) iterm.Settings {
	p := cfg.ITermProfile()
	if profileOverride != "" {
		p = profileOverride
	}
	return iterm.Settings{
		Profile:  p,
		Auto:     cfg.ITermAuto(),
		StoreDir: cfg.ITermProfilesDir(cfgSourcePath),
	}
}

func sshStayOpenAfterDisconnect(err error) {
	if err == nil {
		return
	}
	v := strings.TrimSpace(os.Getenv("MEGH_SSH_STAY_OPEN"))
	switch strings.ToLower(v) {
	case "0", "false", "no", "off":
		return
	}
	tty, openErr := os.Open("/dev/tty")
	if openErr != nil {
		return
	}
	defer tty.Close()
	if !term.IsTerminal(int(tty.Fd())) {
		return
	}
	code := 1
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	}
	fmt.Fprintf(os.Stderr, "\nmegh: ssh ended unexpectedly (exit %d).\n", code)
	fmt.Fprint(os.Stderr, "Scrollback stays in this tab. Press Enter to return to your shell.\n")
	_, _ = bufio.NewReader(tty).ReadString('\n')
}

// resolveProvider applies the SAME precedence to --provider that `up` applies:
// flag > $MEGH_PROVIDER > megh.yaml default_provider > runpod.
//
// Only `up` used to do this; every other command hardcoded its flag default to
// "runpod" and ignored the config. With one backend that was invisible. With
// two it is a trap: `default_provider: docker` would launch a local box that
// `megh ssh <name>` then could not find, reporting "no box matching" as though
// the box did not exist rather than as though it had looked in the wrong place.
// locateBox finds the box a command acts on, and the backend that holds it.
// An explicit --provider or MEGH_PROVIDER pins the lookup to that backend.
// Otherwise every backend with a credential is asked, because default_provider
// says where NEW boxes go, not where existing ones live: a box on another
// backend should not vanish because the default changed.
func locateBox(ctx context.Context, cmd *cobra.Command, flagVal string, args []string) (providers.Provider, *providers.Box, error) {
	if providerPinned(cmd) {
		prov, err := resolveProvider(cmd, flagVal)
		if err != nil {
			return nil, nil, err
		}
		box, err := providers.FindOrSole(ctx, prov, args)
		return prov, box, err
	}
	return providers.Locate(ctx, providers.All(), args)
}

// tailnetOnlyBox stands in for a box no backend could be asked about, so the
// connection goes to its MagicDNS name, which needs no provider key at all. It
// returns the host to dial: the full name (<box>.<tailnet>) when megh.yaml
// names the tailnet, because a bare name can resolve to something else (a
// local container with the same hostname). It refuses unless the name resolves
// to a tailnet address, since anything else is not the box.
func tailnetOnlyBox(name string) (*providers.Box, string, error) {
	short := providers.ShortName(name)
	host := short
	if cfg.Tailnet != "" {
		host = short + "." + cfg.Tailnet
	}
	addrs, err := net.LookupHost(host)
	onTailnet := false
	for _, a := range addrs {
		onTailnet = onTailnet || isTailnetAddr(a)
	}
	if err != nil || !onTailnet {
		return nil, "", fmt.Errorf("no backend this machine can ask knows %q, and %q does not resolve to a tailnet address (pass --provider, or check this machine is on the tailnet: megh doctor control-plane)", short, host)
	}
	fmt.Fprintf(os.Stderr, "megh: no backend this machine can ask knows %q; connecting to %s over the tailnet\n", short, host)
	return &providers.Box{Name: providers.PrefixName(short), Status: "RUNNING"}, host, nil
}

// tailnetRanges are the address blocks Tailscale assigns nodes: the CGNAT
// range for IPv4 and its ULA prefix for IPv6.
var tailnetRanges = []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("fd7a:115c:a1e0::/48")}

func isTailnetAddr(s string) bool {
	a, err := netip.ParseAddr(s)
	if err != nil {
		return false
	}
	for _, p := range tailnetRanges {
		if p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}

// providerPinned reports whether the user named a backend for this command.
func providerPinned(cmd *cobra.Command) bool {
	return cmd.Flags().Changed("provider") || os.Getenv("MEGH_PROVIDER") != ""
}

func resolveProvider(cmd *cobra.Command, flagVal string) (providers.Provider, error) {
	return providers.For(resolve(cmd, "provider", flagVal, "MEGH_PROVIDER", cfg.DefaultProvider, "runpod"))
}
