package cmd

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/panyam/megh/internal/providers/runpod"
	"github.com/spf13/cobra"
)

var (
	sshProvider string
	sshTunnel   bool
)

var sshCmd = &cobra.Command{
	Use:   "ssh [box-name-or-id]",
	Short: "SSH into a box, tunneling the web shell and headed-browser surfaces",
	Long: `SSH into a provisioned box with the profile's box key, forwarding the
profile's GitHub identity keys (private keys never touch the box). Per-identity
Host aliases (gh-<name>) are configured on the box so per-repo remotes use the
right key.

By default it also forwards the box's web surfaces to your localhost:
  web shell    -> http://localhost:7681
  headed vnc   -> http://localhost:6080/vnc.html

With no argument it connects to the only box; otherwise pass a name or id.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if sshProvider != "runpod" {
			return fmt.Errorf("provider %q not implemented yet", sshProvider)
		}
		ctx := context.Background()
		var (
			pod *runpod.Pod
			err error
		)
		if len(args) == 1 {
			pod, err = runpod.Find(ctx, args[0])
		} else {
			pod, err = runpod.Sole(ctx)
		}
		if err != nil {
			return err
		}
		if !pod.SSHReady() {
			return fmt.Errorf("ssh endpoint for %q not ready yet (pod initializing); try again shortly", pod.Name)
		}

		// Configure per-identity GitHub Host aliases on the box first (no key
		// forwarding needed just to write files).
		var fwdKeys []string
		if activeProfile != nil {
			fwdKeys = activeProfile.GHKeyFiles()
			setup, serr := ghSetupScript(activeProfile)
			if serr != nil {
				return serr
			}
			if setup != "" {
				setupArgs := []string{
					"-p", strconv.Itoa(pod.SSHPort),
					"-o", "StrictHostKeyChecking=accept-new",
					"root@" + pod.PublicIP, "bash -s",
				}
				if err := runSSH(cfg.SSHKeyFile, nil, setupArgs, strings.NewReader(setup)); err != nil {
					fmt.Fprintf(os.Stderr, "megh: warning: gh key setup failed: %v\n", err)
				}
			}
		}

		sshArgs := []string{
			"-A",
			"-p", strconv.Itoa(pod.SSHPort),
			"-o", "StrictHostKeyChecking=accept-new",
		}
		if sshTunnel {
			sshArgs = append(sshArgs, "-L", "7681:localhost:7681", "-L", "6080:localhost:6080")
		}
		sshArgs = append(sshArgs, "root@"+pod.PublicIP)

		tunNote := ""
		if sshTunnel {
			tunNote = "  (+ localhost:7681 web shell, localhost:6080 vnc)"
		}
		fmt.Fprintf(os.Stderr, "megh: ssh root@%s -p %d%s\n", pod.PublicIP, pod.SSHPort, tunNote)
		return runSSH(cfg.SSHKeyFile, fwdKeys, sshArgs, nil)
	},
}

func init() {
	sshCmd.Flags().StringVar(&sshProvider, "provider", "runpod", "provider (runpod)")
	sshCmd.Flags().BoolVar(&sshTunnel, "tunnel", true, "forward web shell (7681) and noVNC (6080) to localhost")
	rootCmd.AddCommand(sshCmd)
}
