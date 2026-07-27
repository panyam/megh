package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/providers/runpod"
	"github.com/spf13/cobra"
)

var (
	hydrateProvider string
	hydrateCheck    bool
	hydrateLocal    bool
)

var hydrateCmd = &cobra.Command{
	Use:   "hydrate [box-name-or-id]",
	Short: "Clone the megh.yaml repos onto a box's shared volume (idempotent)",
	Long: `Apply the repos: list from megh.yaml onto a running box's /mnt/work/repos,
without recreating the volume. Idempotent: existing repos are left as-is, missing
ones are cloned. Each repo authenticates as its GitHub identity (repo key, else
default_gh_key) via that identity's forwarded key; private keys never touch the
box. The box's ~/.ssh/config gets a per-identity Host alias first.

--check reports drift: declared-but-missing on the volume, and on-volume-but-
undeclared (with origin url to copy into megh.yaml).`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// --local: run ON a box (no jump box). Clone the repos: list straight into
		// /mnt/work/repos using the box's own git (the SSH agent forwarded by
		// `megh ssh` and the gh-* aliases it set up handle auth). No SSH-to-self,
		// no RunPod API key needed.
		if hydrateLocal {
			if len(cfg.Repos) == 0 && !hydrateCheck {
				return fmt.Errorf("no repos: declared in megh.yaml")
			}
			script := applyScript(cfg)
			if hydrateCheck {
				script = checkScript(cfg)
			}
			c := exec.Command("bash", "-c", script)
			c.Stdout, c.Stderr = os.Stdout, os.Stderr
			return c.Run()
		}

		if hydrateProvider != "runpod" {
			return fmt.Errorf("provider %q not implemented yet", hydrateProvider)
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
		d := dialFor(pod)
		if d.tailnet() {
			fmt.Fprintf(os.Stderr, "megh: connecting to %q over the tailnet\n", pod.DisplayName())
		}
		if len(cfg.Repos) == 0 && !hydrateCheck {
			return fmt.Errorf("no repos: declared in megh.yaml")
		}

		// Set up per-identity Host aliases first, and forward the profile's GH
		// keys so the clones can authenticate.
		var (
			setup   string
			fwdKeys []string
		)
		if activeProfile != nil {
			fwdKeys = activeProfile.GHKeyFiles()
			if setup, err = ghSetupScript(activeProfile); err != nil {
				return err
			}
		}
		body := applyScript(cfg)
		if hydrateCheck {
			body = checkScript(cfg)
		}
		script := setup + body

		sshArgs := append(d.opts("-A"), d.userHost(), "bash -s")
		if err := runSSH(d.keyFor(cfg.SSHKeyFile), fwdKeys, sshArgs, strings.NewReader(script)); err != nil {
			return err
		}
		// Copy megh.yaml `files:` (secrets/rc files not in a repo) onto the box.
		if !hydrateCheck {
			if err := pushFiles(d, d.keyFor(cfg.SSHKeyFile), cfg.Files); err != nil {
				fmt.Fprintf(os.Stderr, "megh: warning: file copy failed: %v\n", err)
			}
		}
		return nil
	},
}

// repoDest is the path under /mnt/work/repos: explicit dir, else URL basename.
func repoDest(r config.Repo) string {
	if r.Dir != "" {
		return r.Dir
	}
	return repoDir(r.URL)
}

func applyScript(c config.Config) string {
	var b strings.Builder
	b.WriteString("set -e\nexport GIT_SSH_COMMAND='ssh -o StrictHostKeyChecking=accept-new'\nmkdir -p /mnt/work/repos\n")
	for _, r := range c.Repos {
		d := repoDest(r)
		u := aliasedURL(r.URL, c.GHKey(r))
		fmt.Fprintf(&b,
			"dest=/mnt/work/repos/%s; mkdir -p \"$(dirname \"$dest\")\"; "+
				"if [ -d \"$dest/.git\" ]; then echo 'exists  %s'; "+
				"else echo 'clone   %s'; git clone %q \"$dest\"; fi\n",
			d, d, d, u)
	}
	return b.String()
}

func checkScript(c config.Config) string {
	var b strings.Builder
	b.WriteString("declared=(")
	for _, r := range c.Repos {
		fmt.Fprintf(&b, "%q ", repoDest(r))
	}
	b.WriteString(")\n")
	b.WriteString(`echo "== declared in megh.yaml =="` + "\n")
	b.WriteString(`for d in "${declared[@]}"; do ` +
		`if [ -d "/mnt/work/repos/$d/.git" ]; then echo "  present     $d"; ` +
		`else echo "  MISSING     $d"; fi; done` + "\n")
	b.WriteString(`echo "== on the volume, not declared =="` + "\n")
	b.WriteString(`shopt -s nullglob; drift=0; for p in /mnt/work/repos/*/; do ` +
		`n=$(basename "$p"); ok=0; for d in "${declared[@]}"; do [ "$d" = "$n" ] && ok=1; done; ` +
		`if [ "$ok" -eq 0 ]; then o=$(git -C "$p" remote get-url origin 2>/dev/null || echo "(no origin)"); ` +
		`echo "  UNDECLARED  $n  $o"; drift=1; fi; done; ` +
		`[ "$drift" -eq 0 ] && echo "  (none)"` + "\n")
	return b.String()
}

func init() {
	hydrateCmd.Flags().StringVar(&hydrateProvider, "provider", "runpod", "provider (runpod)")
	hydrateCmd.Flags().BoolVar(&hydrateCheck, "check", false, "report drift instead of applying")
	hydrateCmd.Flags().BoolVar(&hydrateLocal, "local", false, "run on the box itself (clone repos locally, no jump box)")
	rootCmd.AddCommand(hydrateCmd)
}
