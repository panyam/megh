package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"

	"github.com/panyam/megh/internal/features"
	"github.com/panyam/megh/internal/providers/runpod"
	"github.com/spf13/cobra"
)

var (
	enableProvider string
	enableLocal    bool
)

// featureName restricts what can be run to a simple slug.
var featureName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

var enableCmd = &cobra.Command{
	Use:   "enable [feature] [box]",
	Short: "Add a capability to a box on demand (start slim, add features later)",
	Long: `Install + start a capability on a box's local disk, so you can start on the
slim flavor and add only what you need. Scripts are embedded in megh, so this
works against any box (piped over SSH) and needs no image rebuild.

  megh enable             list available features
  megh enable vnc         headed-browser display (noVNC on :6080)
  megh enable playwright  Playwright + Chromium (headed needs 'enable vnc')
  megh enable code        code-server (VS Code on :8080)

Runs from the control machine and ssh-es to the box (sole box, or name it as the
second arg). Use --local when running on the box itself.`,
	Args: cobra.MaximumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			fmt.Println("available features (megh enable <name>):")
			for _, n := range features.List() {
				fmt.Printf("  %s\n", n)
			}
			return nil
		}
		name := args[0]
		if !featureName.MatchString(name) {
			return fmt.Errorf("invalid feature name %q", name)
		}
		script, err := features.Script(name)
		if err != nil {
			return fmt.Errorf("%w (run `megh enable` to list)", err)
		}

		if enableLocal {
			c := exec.Command("bash", "-s")
			c.Stdin = bytes.NewReader(script)
			c.Stdout, c.Stderr = os.Stdout, os.Stderr
			return c.Run()
		}

		if enableProvider != "runpod" {
			return fmt.Errorf("provider %q not implemented yet", enableProvider)
		}
		ctx := context.Background()
		var pod *runpod.Pod
		if len(args) == 2 {
			pod, err = runpod.Find(ctx, args[1])
		} else {
			pod, err = runpod.Sole(ctx)
		}
		if err != nil {
			return err
		}
		d := dialFor(pod)
		sshArgs := append(d.opts(), d.userHost(), "bash -s")
		fmt.Fprintf(os.Stderr, "megh: enabling %q on %s\n", name, pod.Name)
		return runSSH(d.keyFor(cfg.SSHKeyFile), nil, sshArgs, bytes.NewReader(script))
	},
}

func init() {
	enableCmd.Flags().StringVar(&enableProvider, "provider", "runpod", "provider (runpod)")
	enableCmd.Flags().BoolVar(&enableLocal, "local", false, "run on the box itself instead of ssh-ing to one")
	rootCmd.AddCommand(enableCmd)
}
