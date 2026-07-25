package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/providers/runpod"
	"github.com/spf13/cobra"
)

var (
	upProvider  string
	upFlavor    string
	upExposeSSH bool
	upOpts      runpod.Options
)

// resolve applies precedence flag > env > config > builtin for a string value.
// The flag var only holds a meaningful value when the flag was explicitly set
// (its registered default is ""), so a set flag wins; otherwise env, then the
// config-derived value, then the builtin default.
func resolve(cmd *cobra.Command, flagName, flagVal, envName, cfgVal, def string) string {
	if cmd.Flags().Changed(flagName) {
		return flagVal
	}
	if envName != "" {
		if v := os.Getenv(envName); v != "" {
			return v
		}
	}
	if cfgVal != "" {
		return cfgVal
	}
	return def
}

func resolveInt(cmd *cobra.Command, flagName string, flagVal, cfgVal, def int) int {
	if cmd.Flags().Changed(flagName) {
		return flagVal
	}
	if cfgVal > 0 {
		return cfgVal
	}
	return def
}

// resolvePubKey reads the SSH public key: explicit flag/env value, else the
// contents of the configured pubkey file (default ~/.ssh/id_ed25519.pub).
func resolvePubKey(cmd *cobra.Command, flagVal, cfgFile string) string {
	if cmd.Flags().Changed("pubkey") {
		return flagVal
	}
	if v := os.Getenv("MEGH_PUBKEY"); v != "" {
		return v
	}
	path := cfgFile
	if path == "" {
		path = "~/.ssh/id_ed25519.pub"
	}
	b, err := os.ReadFile(config.ExpandPath(path))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

var upCmd = &cobra.Command{
	Use:   "up",
	Short: "Launch a dev box on a provider",
	RunE: func(cmd *cobra.Command, args []string) error {
		upProvider = resolve(cmd, "provider", upProvider, "MEGH_PROVIDER", cfg.DefaultProvider, "runpod")
		upFlavor = resolve(cmd, "flavor", upFlavor, "MEGH_FLAVOR", cfg.DefaultFlavor, "slim")

		p := cfg.Provider(upProvider)
		upOpts.DataCenter = resolve(cmd, "dc", upOpts.DataCenter, "MEGH_DC", p.DefaultDC, "")
		upOpts.VolumeID = resolve(cmd, "volume", upOpts.VolumeID, "MEGH_VOLUME_ID", p.DefaultVolume, "")
		upOpts.Image = resolve(cmd, "image", upOpts.Image, "MEGH_IMAGE", "", cfg.DefaultImage(upFlavor))
		upOpts.PubKey = resolvePubKey(cmd, upOpts.PubKey, cfg.SSHPubKeyFile)
		upOpts.VCPU = resolveInt(cmd, "vcpu", upOpts.VCPU, p.VCPU, 2)
		upOpts.RAMGiB = resolveInt(cmd, "ram", upOpts.RAMGiB, p.RAM, 8)
		upOpts.DiskGiB = resolveInt(cmd, "disk", upOpts.DiskGiB, p.Disk, 20)
		upOpts.ExposeSSH = p.PublicSSH()
		if cmd.Flags().Changed("expose-ssh") {
			upOpts.ExposeSSH = upExposeSSH
		}
		if upOpts.Name == "" && activeProfile != nil {
			upOpts.Name = "megh-" + activeProfile.Name + "-box"
		}
		if miss := cfg.MissingEnvs(); len(miss) > 0 {
			return fmt.Errorf("required env vars not set (megh.yaml requires): %s", strings.Join(miss, ", "))
		}
		upOpts.ExtraEnv = cfg.BoxEnv()

		switch upProvider {
		case "runpod":
			res, err := runpod.Up(context.Background(), upOpts)
			if err != nil {
				return err
			}
			fmt.Print(res.Summary())
			return nil
		default:
			return fmt.Errorf("provider %q not implemented yet", upProvider)
		}
	},
}

func init() {
	f := upCmd.Flags()
	// Defaults are empty/zero so `Changed` distinguishes an explicit flag from a
	// fallback; real defaults come from env/config/builtin in RunE (see resolve).
	f.StringVar(&upProvider, "provider", "", "provider (default: config default_provider, else runpod)")
	f.StringVar(&upFlavor, "flavor", "", "dev-env flavor; the image is megh-<flavor> (default: slim; use base for frontend)")
	f.StringVar(&upOpts.Name, "name", "", "box name (default megh-<user>-box)")
	f.IntVar(&upOpts.VCPU, "vcpu", 0, "vCPU count (default: config, else 2)")
	f.IntVar(&upOpts.RAMGiB, "ram", 0, "RAM in GiB (default: config, else 8)")
	f.IntVar(&upOpts.DiskGiB, "disk", 0, "ephemeral container disk in GiB (default: config, else 20)")
	f.StringVar(&upOpts.Image, "image", "", "container image (default $MEGH_IMAGE, else ghcr.io/<ns>/megh-<flavor>:latest)")
	f.StringVar(&upOpts.VolumeID, "volume", "", "network volume id (default $MEGH_VOLUME_ID, else config default_volume)")
	f.StringVar(&upOpts.DataCenter, "dc", "", "data center id (default $MEGH_DC, else config default_dc)")
	f.StringVar(&upOpts.PubKey, "pubkey", "", "SSH public key (default $MEGH_PUBKEY, else config ssh_pubkey_file)")
	f.BoolVar(&upExposeSSH, "expose-ssh", true, "expose public break-glass SSH 22/tcp (default: config; false = tailnet-only)")
	rootCmd.AddCommand(upCmd)
}
