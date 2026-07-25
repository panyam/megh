package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// featuresDir holds the on-demand capability scripts baked into every megh image.
const featuresDir = "/usr/local/lib/megh/features"

// featureName restricts what can be run to a simple slug (no path traversal).
var featureName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

var enableCmd = &cobra.Command{
	Use:   "enable [feature]",
	Short: "Add a capability to a box on demand (start slim, add features later)",
	Long: `Run ON a box to install + start a capability on the box's local disk, so you
can start on the slim flavor and add only what you need. Each feature is an
idempotent script baked into the image.

  megh enable             list available features
  megh enable vnc         headed-browser display (noVNC on :6080)
  megh enable playwright  Playwright + Chromium (headed needs 'enable vnc')
  megh enable code        code-server (VS Code on :8080)`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return listFeatures()
		}
		name := args[0]
		if !featureName.MatchString(name) {
			return fmt.Errorf("invalid feature name %q", name)
		}
		script := filepath.Join(featuresDir, name+".sh")
		if _, err := os.Stat(script); err != nil {
			return fmt.Errorf("unknown feature %q (run `megh enable` to list)", name)
		}
		c := exec.Command("bash", script)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		return c.Run()
	},
}

func listFeatures() error {
	entries, err := os.ReadDir(featuresDir)
	if err != nil {
		return fmt.Errorf("run this on a megh box: %s not found here", featuresDir)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sh") {
			names = append(names, strings.TrimSuffix(e.Name(), ".sh"))
		}
	}
	if len(names) == 0 {
		fmt.Println("no features available")
		return nil
	}
	sort.Strings(names)
	fmt.Println("available features (megh enable <name>):")
	for _, n := range names {
		fmt.Printf("  %s\n", n)
	}
	return nil
}

func init() {
	rootCmd.AddCommand(enableCmd)
}
