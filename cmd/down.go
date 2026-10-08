package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/panyam/megh/internal/lifecycle"
	"github.com/panyam/megh/internal/providers"
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
persists and the next box mounts it. Push code to git if you want cross-provider
durability; the volume copy survives regardless.

With no argument it terminates the only box; otherwise pass a name or id.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		svc := newService()
		prov, pod, err := locateBox(ctx, cmd, downProvider, args)
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
		// Best-effort: have the box deregister itself before it is terminated, the
		// node-side opposite of `tailscale up`, so an ephemeral node is removed
		// immediately instead of lingering until GC. Runs over the SSH access megh
		// already has, and is silent for a box that never joined a mesh (see
		// meshLeaveMessage). The service bounds it and never lets it block
		// termination.
		leave := func(lctx context.Context, b providers.Box) string {
			return meshLeave(lctx, dialFor(&b), b.DisplayName())
		}
		if err := svc.Down(ctx, prov, *pod, lifecycle.DownOptions{Leave: leave}); err != nil {
			return err
		}
		publishPortalBestEffort()
		return nil
	},
}

func init() {
	downCmd.Flags().StringVar(&downProvider, "provider", "", "look the box up on this provider only (default: every provider with a credential)")
	downCmd.Flags().BoolVarP(&downYes, "yes", "y", false, "skip the confirmation prompt")
	rootCmd.AddCommand(downCmd)
}
