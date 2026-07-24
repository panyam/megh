package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/panyam/megh/internal/providers/runpod"
	"github.com/spf13/cobra"
)

var (
	hydrateProvider string
	hydrateCheck    bool
)

// repoDir derives the working-copy directory name from a git URL:
// git@github.com:panyam/megh.git -> megh
func repoDir(url string) string {
	u := strings.TrimSuffix(strings.TrimSpace(url), ".git")
	if i := strings.LastIndexAny(u, "/:"); i >= 0 {
		u = u[i+1:]
	}
	return u
}

var hydrateCmd = &cobra.Command{
	Use:   "hydrate [box-name-or-id]",
	Short: "Clone the megh.yaml repos onto a box's shared volume (idempotent)",
	Long: `Apply the repos: list from megh.yaml onto a running box's /mnt/work/repos,
without recreating the volume. Idempotent: existing repos are left as-is, missing
ones are cloned using your forwarded SSH agent (no keys land on the box).

--check reports drift instead of applying: which declared repos are missing from
the volume, and which repos on the volume are not declared in megh.yaml (copy
those into megh.yaml to adopt them).`,
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

		script := applyScript(cfg.Repos)
		if hydrateCheck {
			script = checkScript(cfg.Repos)
		}
		ssh := exec.Command("ssh", "-A", "-p", strconv.Itoa(pod.SSHPort),
			"-o", "StrictHostKeyChecking=accept-new", "root@"+pod.PublicIP, "bash -s")
		ssh.Stdin = strings.NewReader(script)
		ssh.Stdout, ssh.Stderr = os.Stdout, os.Stderr
		return ssh.Run()
	},
}

func applyScript(repos []string) string {
	var b strings.Builder
	b.WriteString("set -e\n")
	b.WriteString("export GIT_SSH_COMMAND='ssh -o StrictHostKeyChecking=accept-new'\n")
	b.WriteString("mkdir -p /mnt/work/repos\n")
	for _, url := range repos {
		d := repoDir(url)
		fmt.Fprintf(&b,
			"if [ -d /mnt/work/repos/%s/.git ]; then echo 'exists  %s'; "+
				"else echo 'clone   %s'; git clone %q /mnt/work/repos/%s; fi\n",
			d, d, d, url, d)
	}
	return b.String()
}

func checkScript(repos []string) string {
	var b strings.Builder
	b.WriteString("declared=(")
	for _, url := range repos {
		fmt.Fprintf(&b, "%q ", repoDir(url))
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
