package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/providers/runpod"
	"github.com/spf13/cobra"
)

// surface is a known web surface on a box: its port, a short label, and the
// browser path to open.
type surface struct {
	port  int
	label string
	path  string
}

var surfaces = []surface{
	{7681, "shell", "/"},
	{7682, "webterm", "/"},
	{6080, "vnc", "/vnc.html"},
	{8080, "code", "/"},
}

func surfaceFor(port int) surface {
	for _, s := range surfaces {
		if s.port == port {
			return s
		}
	}
	return surface{port: port, label: "port", path: "/"}
}

var browseProvider string

var browseCmd = &cobra.Command{
	Use:   "browse [port] [box]",
	Short: "Tunnel a box's web surfaces to localhost and print the browser URLs",
	Long: `Open SSH port-forwards from a box's private web surfaces to your localhost,
print the URLs, and keep the tunnels open until Ctrl-C. No Tailscale needed.

  megh browse         forward every live surface (shell/vnc/code), print URLs
  megh browse 6080    forward just that port

Only surfaces actually listening on the box are shown. Ctrl-C closes the tunnels.`,
	Args: cobra.MaximumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if browseProvider != "runpod" {
			return fmt.Errorf("provider %q not implemented yet", browseProvider)
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
		var (
			pod *runpod.Pod
			err error
		)
		if boxArg != "" {
			pod, err = runpod.Find(ctx, boxArg)
		} else {
			pod, err = runpod.Sole(ctx)
		}
		if err != nil {
			return err
		}
		d := dialFor(pod)
		boxKey := d.keyFor(cfg.SSHKeyFile)

		var ports []int
		if wantPort != 0 {
			ports = []int{wantPort}
		} else {
			ports, err = liveSurfaces(boxKey, d)
			if err != nil {
				return err
			}
			if len(ports) == 0 {
				fmt.Println("no web surfaces are up on the box (try `megh enable vnc` or `megh enable code`)")
				return nil
			}
		}

		fwd := []string{"-N"}
		fmt.Fprintf(os.Stderr, "tunneling %s -> localhost (Ctrl-C to close):\n", pod.DisplayName())
		for _, p := range ports {
			s := surfaceFor(p)
			fwd = append(fwd, "-L", fmt.Sprintf("%d:localhost:%d", p, p))
			fmt.Fprintf(os.Stderr, "  %-6s http://localhost:%d%s\n", s.label, p, s.path)
		}

		sshArgs := append(d.opts(fwd...), d.userHost())
		return runSSH(boxKey, nil, sshArgs, nil)
	},
}

// liveSurfaces returns the catalog ports actually listening on the box.
func liveSurfaces(boxKey string, d dial) ([]int, error) {
	check := "for p in 7681 7682 6080 8080; do (exec 3<>/dev/tcp/127.0.0.1/$p) 2>/dev/null && echo $p; done"
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

// sshCapture runs a remote command on the box and returns its stdout.
func sshCapture(keyFile string, d dial, remote string) (string, error) {
	var args []string
	if keyFile != "" {
		args = append(args, "-i", config.ExpandPath(keyFile), "-o", "IdentitiesOnly=yes")
	}
	args = append(args, "-o", "StrictHostKeyChecking=accept-new", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10")
	if d.port != 0 {
		args = append(args, "-p", strconv.Itoa(d.port))
	}
	args = append(args, d.userHost(), remote)
	out, err := exec.Command("ssh", args...).Output()
	return string(out), err
}

func init() {
	browseCmd.Flags().StringVar(&browseProvider, "provider", "runpod", "provider (runpod)")
	rootCmd.AddCommand(browseCmd)
}
