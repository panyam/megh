package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/panyam/megh/internal/providers/runpod"
	"github.com/spf13/cobra"
)

var (
	doctorProvider string
	doctorLocal    bool
)

// doctorScript runs on the box (piped to bash) and prints a short health report.
// The headline is tailscale registration — the usual reason a box is unreachable.
const doctorScript = `set -u
sock=/var/run/tailscale/tailscaled.sock
echo "megh doctor — $(hostname)"

if command -v tailscale >/dev/null 2>&1; then
  js="$(tailscale --socket="$sock" status --json 2>/dev/null || true)"
  if [ -n "$js" ] && command -v jq >/dev/null 2>&1; then
    state="$(printf '%s' "$js" | jq -r '.BackendState // "?"')"
    name="$(printf '%s' "$js" | jq -r '.Self.DNSName // ""' | sed 's/\.$//')"
    ip="$(printf '%s' "$js" | jq -r '.TailscaleIPs[0] // ""')"
    if [ "$state" = "Running" ]; then
      echo "  tailscale : registered as ${name:-?} (${ip:-?})"
    else
      echo "  tailscale : NOT connected (state=${state}); is TS_AUTHKEY set? see /tmp/tailscale-up.log"
    fi
  else
    echo "  tailscale : not up (tailscaled not running / TS_AUTHKEY unset); see /tmp/tailscale-up.log"
  fi
else
  echo "  tailscale : binary not installed"
fi

for pl in "7681:shell" "7682:webterm" "6080:vnc" "8080:code"; do
  p="${pl%%:*}"; label="${pl##*:}"
  if (exec 3<>/dev/tcp/127.0.0.1/$p) 2>/dev/null; then s="up"; else s="down"; fi
  printf "  surface   : %-8s :%s %s\n" "$label" "$p" "$s"
done

if [ -d /mnt/work ]; then
  if touch /mnt/work/.megh-doctor 2>/dev/null; then
    rm -f /mnt/work/.megh-doctor
    echo "  scratch   : /mnt/work writable"
  else
    echo "  scratch   : /mnt/work present but NOT writable"
  fi
else
  echo "  scratch   : /mnt/work missing"
fi
`

var doctorCmd = &cobra.Command{
	Use:   "doctor [box]",
	Short: "Probe a box's health (tailscale, web surfaces, scratch volume)",
	Long: `Report a box's health, from the control machine (ssh) or on the box (--local):

  - tailscale : registered on the tailnet? (the usual reason a box is unreachable)
  - surfaces  : ttyd :7681, webterm :7682, noVNC :6080, code :8080 listening
  - scratch   : /mnt/work present and writable

With no argument it probes the only box; otherwise pass a name or id. (DinD /
docker-build viability is still planned.)`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if doctorLocal {
			c := exec.Command("bash", "-s")
			c.Stdin = bytes.NewReader([]byte(doctorScript))
			c.Stdout, c.Stderr = os.Stdout, os.Stderr
			return c.Run()
		}
		if doctorProvider != "runpod" {
			return fmt.Errorf("provider %q not implemented yet", doctorProvider)
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
		pod = awaitSSHReady(ctx, pod)
		d := dialFor(pod)
		sshArgs := append(d.opts(), d.userHost(), "bash -s")
		fmt.Fprintf(os.Stderr, "megh: probing %s\n", pod.DisplayName())
		return runSSH(d.keyFor(cfg.SSHKeyFile), nil, sshArgs, bytes.NewReader([]byte(doctorScript)))
	},
}

func init() {
	doctorCmd.Flags().StringVar(&doctorProvider, "provider", "runpod", "provider (runpod)")
	doctorCmd.Flags().BoolVar(&doctorLocal, "local", false, "run on the box itself instead of ssh-ing to one")
	rootCmd.AddCommand(doctorCmd)
}
