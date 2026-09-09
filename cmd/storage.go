package cmd

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/panyam/megh/internal/providers"
	"github.com/spf13/cobra"
)

var storageCmd = &cobra.Command{
	Use:     "storage",
	Aliases: []string{"vol", "volume"},
	Short:   "Manage scratch volumes (network storage) across providers",
}

var storageListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List scratch volumes across all providers",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		// One global view across every registered backend.
		vols, errs := providers.VolumesAll(ctx)
		if len(vols) == 0 && len(errs) > 0 {
			return errs[0]
		}
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "megh: skipping a provider: %v\n", e)
		}
		if len(vols) == 0 {
			fmt.Println("no volumes")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "PROVIDER\tID\tNAME\tDC\tSIZE")
		for _, v := range vols {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%dGB\n", v.Provider, v.ID, v.Name, v.DataCenter, v.Size)
		}
		return w.Flush()
	},
}

var (
	storageProvider string
	storageName     string
	storageSize     int
	storageDC       string
)

var storageCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a scratch volume in a data center",
	RunE: func(cmd *cobra.Command, args []string) error {
		prov, err := resolveProvider(cmd, storageProvider)
		if err != nil {
			return err
		}
		if storageDC == "" {
			return fmt.Errorf("--dc is required (the volume is pinned to it)")
		}
		v, err := prov.CreateVolume(context.Background(), storageName, storageSize, storageDC)
		if err != nil {
			return err
		}
		fmt.Printf("created volume %s  (%s, %dGB, %s)\n", v.ID, v.Name, v.Size, v.DataCenter)
		fmt.Printf("launch onto it: megh up --provider runpod --volume %s --dc %s\n", v.ID, v.DataCenter)
		return nil
	},
}

var storageRmCmd = &cobra.Command{
	Use:     "rm <id>",
	Aliases: []string{"delete"},
	Short:   "Delete a scratch volume by id (must be detached from all boxes)",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		prov, err := resolveProvider(cmd, storageProvider)
		if err != nil {
			return err
		}
		if err := prov.DeleteVolume(context.Background(), args[0]); err != nil {
			return err
		}
		fmt.Printf("deleted volume %s\n", args[0])
		return nil
	},
}

func init() {
	storageCreateCmd.Flags().StringVar(&storageProvider, "provider", "", "provider (default: config default_provider, else runpod)")
	storageCreateCmd.Flags().StringVar(&storageName, "name", "megh-scratch", "volume name")
	storageCreateCmd.Flags().IntVar(&storageSize, "size", 100, "size in GiB")
	storageCreateCmd.Flags().StringVar(&storageDC, "dc", "", "data center id (required)")

	storageRmCmd.Flags().StringVar(&storageProvider, "provider", "", "provider (default: config default_provider, else runpod)")

	storageCmd.AddCommand(storageListCmd, storageCreateCmd, storageRmCmd)
	rootCmd.AddCommand(storageCmd)
}
