package cmd

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/panyam/megh/internal/features"
	"github.com/panyam/megh/internal/providers"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/panyam/megh/internal/config"
)

var (
	enableProvider string
	enableLocal    bool
)

// featureName restricts what can be run to a simple slug.
var featureName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// The MEGH_ prefix is an allowlist for a feature's own knobs, and a credential
// that happens to be named MEGH_* is not one of those. The deny list is
// config.IsControlPlaneSecret, shared with the docker backend so the two cannot
// drift. See CONSTRAINTS.md C5.

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
		if config.IsControlPlaneSecret(kv[:i]) {
			continue
		}
		k, v := kv[:i], kv[i+1:]
		// Single-quote the value and escape embedded quotes, so arbitrary
		// characters survive the trip without being re-interpreted.
		fmt.Fprintf(&b, "export %s='%s'\n", k, strings.ReplaceAll(v, `'`, `'\''`))
	}
	return b.Bytes()
}

// isTerminal reports whether f is a terminal rather than a pipe, a file or
// /dev/null. The os.ModeCharDevice bit is NOT this test: /dev/null is a
// character device too, so `megh enable </dev/null` prompted at an input that
// can never answer. x/term asks the tty ioctl, and it is also the part that
// differs between Linux and the Mac megh runs from.
func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// chooseFeature resolves an absent feature name. On a terminal it prompts; piped
// it prints the list and returns "", which the caller treats as "nothing to do"
// rather than an error — `megh enable` with no arguments has always been the way
// to ask what exists, and a prompt there would block a `megh enable | grep`.
func chooseFeature(in *os.File, out io.Writer, names []string) (string, error) {
	if !isTerminal(in) {
		printFeatures(out, names)
		return "", nil
	}
	return pickFromList(in, out, names)
}

// printFeatures is the non-interactive listing.
func printFeatures(out io.Writer, names []string) {
	fmt.Fprintln(out, "available features (megh enable <name>):")
	for _, n := range names {
		fmt.Fprintf(out, "  %-11s %s\n", n, summarize(features.Describe(n)))
	}
}

// pickFromList prints a numbered menu and reads one choice. A number or a name
// both work, since the name is what every other invocation uses and typing it
// should not be punished. Enter cancels.
func pickFromList(in io.Reader, out io.Writer, names []string) (string, error) {
	fmt.Fprintln(out, "features (megh enable <name> [box]):")
	for i, n := range names {
		fmt.Fprintf(out, "  %2d  %-11s %s\n", i+1, n, summarize(features.Describe(n)))
	}
	fmt.Fprintf(out, "choose [1-%d, or a name], Enter to cancel: ", len(names))

	sc := bufio.NewScanner(in)
	if !sc.Scan() {
		fmt.Fprintln(out, "nothing chosen")
		return "", nil
	}
	answer := strings.TrimSpace(sc.Text())
	if answer == "" {
		fmt.Fprintln(out, "nothing chosen")
		return "", nil
	}
	if n, err := strconv.Atoi(answer); err == nil {
		if n < 1 || n > len(names) {
			return "", fmt.Errorf("there is no feature %d; pick 1-%d", n, len(names))
		}
		return names[n-1], nil
	}
	if slices.Contains(names, answer) {
		return answer, nil
	}
	return "", fmt.Errorf("unknown feature %q (run `megh enable` to list)", answer)
}

// summarize trims a feature's own summary to one chooser row. The scripts are
// the source of truth and some of them enumerate everything they install, so the
// clipping belongs here rather than in their headers.
func summarize(desc string) string {
	const width = 68
	if len(desc) <= width {
		return desc
	}
	cut := strings.LastIndex(desc[:width], " ")
	if cut < width/2 {
		cut = width
	}
	return desc[:cut] + "…"
}

var enableCmd = &cobra.Command{
	Use:   "enable [feature] [box]",
	Short: "Add a capability to a box on demand (start slim, add features later)",
	Long: `Install + start a capability on a box's local disk, so you can start on the
slim flavor and add only what you need. Scripts are embedded in megh, so this
works against any box (piped over SSH) and needs no image rebuild.

  megh enable             choose a feature from a menu (a plain list when piped)
  megh enable webterm     mobile/tablet web terminal + on-screen key bar (:7682)
  megh enable vnc         headed-browser display (noVNC on :6080)
  megh enable eda         KiCad, lepton-eda, gerbv, xschem, ngspice, gtkwave,
                          pcb-rnd, ddd + software GL (draws on 'enable vnc')
  megh enable playwright  Playwright + Chromium, plus 'pw-ui' to serve UI mode,
                          the trace viewer or the HTML report on :9323 with no
                          display (a LIVE headed browser needs 'enable vnc')
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
		name := ""
		if len(args) > 0 {
			name = args[0]
		} else {
			// No name: offer the list. On a terminal that is a chooser; piped or
			// scripted it stays the plain listing it has always been.
			picked, err := chooseFeature(os.Stdin, os.Stdout, features.List())
			if err != nil || picked == "" {
				return err
			}
			name = picked
		}
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

		prov, err := resolveProvider(cmd, enableProvider)
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
		sshArgs := append(d.opts(), d.userHost(), "bash -s")
		fmt.Fprintf(os.Stderr, "megh: enabling %q on %s\n", name, pod.DisplayName())
		return runSSH(d.keyFor(cfg.SSHKeyFile), nil, sshArgs, bytes.NewReader(script))
	},
}

func init() {
	enableCmd.Flags().StringVar(&enableProvider, "provider", "", "provider (default: config default_provider, else runpod)")
	enableCmd.Flags().BoolVar(&enableLocal, "local", false, "run on the box itself instead of ssh-ing to one")
	rootCmd.AddCommand(enableCmd)
}
