package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/providers"
	"github.com/spf13/cobra"
)

var (
	upProvider  string
	upFlavor    string
	upExposeSSH bool
	upOpts      providers.Options
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
func resolvePubKey(cmd *cobra.Command, flagVal, cfgFile string) (string, error) {
	if cmd.Flags().Changed("pubkey") {
		return flagVal, nil
	}
	if v := os.Getenv("MEGH_PUBKEY"); v != "" {
		return v, nil
	}
	path := cfgFile
	if path == "" {
		path = "~/.ssh/id_ed25519.pub"
	}
	b, err := os.ReadFile(config.ExpandPath(path))
	if err != nil {
		// This used to return "" and launch anyway. The pod then comes up with no
		// key of yours in authorized_keys and nothing says so: `megh up` prints a
		// normal summary, the box bills, and the failure surfaces minutes later as
		// "Permission denied (publickey)" with no hint that the cause was a file
		// missing HERE. It cost a pod and a rebuild before being understood.
		//
		// A control machine set up long ago has this file and never notices it is
		// load-bearing, so the case only appears when megh runs somewhere new --
		// which is exactly where it is hardest to diagnose.
		return "", fmt.Errorf(`no SSH public key to inject, so the box would be unreachable.

Tried: %s (%v)

Give it one of:
  megh profile create <name> && megh profile use <name>   (recommended; megh mints and injects a box key)
  export MEGH_PUBKEY="$(cat ~/.ssh/some_key.pub)"
  megh up --pubkey "$(cat ~/.ssh/some_key.pub)"`, path, err)
	}
	return strings.TrimSpace(string(b)), nil
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

		prov, err := resolveProvider(cmd, upProvider)
		if err != nil {
			return err
		}
		p := cfg.Provider(upProvider)
		upOpts.DataCenter = resolve(cmd, "dc", upOpts.DataCenter, "MEGH_DC", p.DefaultDC, "")
		upOpts.VolumeID = resolve(cmd, "volume", upOpts.VolumeID, "MEGH_VOLUME_ID", p.DefaultVolume, "")
		upOpts.Image = resolve(cmd, "image", upOpts.Image, "MEGH_IMAGE", "", cfg.DefaultImage(upFlavor))
		upOpts.PubKey, err = resolvePubKey(cmd, upOpts.PubKey, cfg.SSHPubKeyFile)
		if err != nil {
			return err
		}
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
		upOpts.Name = providers.PrefixName(args[0])

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

		ctx := context.Background()
		// Names double as the Tailscale hostname, so refuse a duplicate before
		// launching rather than let two boxes fight over one tailnet name.
		boxes, err := prov.List(ctx)
		if err != nil {
			return err
		}
		for _, b := range providers.Managed(boxes) {
			if b.Name == upOpts.Name {
				return fmt.Errorf("a box named %q already exists (id %s); pick another name or `megh down %s` first",
					upOpts.Name, b.ID, providers.ShortName(upOpts.Name))
			}
		}
		upOpts.TSAuthKey = bootAuthKey(ctx, prov.Mesh(), providers.ShortName(upOpts.Name))

		res, err := prov.Up(ctx, upOpts)
		if err != nil {
			return err
		}
		fmt.Print(res.Summary())
		publishPortalBestEffort()
		return nil
	},
}

// bootAuthKey returns the node key to place in the box's create-time env.
//
// Only a backend that joins at boot gets one. A local box is joined afterwards
// over SSH (`megh mesh join`), so minting here would spend a single-use key on
// nothing and write a credential into the container's stored env, which
// `docker inspect` then keeps for the life of the box.
//
// Best effort even when it does apply: a failure warns and falls back rather
// than blocking a launch, because the control machine reaches a box over public
// SSH and the mesh is the convenience layer on top.
func bootAuthKey(ctx context.Context, m providers.Mesh, box string) string {
	if !m.AtBoot {
		return ""
	}
	return mintBoxAuthKey(ctx, box)
}

func init() {
	f := upCmd.Flags()
	// Defaults are empty/zero so `Changed` distinguishes an explicit flag from a
	// fallback; real defaults come from env/config/builtin in RunE (see resolve).
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
