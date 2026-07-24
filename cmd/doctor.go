package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Probe a box's capabilities (DinD, ttyd, noVNC, ssh) [planned]",
	Long: `Runs on a box to report what it can and cannot do. Planned checks:

  - docker build inside the box (RunPod DinD viability: decides whether RunPod
    goes beyond a taste-test or real container-in-dev work needs a VM host)
  - ttyd :7681, noVNC :6080, sshd :22 reachable
  - /mnt/work scratch volume mounted and writable`,
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("megh doctor: not implemented yet.")
		return nil
	},
}

func init() { rootCmd.AddCommand(doctorCmd) }
