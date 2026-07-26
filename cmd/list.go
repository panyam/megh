package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/panyam/megh/internal/providers/runpod"
	"github.com/spf13/cobra"
)

// shortImage trims the registry/namespace prefix for display:
// ghcr.io/panyam/megh-slim:latest -> megh-slim:latest
func shortImage(image string) string {
	if image == "" {
		return "-"
	}
	if i := strings.LastIndex(image, "/"); i >= 0 {
		return image[i+1:]
	}
	return image
}

var (
	listProvider string
	listAll      bool
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List megh dev boxes (use --all for every pod on the account)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if listProvider != "runpod" {
			return fmt.Errorf("provider %q not implemented yet", listProvider)
		}
		pods, err := runpod.List(context.Background())
		if err != nil {
			return err
		}
		if !listAll {
			pods = runpod.ManagedPods(pods)
		}
		if len(pods) == 0 {
			fmt.Println("no boxes")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tID\tSTATUS\tIMAGE\tDC\t$/HR\tSSH")
		for _, p := range pods {
			ssh := "initializing"
			if p.SSHReady() {
				ssh = fmt.Sprintf("%s:%d", p.PublicIP, p.SSHPort)
			}
			// Managed view shows the bare name the user typed; --all keeps the raw
			// pod name so megh boxes stay visibly distinct from foreign pods.
			name := p.DisplayName()
			if listAll {
				name = p.Name
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%.3f\t%s\n",
				name, p.ID, p.Status, shortImage(p.Image), p.DataCenter, p.CostPerHr, ssh)
		}
		return w.Flush()
	},
}

func init() {
	listCmd.Flags().StringVar(&listProvider, "provider", "runpod", "provider (runpod)")
	listCmd.Flags().BoolVar(&listAll, "all", false, "show every pod on the account, not just megh-managed")
	rootCmd.AddCommand(listCmd)
}
