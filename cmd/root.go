package cmd

import (
	"fmt"
	"os"

	"github.com/panyam/megh/internal/config"
	"github.com/spf13/cobra"
)

// cfg is the resolved configuration, loaded once in Execute and read by
// subcommands. The provider abstraction lives in this CLI: each backend uses
// whatever launches that provider most reliably, behind one set of subcommands.
var cfg config.Config

var rootCmd = &cobra.Command{
	Use:   "megh",
	Short: "Self-hosted cloud dev boxes for agentic coding",
	Long: `megh launches and manages disposable cloud dev boxes across providers.

Boxes are cattle: everything precious lives in git and on a scratch volume, so
losing a box costs minutes. The dev environment is declared once and built into
two artifacts (a container image and a VM image); the provider picks which.`,
	SilenceUsage: true,
}

// Execute runs the root command.
func Execute() {
	cfg = config.Default()
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "megh:", err)
		os.Exit(1)
	}
}
