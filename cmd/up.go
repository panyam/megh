package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/providers/runpod"
	"github.com/spf13/cobra"
)

var (
	upProvider string
	upFlavor   string
	upOpts     runpod.Options
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// defaultPubKey falls back to the user's ed25519 public key so a box launched
// without MEGH_PUBKEY is still reachable over SSH.
func defaultPubKey() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(home, ".ssh", "id_ed25519.pub"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

var upCmd = &cobra.Command{
	Use:   "up",
	Short: "Launch a dev box on a provider",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Resolve sensible defaults so a bare `megh up` works.
		if upOpts.Image == "" {
			upOpts.Image = config.Default().DefaultImage(upFlavor)
		}
		if upOpts.PubKey == "" {
			upOpts.PubKey = defaultPubKey()
		}
		switch upProvider {
		case "runpod":
			res, err := runpod.Up(context.Background(), upOpts)
			if err != nil {
				return err
			}
			fmt.Print(res.Summary())
			return nil
		case "":
			return fmt.Errorf("--provider is required (set --provider or $MEGH_PROVIDER)")
		default:
			return fmt.Errorf("provider %q not implemented yet", upProvider)
		}
	},
}

func init() {
	f := upCmd.Flags()
	f.StringVar(&upProvider, "provider", envOr("MEGH_PROVIDER", "runpod"),
		"provider (default $MEGH_PROVIDER, else runpod)")
	f.StringVar(&upFlavor, "flavor", envOr("MEGH_FLAVOR", "base"),
		"dev-env flavor; the default image is megh-<flavor>")
	f.StringVar(&upOpts.Name, "name", "", "box name (default megh-<user>-box)")
	f.IntVar(&upOpts.VCPU, "vcpu", 4, "vCPU count")
	f.IntVar(&upOpts.RAMGiB, "ram", 16, "RAM in GiB")
	f.IntVar(&upOpts.DiskGiB, "disk", 20,
		"ephemeral container disk in GiB (capped by instance size; persistent scratch is the volume)")
	f.StringVar(&upOpts.Image, "image", os.Getenv("MEGH_IMAGE"),
		"container image (default $MEGH_IMAGE, else ghcr.io/<namespace>/megh-<flavor>:latest)")
	f.StringVar(&upOpts.VolumeID, "volume", os.Getenv("MEGH_VOLUME_ID"), "network volume id (default $MEGH_VOLUME_ID)")
	f.StringVar(&upOpts.DataCenter, "dc", os.Getenv("MEGH_DC"), "data center id (default $MEGH_DC)")
	f.StringVar(&upOpts.PubKey, "pubkey", os.Getenv("MEGH_PUBKEY"),
		"SSH public key (default $MEGH_PUBKEY, else ~/.ssh/id_ed25519.pub)")
	rootCmd.AddCommand(upCmd)
}
