package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

var (
	browseProvider   string
	browseBackground bool
	browseStop       bool
)

// browseArgs is a parsed `megh browse` invocation: which box, which ports.
//
// The box comes first now, as it does in every other command that takes one.
// Parsing stays order-insensitive — a numeric argument is a port, anything else
// is the box — because the old shape was documented for months and a rejection
// would teach nothing the help does not already say.
type browseArgs struct {
	box   string
	ports []int
}

func parseBrowseArgs(args []string) browseArgs {
	var out browseArgs
	for _, a := range args {
		if p, err := strconv.Atoi(a); err == nil {
			out.ports = append(out.ports, p)
			continue
		}
		if out.box == "" {
			out.box = a
		}
	}
	return out
}

// splitRequested divides the requested ports into those actually listening and
// those not. Naming a dead port used to abandon the whole call, so one typo in
// `megh browse dev 5678 3000` cost you the tunnel to 5678 as well. With nothing
// requested, every live port is forwarded.
func splitRequested(want, live []int) (up, down []int) {
	if len(want) == 0 {
		return live, nil
	}
	for _, p := range want {
		if slices.Contains(live, p) {
			up = append(up, p)
		} else {
			down = append(down, p)
		}
	}
	return up, down
}

// tunnelSocket is where a backgrounded tunnel's ssh control socket lives. The
// socket IS the state: megh keeps no record of open tunnels, and a socket whose
// ssh is gone answers nothing, so there is no bookkeeping to fall out of date.
//
// It lives in the temp dir rather than under ~/.megh, for two measured reasons,
// both of which bite when megh runs FROM a box rather than from the Mac.
//
// ssh creates a master socket under a random name and then hard-links it into
// place. `~/.megh` is persisted onto the work mount, which on a local box is a
// bind mount from macOS, and linking there fails: "muxserver_listen: link mux
// listener ... Bad file descriptor". A temp dir is local to the machine, so the
// link succeeds.
//
// And a Unix socket path has a hard length limit around 104 bytes, which ssh's
// random suffix eats into. A deep path fails with "too long for Unix domain
// socket" and reads like a megh bug.
//
// Nothing is lost by putting it in temp: a tunnel cannot outlive a reboot, so
// neither should its socket.
func tunnelSocket(provider, box string) string {
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("megh-tunnels-%d", os.Getuid()))
	return filepath.Join(dir, provider+"-"+box+".sock")
}

// maxSocketPath is the shortest of the platform limits (macOS 104, Linux 108),
// minus room for the random suffix ssh appends while it sets the socket up.
const maxSocketPath = 90

// browseSSHArgs builds the forwarding ssh argv. With a socket it backgrounds
// itself after authenticating (-f) and masters a control connection (-M -S) so
// `--stop` has something to talk to; without one it stays in the foreground and
// Ctrl-C closes it.
func browseSSHArgs(d dial, ports []int, socket string) []string {
	fwd := []string{"-N"}
	if socket != "" {
		fwd = append(fwd, "-f", "-M", "-S", socket)
	}
	for _, p := range ports {
		fwd = append(fwd, "-L", fmt.Sprintf("%d:localhost:%d", p, p))
	}
	return append(d.opts(fwd...), d.userHost())
}

// tunnelStopArgs asks the master connection to exit, which closes every forward
// it carries.
func tunnelStopArgs(d dial, socket string) []string {
	return append(d.opts("-S", socket, "-O", "exit"), d.userHost())
}

// tunnelCheckArgs asks whether a master is still behind this socket. It answers
// "Master running (pid=N)" and exits 0, or fails, which is how a live tunnel is
// told from a leftover socket file.
func tunnelCheckArgs(d dial, socket string) []string {
	return append(d.opts("-S", socket, "-O", "check"), d.userHost())
}

var browseCmd = &cobra.Command{
	Use:   "browse [box] [port...]",
	Short: "Tunnel a box's ports to localhost and print the browser URLs",
	Long: `Open SSH port-forwards from a box's private ports to your localhost, print the
URLs, and keep them open until Ctrl-C. No mesh needed, and nothing on the box
changes: the tunnel is opened from here, so a port that started a minute ago is
reachable without restarting anything.

  megh browse dev              forward every live surface (shell/vnc/code)
  megh browse dev 5678         forward one port (any port, not just the surfaces)
  megh browse dev 5678 3000    forward several
  megh browse dev 5678 -b      background it; close with: megh browse dev --stop

With one box, the name is optional. Only ports actually listening are
forwarded, and a port that is not gets explained rather than silently tunnelled
to nothing.`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		prov, err := resolveProvider(cmd, browseProvider)
		if err != nil {
			return err
		}
		req := parseBrowseArgs(args)

		ctx := context.Background()
		var pod *providers.Box
		if req.box != "" {
			pod, err = providers.Find(ctx, prov, req.box)
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
		socket := tunnelSocket(prov.Name(), pod.DisplayName())

		if browseStop {
			if _, err := os.Stat(socket); err != nil {
				fmt.Printf("no background tunnel to %s\n", pod.DisplayName())
				return nil
			}
			if err := runSSH(boxKey, nil, tunnelStopArgs(d, socket), nil); err != nil {
				return err
			}
			fmt.Printf("closed the background tunnel to %s\n", pod.DisplayName())
			return nil
		}

		// Probe in every case. Naming a port used to skip this and forward blindly,
		// which is how you get a URL for a surface that does not exist.
		live, err := liveSurfaces(boxKey, d, req.ports...)
		if err != nil {
			return err
		}
		ports, dead := splitRequested(req.ports, live)
		for _, p := range dead {
			fmt.Println(notListeningMsg(p, live, pod.DisplayName()))
		}
		if len(ports) == 0 {
			if len(dead) == 0 {
				fmt.Println("no web surfaces are up on the box (try `megh enable vnc` or `megh enable code`)")
			}
			return nil
		}

		if browseBackground {
			if len(socket) > maxSocketPath {
				return fmt.Errorf("tunnel socket path is too long for a unix socket (%d chars): %s\nset TMPDIR to something shorter", len(socket), socket)
			}
			// A live master means the tunnel is already open, and a second one on
			// the same socket fails with "control socket already exists", which
			// reads as a megh bug. A socket whose master is gone is debris from a
			// killed ssh or a rebooted box, and blocking on it would be worse: the
			// tunnel it names cannot be closed because it does not exist.
			if _, err := os.Stat(socket); err == nil {
				if err := exec.Command("ssh", tunnelCheckArgs(d, socket)...).Run(); err == nil {
					return fmt.Errorf("a background tunnel to %s is already open; close it first: megh browse %s --stop",
						pod.DisplayName(), pod.DisplayName())
				}
				os.Remove(socket)
			}
			if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
				return err
			}
		} else {
			socket = ""
		}

		fmt.Fprintf(os.Stderr, "tunneling %s -> localhost:\n", pod.DisplayName())
		for _, p := range ports {
			s := providers.SurfaceFor(p)
			fmt.Fprintf(os.Stderr, "  %-7s http://localhost:%d%s\n", s.Label, p, s.Path)
		}
		if browseBackground {
			fmt.Fprintf(os.Stderr, "close with: megh browse %s --stop\n", pod.DisplayName())
		} else {
			fmt.Fprintln(os.Stderr, "Ctrl-C to close")
		}
		return runSSH(boxKey, nil, browseSSHArgs(d, ports, socket), nil)
	},
}

// liveSurfaces returns the ports actually listening on the box: the catalog,
// plus any extra. The extras are how `megh browse dev 5173` reaches a dev server
// the catalog has never heard of; probing only the catalog made every such port
// read as dead.
func liveSurfaces(boxKey string, d dial, extra ...int) ([]int, error) {
	// Run under bash EXPLICITLY. /dev/tcp is a bash feature, and the box's login
	// shell is zsh, which has no such thing — so this probe silently found
	// nothing on every box and browse reported "no web surfaces are up" while
	// ttyd was plainly listening. `megh doctor` never had the bug because it
	// pipes its script to `bash -s`.
	//
	// `exit 0` matters too: without it the loop's status is the LAST port's, so a
	// box with a live shell but no code-server on :8080 made ssh exit 1 and this
	// function discard a perfectly good answer.
	check := probeCmd(probePorts(extra...))
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

// probePorts is the catalog plus any extras, without repeating a catalog port.
func probePorts(extra ...int) []int {
	ports := providers.SurfacePorts()
	for _, p := range extra {
		if p != 0 && !slices.Contains(ports, p) {
			ports = append(ports, p)
		}
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
	browseCmd.Flags().BoolVarP(&browseBackground, "background", "b", false, "open the tunnel in the background and return")
	browseCmd.Flags().BoolVar(&browseStop, "stop", false, "close the background tunnel to this box")
	rootCmd.AddCommand(browseCmd)
}
