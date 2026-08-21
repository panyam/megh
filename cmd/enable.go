package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

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

// meghEnvDeny lists MEGH_-prefixed vars that must NOT reach a box despite
// matching the prefix. The prefix is an allowlist for a feature's own knobs, and
// a credential that happens to be named MEGH_* is not one of those.
//
// MEGH_TAILSCALE_API_KEY can delete every node on the tailnet, including
// machines megh never created, so it is strictly worse in a box's hands than the
// provider key C3 already excludes. See CONSTRAINTS.md C5.
var meghEnvDeny = map[string]bool{
	"MEGH_TAILSCALE_API_KEY":       true,
	"MEGH_TAILSCALE_CLIENT_ID":     true,
	"MEGH_TAILSCALE_CLIENT_SECRET": true,
}

// meghEnv renders the caller's MEGH_* environment as shell `export` lines to
// prepend to a feature script. RUNPOD_API_KEY and friends are deliberately NOT
// included: a box holding a provider key could manage your other boxes.
func meghEnv() []byte {
	var b bytes.Buffer
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i < 0 || !strings.HasPrefix(kv, "MEGH_") {
			continue
		}
		if meghEnvDeny[kv[:i]] {
			continue
		}
		k, v := kv[:i], kv[i+1:]
		// Single-quote the value and escape embedded quotes, so arbitrary
		// characters survive the trip without being re-interpreted.
		fmt.Fprintf(&b, "export %s='%s'\n", k, strings.ReplaceAll(v, `'`, `'\''`))
	}
	return b.Bytes()
}

var enableCmd = &cobra.Command{
	Use:   "enable [feature] [box]",
	Short: "Add a capability to a box on demand (start slim, add features later)",
	Long: `Install + start a capability on a box's local disk, so you can start on the
slim flavor and add only what you need. Scripts are embedded in megh, so this
works against any box (piped over SSH) and needs no image rebuild.

  megh enable             list available features
  megh enable webterm     mobile/tablet web terminal + on-screen key bar (:7682)
  megh enable vnc         headed-browser display (noVNC on :6080)
  megh enable playwright  Playwright + Chromium (headed needs 'enable vnc')
  megh enable code        code-server (VS Code on :8080)
  megh enable postgres    PostgreSQL + pgvector on :5433 (one db per project)
  megh enable redis       Redis on :6399
  megh enable lgtm        dev/demo observability: Grafana + Loki + Tempo + Mimir
                          behind one OTLP collector (:4317/:4318, UI on :3000).
                          Multi-tenant, one tenant per project. Off until you
                          run 'lgtm start' on the box.

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
		// Feature scripts take MEGH_*-prefixed knobs (storage roots, pinned
		// versions, tenant lists). The script is piped to a remote `bash -s`, which
		// inherits nothing from this shell, so carry those vars across explicitly.
		script = append(meghEnv(), script...)

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
		fmt.Fprintf(os.Stderr, "megh: enabling %q on %s\n", name, pod.DisplayName())
		return runSSH(d.keyFor(cfg.SSHKeyFile), nil, sshArgs, bytes.NewReader(script))
	},
}

func init() {
	enableCmd.Flags().StringVar(&enableProvider, "provider", "runpod", "provider (runpod)")
	enableCmd.Flags().BoolVar(&enableLocal, "local", false, "run on the box itself instead of ssh-ing to one")
	rootCmd.AddCommand(enableCmd)
}
