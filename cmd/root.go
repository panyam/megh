package cmd

import (
	"fmt"
	"os"

	"github.com/panyam/megh/internal/config"
	"github.com/spf13/cobra"
)

// cfg is the resolved configuration, loaded once in PersistentPreRunE and read
// by subcommands. cfgFlag is the optional --config path.
var (
	cfg     config.Config
	cfgFlag string
)

var rootCmd = &cobra.Command{
	Use:   "megh",
	Short: "Self-hosted cloud dev boxes for agentic coding",
	Long: `megh launches and manages disposable cloud dev boxes across providers.

Boxes are cattle: everything precious lives in git and on a scratch volume, so
losing a box costs minutes. The dev environment is declared once and built into
two artifacts (a container image and a VM image); the provider picks which.

Settings come from megh.yaml (checked in); secrets stay in the environment,
referenced by megh.yaml via env-var names, never stored in the repo.`,
	SilenceUsage: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := config.Load(cfgFlag)
		if err != nil {
			return err
		}
		cfg = c
		adoptSecrets(cfg)
		return nil
	},
}

// Execute runs the root command.
func Execute() {
	cfg = config.Default()
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "megh:", err)
		os.Exit(1)
	}
}

// adoptSecrets copies secret VALUES from the env-var names configured in
// megh.yaml into the conventional names the provider backends read, so a custom
// env-var name in the config just works. Values are only ever read from the
// environment, never from the file.
func adoptSecrets(c config.Config) {
	adopt := func(canonical, configured string) {
		if configured == "" || configured == canonical || os.Getenv(canonical) != "" {
			return
		}
		if v := os.Getenv(configured); v != "" {
			os.Setenv(canonical, v)
		}
	}
	if p, ok := c.Providers["runpod"]; ok {
		adopt("RUNPOD_API_KEY", p.APIKeyEnv)
	}
	adopt("TS_AUTHKEY", c.Tailscale.AuthKeyEnv)
	adopt("MEGH_SESSIONS_TOKEN", c.Sessions.TokenEnv)
	// Sessions repo is not a secret; surface it so the launcher passes it as pod env.
	if os.Getenv("MEGH_SESSIONS_REPO") == "" && c.Sessions.Repo != "" {
		os.Setenv("MEGH_SESSIONS_REPO", c.Sessions.Repo)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFlag, "config", "",
		"path to megh.yaml (default: auto-discover from cwd upward, then ~/.config/megh)")
}
