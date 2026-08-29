package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/panyam/megh/internal/providers/runpod"
	"github.com/spf13/cobra"
)

var sshProvider string

var sshCmd = &cobra.Command{
	Use:   "ssh [box-name-or-id]",
	Short: "Open an interactive shell on a box (git-ready via forwarded keys)",
	Long: `SSH into a box with the profile's box key, forwarding the profile's GitHub
identity keys (private keys never touch the box) and configuring per-identity
Host aliases so git works. This is a plain shell.

For browser access to the box's web surfaces, use 'megh browse' (localhost
tunnels) or Tailscale.

If the box exposes public SSH it connects over its IP; otherwise it connects to
the box's Tailscale MagicDNS name (requires this machine on the tailnet). With no
argument it connects to the only box.`,
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

		pod = awaitSSHReady(ctx, pod)
		d := dialFor(pod)
		if d.tailnet() {
			fmt.Fprintf(os.Stderr, "megh: %q has no public SSH endpoint (still initializing, or tailnet-only). "+
				"Trying its tailnet name — this needs THIS machine on the tailnet; otherwise wait and retry `megh ssh`.\n",
				pod.DisplayName())
		}

		// Set up per-identity GitHub Host aliases on the box, and forward the
		// profile's GH keys so git works in the shell.
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

		// Copy any megh.yaml `files:` (secrets/rc files not in a repo) onto the box.
		if err := pushFiles(d, d.keyFor(cfg.SSHKeyFile), cfg.Files); err != nil {
			fmt.Fprintf(os.Stderr, "megh: warning: file copy failed: %v\n", err)
		}
		// Mirror megh.yaml `sync:` dirs. Best effort like the copy above: a
		// failed sync must not stop you getting onto the box.
		if err := pushSync(d, d.keyFor(cfg.SSHKeyFile), cfg.Sync); err != nil {
			fmt.Fprintf(os.Stderr, "megh: warning: sync failed: %v\n", err)
		}

		sshArgs := append(d.opts("-A"), d.userHost())
		fmt.Fprintf(os.Stderr, "megh: ssh %s (browser access: megh browse)\n", d.userHost())
		return runSSH(d.keyFor(cfg.SSHKeyFile), fwdKeys, sshArgs, nil)
	},
}

func init() {
	sshCmd.Flags().StringVar(&sshProvider, "provider", "runpod", "provider (runpod)")
	rootCmd.AddCommand(sshCmd)
}
