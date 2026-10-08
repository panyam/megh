package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/panyam/megh/internal/providers"
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
		ctx := context.Background()
		svc := newService()
		// Pinned (--provider / MEGH_PROVIDER): that backend only. Otherwise every
		// backend with a credential, like `storage list`, so a box on a
		// non-default backend is never invisible.
		backends := []string{resolve(cmd, "provider", listProvider, "MEGH_PROVIDER", cfg.DefaultProvider, "runpod")}
		if !providerPinned(cmd) {
			backends = backends[:0]
			for _, p := range providers.All() {
				backends = append(backends, p.Name())
			}
		}
		type row struct {
			provider string
			box      providers.Box
		}
		var rows []row
		for _, name := range backends {
			pods, err := svc.List(ctx, name, listAll)
			if err != nil {
				if !providerPinned(cmd) {
					if !errors.Is(err, providers.ErrNotConfigured) {
						fmt.Fprintf(os.Stderr, "megh: list: skipping %s: %v\n", name, err)
					}
					continue
				}
				return err
			}
			for _, p := range pods {
				rows = append(rows, row{name, p})
			}
		}
		if len(rows) == 0 {
			fmt.Println("no boxes")
			return nil
		}
		names := newPlaceNamer(ctx, registeredPlacer)
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tPROVIDER\tID\tSTATUS\tIMAGE\tDC\tPLACE\t$/HR\tSSH")
		for _, r := range rows {
			p := r.box
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
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%.3f\t%s\n",
				name, r.provider, p.ID, p.Status, shortImage(p.Image), p.DataCenter, orDash(names.place(r.provider, p.DataCenter)), p.CostPerHr, ssh)
		}
		return w.Flush()
	},
}

func init() {
	listCmd.Flags().StringVar(&listProvider, "provider", "", "list only this provider's boxes (default: every provider with a credential)")
	listCmd.Flags().BoolVar(&listAll, "all", false, "show every pod on the account, not just megh-managed")
	rootCmd.AddCommand(listCmd)
}
