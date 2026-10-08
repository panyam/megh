package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/panyam/megh/internal/config"
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

On a box it runs right there (no box name, no RUNPOD_API_KEY). With an SSH
agent forwarded it clones as above; without one (Tailscale's console, webterm)
it clones over https using gh's login, so run 'gh auth login' once per volume.
A box nothing has touched yet also needs its megh.yaml: 'megh config pull'.

--check reports drift: declared-but-missing on the volume, and on-volume-but-
undeclared (with origin url to copy into megh.yaml).`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Local: run ON a box (no jump box), which is the default when megh is
		// on one and no box is named. Clone the repos: list straight into
		// /mnt/work/repos with the box's own git: over the SSH agent `megh ssh`
		// forwards when there is one, else over https with gh's login (see
		// cloneTransport). No SSH-to-self, no RunPod API key needed.
		if hydrateRunsHere(hydrateLocal, args) {
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

		ctx := context.Background()
		prov, pod, err := locateBox(ctx, cmd, hydrateProvider, args)
		if err != nil {
			return err
		}
		pod = awaitSSHReady(ctx, prov, pod)
		d := dialFor(pod)
		if err := d.preflight(pod); err != nil {
			return err
		}
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

// hydrateRunsHere is enableRunsHere for hydrate: --local, or on a box with no
// box named, clones on this machine; naming a box always targets that box.
func hydrateRunsHere(local bool, args []string) bool {
	if local {
		return true
	}
	return len(args) == 0 && onABox()
}

// repoDest is the path under /mnt/work/repos: explicit dir, else URL basename.
func repoDest(r config.Repo) string {
	if r.Dir != "" {
		return r.Dir
	}
	return repoDir(r.URL)
}

// cloneTransport picks how the box's git authenticates, once per run. A usable
// SSH agent (megh ssh/hydrate forward one) keeps the gh-<identity> aliases. With
// none, as in Tailscale's console or webterm, it clones over https through gh's
// credential helper, and refuses up front if gh is not logged in rather than
// letting git stop at a password prompt nobody will answer.
const cloneTransport = `if ssh-add -l >/dev/null 2>&1; then via=ssh; else
  via=https
  if ! gh auth status >/dev/null 2>&1; then
    echo "megh: no SSH agent here and gh is not logged in, so private repos cannot be cloned." >&2
    echo "megh: run 'gh auth login -h github.com -p https -w' (once per volume), then hydrate again." >&2
    exit 1
  fi
  gh auth setup-git >/dev/null 2>&1 || true
  export GIT_TERMINAL_PROMPT=0
  echo "megh: no SSH agent; cloning over https with gh's credentials"
fi
`

// megh_clone clones one repo, and is the reason one bad repo no longer stops a
// hydrate. A destination that exists without .git but holds only directories
// is what the entrypoint leaves on a fresh volume (it makes the parents of
// every symlink target, and some targets sit inside repos not yet cloned), so
// those empty directories are removed and the clone goes ahead. A destination
// with any file or symlink in it is never touched: it is skipped, named, and
// the run carries on and exits non-zero at the end.
const cloneFunc = `skipped=""
megh_clone() {
  dest="$repos/$1"; mkdir -p "$(dirname "$dest")"
  if [ -d "$dest/.git" ]; then echo "exists  $1"; return 0; fi
  if [ -e "$dest" ]; then
    first=$(find "$dest" ! -type d -print 2>/dev/null | head -n 1)
    if [ -n "$first" ]; then
      echo "skip    $1: not a git repo and not empty (first: ${first#"$repos"/})" >&2
      skipped="$skipped $1"; return 0
    fi
    find "$dest" -depth -type d -empty -delete
    echo "cleared $1: only empty folders were there (made by the boot script's symlinks)"
  fi
  echo "clone   $1"
  if ! git clone "$2" "$dest"; then echo "failed  $1" >&2; skipped="$skipped $1"; fi
}
`

func applyScript(c config.Config) string {
	var b strings.Builder
	b.WriteString("set -e\nexport GIT_SSH_COMMAND='ssh -o StrictHostKeyChecking=accept-new'\n")
	b.WriteString("repos=\"${MEGH_REPOS_ROOT:-/mnt/work/repos}\"\nmkdir -p \"$repos\"\n")
	b.WriteString(cloneTransport)
	b.WriteString(cloneFunc)
	for _, r := range c.Repos {
		fmt.Fprintf(&b, "u=%q; [ \"$via\" = https ] && u=%q; megh_clone %q \"$u\"\n",
			aliasedURL(r.URL, c.GHKey(r)), githubHTTPS(r.URL), repoDest(r))
	}
	b.WriteString(`if [ -n "$skipped" ]; then echo "megh: hydrate did not clone:$skipped" >&2; exit 1; fi` + "\n")
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
	// Declared dests are multi-segment (group/repo/main), so a one-level
	// scan of repos/*/ can never match them: it reports every GROUP dir as
	// undeclared and misses a real stray nested below. Walk for actual clones
	// instead (a dir holding .git), prune at each one so submodules and vendored
	// checkouts inside a declared repo stay quiet, and compare the path relative
	// to repos/.
	b.WriteString(`drift=0; ` +
		`while IFS= read -r p; do ` +
		`n="${p#/mnt/work/repos/}"; ok=0; ` +
		`for d in "${declared[@]}"; do [ "$d" = "$n" ] && ok=1; done; ` +
		`if [ "$ok" -eq 0 ]; then o=$(git -C "$p" remote get-url origin 2>/dev/null || echo "(no origin)"); ` +
		`echo "  UNDECLARED  $n  $o"; drift=1; fi; ` +
		`done < <(find /mnt/work/repos -mindepth 1 -type d -exec test -d '{}/.git' \; -prune -print 2>/dev/null | sort); ` +
		`[ "$drift" -eq 0 ] && echo "  (none)"` + "\n")
	// --check is a report, not a gate: end on a clean exit so a drift finding
	// does not surface as a bare "Error: exit status 1" from the ssh command.
	b.WriteString("exit 0\n")
	return b.String()
}

func init() {
	hydrateCmd.Flags().StringVar(&hydrateProvider, "provider", "", "look the box up on this provider only (default: every provider with a credential)")
	hydrateCmd.Flags().BoolVar(&hydrateCheck, "check", false, "report drift instead of applying")
	hydrateCmd.Flags().BoolVar(&hydrateLocal, "local", false, "run on the box itself (clone repos locally, no jump box)")
	rootCmd.AddCommand(hydrateCmd)
}
