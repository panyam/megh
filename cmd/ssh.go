package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/panyam/megh/internal/providers/runpod"
	"github.com/spf13/cobra"
)

var (
	sshProvider string
	sshTunnel   bool
)

var sshCmd = &cobra.Command{
	Use:   "ssh [box-name-or-id]",
	Short: "SSH into a box, tunneling the web shell and headed-browser surfaces",
	Long: `SSH into a provisioned box with the profile's box key, forwarding the
profile's GitHub identity keys (private keys never touch the box). Per-identity
Host aliases (gh-<name>) are configured on the box so per-repo remotes use the
right key.

If the box exposes public SSH it connects over its IP; otherwise it connects to
the box's Tailscale MagicDNS name (requires this machine on the tailnet).

By default it also forwards the box's web surfaces to your localhost:
  web shell    -> http://localhost:7681
  headed vnc   -> http://localhost:6080/vnc.html

With no argument it connects to the only box; otherwise pass a name or id.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if sshProvider != "runpod" {
			return fmt.Errorf("provider %q not implemented yet", sshProvider)
		}
		ctx := context.Background()
		var (
			pod *runpod.Pod
			err error
		)
		if len(args) == 1 {
			pod, err = runpod.Find(ctx, args[0])
		} else {
			pod, err = runpod.Sole(ctx)
		}
		if err != nil {
			return err
		}

		d := dialFor(pod)
		if d.tailnet() {
			fmt.Fprintf(os.Stderr, "megh: no public SSH; connecting to %q over the tailnet (needs this machine on the tailnet)\n", pod.Name)
		}

		// Configure per-identity GitHub Host aliases on the box first.
		var fwdKeys []string
		if activeProfile != nil {
			fwdKeys = activeProfile.GHKeyFiles()
			setup, serr := ghSetupScript(activeProfile)
			if serr != nil {
				return serr
			}
			if setup != "" {
				setupArgs := append(d.opts(), d.userHost(), "bash -s")
				if err := runSSH(d.keyFor(cfg.SSHKeyFile), nil, setupArgs, strings.NewReader(setup)); err != nil {
					fmt.Fprintf(os.Stderr, "megh: warning: gh key setup failed: %v\n", err)
				}
			}
		}

		extra := []string{"-A"}
		if sshTunnel {
			extra = append(extra,
				"-L", "7681:localhost:7681", // ttyd
				"-L", "6080:localhost:6080", // noVNC
				"-L", "8080:localhost:8080", // code-server
			)
		}
		sshArgs := append(d.opts(extra...), d.userHost())

		via := "public"
		if d.tailnet() {
			via = "tailnet"
		}
		tunNote := ""
		if sshTunnel {
			tunNote = "  (+ localhost 7681 shell / 6080 vnc / 8080 code)"
		}
		fmt.Fprintf(os.Stderr, "megh: ssh %s via %s%s\n", d.userHost(), via, tunNote)
		return runSSH(d.keyFor(cfg.SSHKeyFile), fwdKeys, sshArgs, nil)
	},
}

func init() {
	sshCmd.Flags().StringVar(&sshProvider, "provider", "runpod", "provider (runpod)")
	sshCmd.Flags().BoolVar(&sshTunnel, "tunnel", true, "forward web shell (7681) and noVNC (6080) to localhost")
	rootCmd.AddCommand(sshCmd)
}
