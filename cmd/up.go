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
	upFromBox   bool
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
	Use:   "up <name>",
	Short: "Launch a dev box on a provider",
	Long: `Launch a dev box. <name> is required and must be unique among your live boxes.

The name you give is the box's Tailscale hostname and how megh refers to it, so a
duplicate would collide on the tailnet (and make 'megh list'/'ssh'/'down'
ambiguous); megh errors before launching if the name is already in use. RunPod
has no tags, so the pod itself is stored with a 'megh-' prefix as the marker megh
filters on, but you never type it or see it: 'megh up work' joins the tailnet as
'work' and 'megh ssh work' / 'megh down work' resolve it.`,
	Args: cobra.ExactArgs(1),
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
		// Name is a required positional arg. Enforce the megh- marker up front so
		// the uniqueness check below and the Tailscale hostname both use the final
		// name (runpod.Up applies the same prefix idempotently).
		name := args[0]
		if !strings.HasPrefix(name, runpod.NamePrefix) {
			name = runpod.NamePrefix + name
		}
		upOpts.Name = name

		if err := refuseToSpawnFromABox(); err != nil {
			return err
		}
		if miss := cfg.MissingEnvs(); len(miss) > 0 {
			return fmt.Errorf("required env vars not set (megh.yaml requires): %s", strings.Join(miss, ", "))
		}
		upOpts.ExtraEnv = cfg.BoxEnv()
		// Tool state (logins/config) to persist on the volume across rebuilds; the
		// entrypoint symlinks each onto the volume. Empty -> entrypoint default.
		if len(cfg.Persist) > 0 {
			if upOpts.ExtraEnv == nil {
				upOpts.ExtraEnv = map[string]string{}
			}
			upOpts.ExtraEnv["MEGH_PERSIST"] = strings.Join(cfg.Persist, ",")
		}
		// Home->volume path maps (e.g. ~/newstack -> repos/newstack) so local paths
		// work on the box. Passed as MEGH_SYMLINKS ("link:target,..."); order-free.
		if len(cfg.Symlinks) > 0 {
			if upOpts.ExtraEnv == nil {
				upOpts.ExtraEnv = map[string]string{}
			}
			var pairs []string
			for link, target := range cfg.Symlinks {
				pairs = append(pairs, link+":"+target)
			}
			upOpts.ExtraEnv["MEGH_SYMLINKS"] = strings.Join(pairs, ",")
		}

		switch upProvider {
		case "runpod":
			ctx := context.Background()
			// Names double as the Tailscale hostname, so refuse a duplicate before
			// launching rather than let two boxes fight over one tailnet name.
			pods, err := runpod.List(ctx)
			if err != nil {
				return err
			}
			for _, p := range runpod.ManagedPods(pods) {
				if p.Name == upOpts.Name {
					return fmt.Errorf("a box named %q already exists (id %s); pick another name or `megh down %s` first",
						upOpts.Name, p.ID, strings.TrimPrefix(upOpts.Name, runpod.NamePrefix))
				}
			}
			// Mint this box its own Tailscale key, if configured. Best effort: a
			// failure falls back to the shared static key rather than blocking a
			// launch, because the tailnet is a convenience layer and public SSH is
			// the path the control machine actually uses.
			upOpts.TSAuthKey = mintBoxAuthKey(ctx, runpod.ShortName(upOpts.Name))

			res, err := runpod.Up(ctx, upOpts)
			if err != nil {
				return err
			}
			fmt.Print(res.Summary())
			publishPortalBestEffort()
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
	f.BoolVar(&upFromBox, "i-am-the-control-plane", false,
		"allow launching from a box (needs a provider credential there; see CONSTRAINTS C3)")
	f.StringVar(&upProvider, "provider", "", "provider (default: config default_provider, else runpod)")
	f.StringVar(&upFlavor, "flavor", "", "dev-env flavor; the image is megh-<flavor> (default: slim; use base for frontend)")
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

// boxMarker is written into the image by the Dockerfile, so its presence is a
// reliable "we are running ON a megh box" signal.
const boxMarker = "/etc/megh/build-info"

// refuseToSpawnFromABox stops `megh up` running on a box.
//
// Launching boxes needs a provider credential, and a box holding one can
// terminate and launch every other box on the account, which is what
// CONSTRAINTS C3 exists to prevent. megh already refuses to SEND that credential
// to a box; this closes the other half, where someone puts it there by hand and
// the property quietly stops holding.
//
// The intended shape is that spawning happens from a device you physically hold
// (a phone running megh in Termux is enough) while boxes only ever do the work.
// Spawning is rare and privileged, working is constant and unprivileged, so they
// belong on different machines.
//
// --i-am-the-control-plane overrides it, deliberately verbose: an escape hatch
// you cannot type by accident, for a box you have decided to elevate.
func refuseToSpawnFromABox() error {
	if upFromBox {
		return nil
	}
	if _, err := os.Stat(boxMarker); err != nil {
		return nil // not a box; the normal case
	}
	return fmt.Errorf(`refusing to launch a box from another box.

A box that can spawn boxes needs a provider credential, and one that holds it can
terminate every other box on the account (CONSTRAINTS.md C3). Spawn from a device
you hold instead; megh runs fine on a phone under Termux.

If this box IS your control plane, say so: megh up --i-am-the-control-plane`)
}
