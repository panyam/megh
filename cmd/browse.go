package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/providers"
	"github.com/spf13/cobra"
)

// probeCmd asks the box which of the given ports are listening. It wraps the
// loop in `bash -c` so it does not run under the box's login shell (zsh), which
// has no /dev/tcp, and ends in `exit 0` so the status reports "the probe ran"
// rather than "the last port was open".
//
// It dials `localhost`, not 127.0.0.1, for the same reason the -L forward does:
// a dev server that binds ::1 only (Vite on a recent Node) is invisible to a
// 127.0.0.1 probe, while bash tries every address localhost resolves to.
func probeCmd(ports []int) string {
	ps := make([]string, 0, len(ports))
	for _, p := range ports {
		ps = append(ps, strconv.Itoa(p))
	}
	return `bash -c 'for p in ` + strings.Join(ps, " ") +
		`; do (exec 3<>/dev/tcp/localhost/$p) 2>/dev/null && echo $p; done; exit 0'`
}

// notListeningMsg explains a requested port that nothing is serving, and names
// what IS up instead. The slim flavor ships no vnc at all, so asking for :6080
// there used to print a working-looking URL and then leave ssh spewing
// "channel N: open failed" — the box is fine, the surface simply is not there.
func notListeningMsg(want int, live []int, box string) string {
	s := providers.SurfaceFor(want)
	var b strings.Builder
	if s.Feature == "" && s.Label == "port" {
		fmt.Fprintf(&b, "nothing is listening on %d on %s.\n", want, box)
	} else {
		fmt.Fprintf(&b, "nothing is listening on %d (%s) on %s.\n", want, s.Label, box)
	}
	if len(live) == 0 {
		b.WriteString("  no web surfaces are up at all; is the box still booting?\n")
	} else {
		b.WriteString("  up right now:")
		for _, p := range live {
			fmt.Fprintf(&b, " %d (%s)", p, providers.SurfaceFor(p).Label)
		}
		b.WriteString("\n")
	}
	if s.Feature != "" {
		fmt.Fprintf(&b, "  add it with: megh enable %s %s", s.Feature, box)
	}
	return strings.TrimRight(b.String(), "\n")
}

var browseProvider string

var browseCmd = &cobra.Command{
	Use:   "browse [port] [box]",
	Short: "Tunnel a box's web surfaces to localhost and print the browser URLs",
	Long: `Open SSH port-forwards from a box's private web surfaces to your localhost,
print the URLs, and keep the tunnels open until Ctrl-C. No Tailscale needed.

  megh browse         forward every live surface (shell/vnc/code), print URLs
  megh browse 6080    forward just that port
  megh browse 5173    any port works, not only the built-in surfaces (a dev server)

Only ports actually listening on the box are forwarded, and nothing on the box
changes: the tunnel is opened from here, so a port started a minute ago is
reachable without restarting anything. Ctrl-C closes the tunnels.`,
	Args: cobra.MaximumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		prov, err := resolveProvider(cmd, browseProvider)
		if err != nil {
			return err
		}
		var wantPort int
		var boxArg string
		for _, a := range args {
			if p, err := strconv.Atoi(a); err == nil {
				wantPort = p
			} else {
				boxArg = a
			}
		}

		ctx := context.Background()
		var pod *providers.Box
		if boxArg != "" {
			pod, err = providers.Find(ctx, prov, boxArg)
		} else {
			pod, err = providers.Sole(ctx, prov)
		}
		if err != nil {
			return err
		}
		d := dialFor(pod)
		if err := d.preflight(pod); err != nil {
			return err
		}
		boxKey := d.keyFor(cfg.SSHKeyFile)

		// Probe in BOTH cases. Naming a port used to skip this and forward blindly,
		// which is how you get a URL for a surface that does not exist.
		live, err := liveSurfaces(boxKey, d, wantPort)
		if err != nil {
			return err
		}
		var ports []int
		if wantPort != 0 {
			if !slices.Contains(live, wantPort) {
				fmt.Println(notListeningMsg(wantPort, live, pod.DisplayName()))
				return nil
			}
			ports = []int{wantPort}
		} else {
			ports = live
			if len(ports) == 0 {
				fmt.Println("no web surfaces are up on the box (try `megh enable vnc` or `megh enable code`)")
				return nil
			}
		}

		fwd := []string{"-N"}
		fmt.Fprintf(os.Stderr, "tunneling %s -> localhost (Ctrl-C to close):\n", pod.DisplayName())
		for _, p := range ports {
			s := providers.SurfaceFor(p)
			fwd = append(fwd, "-L", fmt.Sprintf("%d:localhost:%d", p, p))
			fmt.Fprintf(os.Stderr, "  %-7s http://localhost:%d%s\n", s.Label, p, s.Path)
		}

		sshArgs := append(d.opts(fwd...), d.userHost())
		return runSSH(boxKey, nil, sshArgs, nil)
	},
}

// liveSurfaces returns the ports actually listening on the box: the catalog,
// plus extra when it is non-zero. extra is how `megh browse 5173` reaches a dev
// server the catalog has never heard of; probing only the catalog made every
// such port read as dead.
func liveSurfaces(boxKey string, d dial, extra int) ([]int, error) {
	// Run under bash EXPLICITLY. /dev/tcp is a bash feature, and the box's login
	// shell is zsh, which has no such thing — so this probe silently found
	// nothing on every box and browse reported "no web surfaces are up" while
	// ttyd was plainly listening. `megh doctor` never had the bug because it
	// pipes its script to `bash -s`.
	//
	// `exit 0` matters too: without it the loop's status is the LAST port's, so a
	// box with a live shell but no code-server on :8080 made ssh exit 1 and this
	// function discard a perfectly good answer.
	check := probeCmd(probePorts(extra))
	out, err := sshCapture(boxKey, d, check)
	if err != nil {
		return nil, err
	}
	var ports []int
	for _, f := range strings.Fields(out) {
		if p, e := strconv.Atoi(f); e == nil {
			ports = append(ports, p)
		}
	}
	return ports, nil
}

// probePorts is the catalog plus extra, without repeating a catalog port.
func probePorts(extra int) []int {
	ports := providers.SurfacePorts()
	if extra != 0 && !slices.Contains(ports, extra) {
		ports = append(ports, extra)
	}
	return ports
}

// sshCapture runs a remote command on the box and returns its stdout.
func sshCapture(keyFile string, d dial, remote string) (string, error) {
	return sshCaptureCtx(context.Background(), keyFile, d, remote)
}

// sshCaptureCtx is sshCapture bounded by a context. ssh's own ConnectTimeout
// only covers the TCP connect, so a call that stalls earlier — name resolution
// of a MagicDNS host from a machine that is not on the tailnet is the case that
// bit us — hangs indefinitely without an outer deadline. Cancelling the context
// kills the ssh process.
func sshCaptureCtx(ctx context.Context, keyFile string, d dial, remote string) (string, error) {
	return sshCaptureIn(ctx, keyFile, d, remote, nil)
}

// sshCaptureIn is sshCaptureCtx with a script on stdin, for a probe too long or
// too quote-heavy to pass as an argument (`megh mesh ls` is the caller).
func sshCaptureIn(ctx context.Context, keyFile string, d dial, remote string, stdin io.Reader) (string, error) {
	c := exec.CommandContext(ctx, "ssh", sshCaptureArgs(keyFile, d, remote)...)
	c.Stdin = stdin
	out, err := c.Output()
	return string(out), err
}

// sshCaptureArgs builds the argv for a non-interactive remote command.
//
// The options come from d.opts so a loopback box is dialled the way every other
// command dials it: docker recycles host ports, so a recreated box behind a
// recycled port trips a host-key MISMATCH, and a probe that pinned the key would
// fail where `megh ssh` succeeds.
func sshCaptureArgs(keyFile string, d dial, remote string) []string {
	var args []string
	if keyFile != "" {
		args = append(args, "-i", config.ExpandPath(keyFile), "-o", "IdentitiesOnly=yes")
	}
	args = append(args, d.opts("-o", "BatchMode=yes", "-o", "ConnectTimeout=10")...)
	return append(args, d.userHost(), remote)
}

func init() {
	browseCmd.Flags().StringVar(&browseProvider, "provider", "", "provider (default: config default_provider, else runpod)")
	rootCmd.AddCommand(browseCmd)
}
