package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/panyam/megh/internal/providers/runpod"
	"github.com/spf13/cobra"
)

var (
	downProvider string
	downYes      bool
)

var downCmd = &cobra.Command{
	Use:   "down [box-name-or-id]",
	Short: "Terminate a dev box (the network volume and /mnt/work survive)",
	Long: `Terminate a megh box to stop paying for it. RunPod bills a powered-off pod at
the full rate, so deletion is how you stop the meter.

The network volume is untouched: /mnt/work (repos, worktrees, state, caches)
persists and the next box mounts it. Push code to git and let the session flush
run if you want cross-provider durability; the volume copy survives regardless.

With no argument it terminates the only box; otherwise pass a name or id.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if downProvider != "runpod" {
			return fmt.Errorf("provider %q not implemented yet", downProvider)
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
		if !downYes {
			fmt.Printf("terminate %s (%s, %s)? the volume survives. [y/N]: ",
				pod.Name, pod.ID, pod.DataCenter)
			var resp string
			fmt.Scanln(&resp)
			if !strings.EqualFold(strings.TrimSpace(resp), "y") {
				fmt.Println("aborted")
				return nil
			}
		}
		// Best-effort: have the box deregister itself from the tailnet before we
		// terminate it — the node-side opposite of the entrypoint's `tailscale up`,
		// so an ephemeral node is removed immediately instead of lingering until GC
		// (and a persistent one is deauthenticated). Runs over the SSH access megh
		// already has, so no Tailscale API credential is needed. Never blocks
		// termination: an unreachable or tailnet-only box just skips it.
		d := dialFor(pod)
		logout := "timeout 15 tailscale --socket=/var/run/tailscale/tailscaled.sock logout >/dev/null 2>&1 || true"
		if _, err := sshCapture(d.keyFor(cfg.SSHKeyFile), d, logout); err != nil {
			fmt.Printf("note: could not reach %s to leave the tailnet (terminating anyway)\n", pod.Name)
		} else {
			fmt.Printf("asked %s to leave the tailnet\n", pod.Name)
		}

		if err := runpod.Terminate(ctx, pod.ID); err != nil {
			return err
		}
		fmt.Printf("terminated %s (%s)\n", pod.Name, pod.ID)
		return nil
	},
}

func init() {
	downCmd.Flags().StringVar(&downProvider, "provider", "runpod", "provider (runpod)")
	downCmd.Flags().BoolVarP(&downYes, "yes", "y", false, "skip the confirmation prompt")
	rootCmd.AddCommand(downCmd)
}
