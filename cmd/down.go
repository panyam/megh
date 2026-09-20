package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/panyam/megh/internal/providers"
	"github.com/spf13/cobra"
)

var (
	downProvider string
	downYes      bool
)

// deregisterTimeout bounds the pre-terminate tailnet logout. Generous enough for
// a reachable box (the remote command alone allows 15s) and short enough that an
// unreachable one does not hold up termination.
const deregisterTimeout = 25 * time.Second

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
		prov, err := resolveProvider(cmd, downProvider)
		if err != nil {
			return err
		}
		ctx := context.Background()
		pod, err := providers.FindOrSole(ctx, prov, args)
		if err != nil {
			return err
		}
		if !downYes {
			fmt.Printf("terminate %s (%s, %s)? the volume survives. [y/N]: ",
				pod.DisplayName(), pod.ID, pod.DataCenter)
			var resp string
			fmt.Scanln(&resp)
			if !strings.EqualFold(strings.TrimSpace(resp), "y") {
				fmt.Println("aborted")
				return nil
			}
		}
		// Best-effort: have the box deregister itself before we terminate it — the
		// node-side opposite of `tailscale up`, so an ephemeral node is removed
		// immediately instead of lingering until GC (and a persistent one is
		// deauthenticated). Runs over the SSH access megh already has, so no
		// Tailscale API credential is needed. Never blocks termination: an
		// unreachable box just skips it.
		//
		// The whole attempt is under a wall-clock deadline. A box with no public
		// SSH dials its MagicDNS name, and from a control machine that is not on
		// the tailnet that name never resolves — ssh's ConnectTimeout does not
		// cover resolution, so without this the terminate step never runs and the
		// pod keeps billing.
		//
		// Skipped on a backend with no mesh, and silent for a box that was on a
		// mesh-capable backend without ever joining one: the box itself reports
		// whether it was connected (see meshLeaveMessage).
		if prov.Mesh().On() {
			d := dialFor(pod)
			lctx, cancel := context.WithTimeout(ctx, deregisterTimeout)
			defer cancel()
			if msg := meshLeave(lctx, d, pod.DisplayName()); msg != "" {
				fmt.Println(msg)
			}
		}

		if err := prov.Terminate(ctx, pod.ID); err != nil {
			return err
		}
		fmt.Printf("terminated %s (%s)\n", pod.DisplayName(), pod.ID)
		// The SSH logout above is the clean path, but it cannot run on a box that
		// was already unreachable, which is how nodes go stale in the first place.
		// Now that the box is definitely gone, remove its node from the control
		// plane too. Best effort and silent when no API key is configured.
		if prov.Mesh().On() {
			pruneNodesBestEffort(ctx, prov, pod.DisplayName())
		}
		publishPortalBestEffort()
		return nil
	},
}

func init() {
	downCmd.Flags().StringVar(&downProvider, "provider", "", "provider (default: config default_provider, else runpod)")
	downCmd.Flags().BoolVarP(&downYes, "yes", "y", false, "skip the confirmation prompt")
	rootCmd.AddCommand(downCmd)
}
