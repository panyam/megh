package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

const enableVNCScript = "/usr/local/bin/megh-enable-vnc"

var enableVNCCmd = &cobra.Command{
	Use:   "enable-vnc",
	Short: "Add the headed-browser display (noVNC on :6080) to a slim box, on demand",
	Long: `Run this ON a box to add the headed-browser display (Xvfb + x11vnc + noVNC on
6080) after the fact. The base flavor bakes it in; slim defers it, so this
installs the stack to the box's local disk when you actually need a headed
browser, starts it, and serves it on the tailnet (or reach it via an SSH tunnel).
Idempotent.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := os.Stat(enableVNCScript); err != nil {
			return fmt.Errorf("run this on a megh box: %s not found here", enableVNCScript)
		}
		c := exec.Command("bash", enableVNCScript)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		return c.Run()
	},
}

func init() {
	rootCmd.AddCommand(enableVNCCmd)
}
