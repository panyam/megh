package cmd

import (
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/panyam/megh/internal/config"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Show the resolved configuration (settings, plus which secrets are set)",
	RunE: func(cmd *cobra.Command, args []string) error {
		_, path, _ := config.Load(cfgFlag)
		if path == "" {
			fmt.Println("source: built-in defaults (no megh.yaml found)")
		} else {
			fmt.Printf("source: %s\n", path)
		}
		fmt.Println()
		fmt.Printf("default_provider: %s\n", cfg.DefaultProvider)
		fmt.Printf("default_flavor:   %s\n", cfg.DefaultFlavor)
		fmt.Printf("default_image:    %s\n", cfg.DefaultImage(cfg.DefaultFlavor))
		fmt.Printf("ssh_pubkey_file:  %s\n", cfg.SSHPubKeyFile)
		if cfg.Sessions.Repo != "" {
			fmt.Printf("sessions_repo:    %s\n", cfg.Sessions.Repo)
		}

		fmt.Println("\nproviders:")
		names := make([]string, 0, len(cfg.Providers))
		for n := range cfg.Providers {
			names = append(names, n)
		}
		sort.Strings(names)
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "  NAME\tDC\tVOLUME\tVCPU\tRAM\tDISK")
		for _, n := range names {
			p := cfg.Providers[n]
			fmt.Fprintf(w, "  %s\t%s\t%s\t%d\t%d\t%d\n", n, p.DefaultDC, p.DefaultVolume, p.VCPU, p.RAM, p.Disk)
		}
		w.Flush()

		// Secret presence only, never values. Names come from the config pointers.
		fmt.Println("\nsecrets (env vars named by config; values never shown/stored):")
		show := func(envName string) {
			if envName == "" {
				return
			}
			state := "MISSING"
			if os.Getenv(envName) != "" {
				state = "set"
			}
			fmt.Printf("  %-22s %s\n", envName, state)
		}
		if len(cfg.Registries) > 0 {
			show(cfg.Registries[0].TokenEnv)
		}
		if p, ok := cfg.Providers["runpod"]; ok {
			show(p.APIKeyEnv)
		}
		show(cfg.Tailscale.AuthKeyEnv)
		// A missing static node key is the EXPECTED state once minting is on, so
		// say that rather than leave a bare MISSING reading as a problem.
		if cfg.Tailscale.MintKeys && os.Getenv(cfg.Tailscale.AuthKeyEnv) == "" {
			fmt.Printf("  %-22s (not needed: megh mints a key per box)\n", "")
		}
		show(cfg.Tailscale.APIKeyEnv)
		show(cfg.Tailscale.ClientIDEnv)
		show(cfg.Tailscale.ClientSecretEnv)
		if cfg.Tailscale.MintKeys {
			fmt.Printf("  %-22s per-box keys, tag %s\n", "tailscale mint_keys", cfg.Tailscale.Tag)
		}

		if len(cfg.Requires.Envs) > 0 || len(cfg.Requires.BoxEnvs) > 0 {
			fmt.Println("\nrequired env (megh.yaml requires; * = also copied to the box):")
			mark := func(list []string, star bool) {
				for _, e := range list {
					state := "MISSING"
					if os.Getenv(e) != "" {
						state = "set"
					}
					s := " "
					if star {
						s = "*"
					}
					fmt.Printf("  %s %-22s %s\n", s, e, state)
				}
			}
			mark(cfg.Requires.Envs, false)
			mark(cfg.Requires.BoxEnvs, true)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(configCmd)
}
