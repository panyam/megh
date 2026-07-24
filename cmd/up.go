package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/panyam/megh/internal/providers/runpod"
	"github.com/spf13/cobra"
)

var (
	upProvider string
	upOpts     runpod.Options
)

var upCmd = &cobra.Command{
	Use:   "up",
	Short: "Launch a dev box on a provider",
	RunE: func(cmd *cobra.Command, args []string) error {
		switch upProvider {
		case "runpod":
			res, err := runpod.Up(context.Background(), upOpts)
			if err != nil {
				return err
			}
			fmt.Print(res.Summary())
			return nil
		case "":
			return fmt.Errorf("--provider is required (runpod)")
		default:
			return fmt.Errorf("provider %q not implemented yet", upProvider)
		}
	},
}

func init() {
	f := upCmd.Flags()
	f.StringVar(&upProvider, "provider", "", "provider to launch on (runpod)")
	f.StringVar(&upOpts.Name, "name", "", "box name (default megh-<user>-box)")
	f.IntVar(&upOpts.VCPU, "vcpu", 4, "vCPU count")
	f.IntVar(&upOpts.RAMGiB, "ram", 16, "RAM in GiB")
	f.IntVar(&upOpts.DiskGiB, "disk", 20, "ephemeral container disk in GiB (RunPod caps this by instance size, ~20-60; persistent scratch is the network volume)")
	f.StringVar(&upOpts.Image, "image", os.Getenv("MEGH_IMAGE"), "container image (default $MEGH_IMAGE)")
	f.StringVar(&upOpts.VolumeID, "volume", os.Getenv("MEGH_VOLUME_ID"), "network volume id (default $MEGH_VOLUME_ID)")
	f.StringVar(&upOpts.DataCenter, "dc", os.Getenv("MEGH_DC"), "data center id (default $MEGH_DC)")
	f.StringVar(&upOpts.PubKey, "pubkey", os.Getenv("MEGH_PUBKEY"), "SSH public key (default $MEGH_PUBKEY)")
	rootCmd.AddCommand(upCmd)
}
