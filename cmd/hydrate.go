package cmd

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/providers/runpod"
	"github.com/spf13/cobra"
)

var (
	hydrateProvider string
	hydrateCheck    bool
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
		if !pod.SSHReady() {
			return fmt.Errorf("ssh endpoint for %q not ready yet", pod.Name)
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

		sshArgs := []string{
			"-A", "-p", strconv.Itoa(pod.SSHPort),
			"-o", "StrictHostKeyChecking=accept-new",
			"root@" + pod.PublicIP, "bash -s",
		}
		return runSSH(cfg.SSHKeyFile, fwdKeys, sshArgs, strings.NewReader(script))
	},
}

func applyScript(c config.Config) string {
	var b strings.Builder
	b.WriteString("set -e\nexport GIT_SSH_COMMAND='ssh -o StrictHostKeyChecking=accept-new'\nmkdir -p /mnt/work/repos\n")
	for _, r := range c.Repos {
		d := repoDir(r.URL)
		u := aliasedURL(r.URL, c.GHKey(r))
		fmt.Fprintf(&b,
			"if [ -d /mnt/work/repos/%s/.git ]; then echo 'exists  %s'; "+
				"else echo 'clone   %s'; git clone %q /mnt/work/repos/%s; fi\n",
			d, d, d, u, d)
	}
	return b.String()
}

func checkScript(c config.Config) string {
	var b strings.Builder
	b.WriteString("declared=(")
	for _, r := range c.Repos {
		fmt.Fprintf(&b, "%q ", repoDir(r.URL))
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
	rootCmd.AddCommand(hydrateCmd)
}
