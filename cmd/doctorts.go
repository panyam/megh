package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/panyam/megh/internal/providers"
	"github.com/panyam/megh/internal/tsops"
	"github.com/spf13/cobra"
)

var (
	tsLocal    bool
	tsAuthKey  string
	tsProvider string
)

// tsHelperAction maps a user-facing `megh doctor ts <action>` to the ts-up.sh
// action and whether a fresh auth key must be injected into the box's env.
func tsHelperAction(action string) (helper string, injectKey bool, err error) {
	switch action {
	case "logs":
		return "logs", false, nil
	case "status":
		return "status", false, nil
	case "start":
		return "up", false, nil
	case "restart":
		return "restart", false, nil
	case "stop":
		return "down", false, nil
	case "setkey":
		return "up", true, nil
	default:
		return "", false, fmt.Errorf("unknown action %q (logs|status|start|stop|restart|setkey)", action)
	}
}

// tsCmd is the retired spelling of what `megh mesh` now owns. It is hidden
// rather than deleted for two reasons: the entrypoint baked into every image
// built before the move calls `megh doctor ts start --local` at boot, and the
// command is named throughout the notes. It runs the same code the mesh verbs
// run, so there is nothing here to drift.
var tsCmd = &cobra.Command{
	Use:    "ts <logs|status|start|stop|restart|setkey|gc> [box]",
	Hidden: true,
	Short:  "Retired: use megh mesh join|status|logs|restart|gc",
	Long: `Retired. These verbs moved to "megh mesh", which groups them by what they
act on rather than by which vendor provides it:

  megh doctor ts start    ->  megh mesh join
  megh doctor ts setkey   ->  megh mesh join --authkey
  megh doctor ts status   ->  megh mesh status
  megh doctor ts logs     ->  megh mesh logs
  megh doctor ts restart  ->  megh mesh restart
  megh doctor ts gc       ->  megh mesh gc
  megh doctor ts stop     ->  megh mesh leave (logs out; the old stop only
                              disconnected, keeping the box's auth)

It still works, so a box booted from an older image keeps working.

Manage a box's Tailscale connection — the usual reason a box is unreachable.

  logs     show /tmp/tailscale-up.log + the daemon log + status (what failed)
  status   tailscale status
  start    (re)connect, reusing the box's existing auth, then serve the surfaces
  stop     disconnect (tailscale down); the daemon and auth state stay
  restart  bounce tailscaled, then start
  setkey   (re)authenticate with a fresh key, then serve — the fix for an expired
           or invalid TS_AUTHKEY. The key comes from --authkey, else the control
           machine's TS_AUTHKEY.
  gc       delete tailnet nodes left behind by boxes that no longer exist
           (acts on the control plane, not on a box; see megh doctor ts gc -h)

Bring-up uses the same logic as boot (internal/tsops/ts-up.sh), shipped from this
binary, so it works on any box regardless of image age. With no box name it
targets the only box; --local runs on the box itself.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		action := args[0]
		helper, injectKey, err := tsHelperAction(action)
		if err != nil {
			return err
		}
		// For setkey, resolve the key to inject: --authkey wins, else TS_AUTHKEY.
		key := ""
		if injectKey {
			if key = tsAuthKey; key == "" {
				key = os.Getenv("TS_AUTHKEY")
			}
			if key == "" {
				return fmt.Errorf("setkey needs a key: pass --authkey or set TS_AUTHKEY")
			}
		}

		if tsLocal {
			return tsBringUpLocal(helper, key)
		}

		prov, err := resolveProvider(cmd, tsProvider)
		if err != nil {
			return err
		}
		ctx := context.Background()
		var pod *providers.Box
		if len(args) == 2 {
			pod, err = providers.Find(ctx, prov, args[1])
		} else {
			pod, err = providers.Sole(ctx, prov)
		}
		if err != nil {
			return err
		}
		d := dialFor(pod)
		if err := d.preflight(pod); err != nil {
			return err
		}

		fmt.Fprintf(os.Stderr, "megh: ts %s on %s\n", action, pod.DisplayName())
		return tsBringUp(d, pod.DisplayName(), key, helper)
	},
}

// tsBringUp pipes the embedded bring-up script to a box and runs one of its
// actions. Both callers go through here — `megh doctor ts` and `megh mesh join`
// — so repair and the routine join can never drift, exactly as boot and repair
// already share ts-up.sh itself.
func tsBringUp(d dial, host, key, action string) error {
	stdin := bytes.NewReader(tsBringUpStdin(host, key))
	return runSSH(d.keyFor(cfg.SSHKeyFile), nil, tsBringUpArgs(d, action), stdin)
}

// tsBringUpLocal runs an action on THIS machine, which is what the entrypoint
// does at boot: same script, no SSH, no provider.
func tsBringUpLocal(action, key string) error {
	c := exec.Command("bash", "-s", "--", action)
	c.Stdin = bytes.NewReader(tsops.Script())
	c.Env = os.Environ()
	if key != "" {
		c.Env = append(c.Env, "TS_AUTHKEY="+key)
	}
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}

// tsBringUpArgs is the ssh argv for one bring-up action. It carries no secret:
// see tsBringUpStdin.
func tsBringUpArgs(d dial, action string) []string {
	return append(d.opts(), d.userHost(), "bash -s -- "+action)
}

// tsBringUpStdin prepends the script's inputs as exports, so a node key travels
// on stdin rather than in the ssh argv, where it would land in the box's process
// list. An empty key is omitted rather than exported empty: the script then
// reuses whatever auth the box already has, which is what makes a join
// re-runnable and what a restarted box relies on.
func tsBringUpStdin(host, key string) []byte {
	var pre strings.Builder
	fmt.Fprintf(&pre, "export TS_HOSTNAME=%s\n", shQuote(host))
	if key != "" {
		fmt.Fprintf(&pre, "export TS_AUTHKEY=%s\n", shQuote(key))
	}
	return append([]byte(pre.String()), tsops.Script()...)
}

// shQuote single-quotes s for safe embedding in a shell script.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func init() {
	tsCmd.Flags().BoolVar(&tsLocal, "local", false, "run on the box itself instead of ssh-ing to one")
	tsCmd.Flags().StringVar(&tsAuthKey, "authkey", "", "auth key for setkey (default: $TS_AUTHKEY)")
	tsCmd.Flags().StringVar(&tsProvider, "provider", "", "provider (default: config default_provider, else runpod)")
	doctorCmd.AddCommand(tsCmd)
}
