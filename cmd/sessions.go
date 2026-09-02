package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/profile"
	"github.com/panyam/megh/internal/providers/runpod"
	"github.com/spf13/cobra"
)

// Agent transcripts are the only thing on the volume that nothing else holds.
// Code is in git, dotfiles are in git, caches regenerate, and the tool logins
// under state/ expire or are re-minted in a command. Transcripts are neither.
//
// They used to be pushed BY the box on a timer, which needed a standing GitHub
// credential there (a background timer cannot use SSH agent forwarding). That is
// the exception CONSTRAINTS C3 otherwise forbids, so the direction is inverted:
// the control machine pulls over the SSH it already has and pushes with the
// GitHub identity in the active profile. Nothing on a box can write your history.

var (
	sessionsProvider string
	sessionsDryRun   bool
	sessionsNoPush   bool
)

// sessionSources are the transcript directories on the volume, and the name each
// takes in the repo. The old on-box script staged exactly these two.
var sessionSources = []struct{ remote, name string }{
	{"/mnt/work/state/claude/projects", "claude-projects"},
	{"/mnt/work/state/codex/sessions", "codex-sessions"},
}

// sessionExcludes never leave the box. The transcript dirs sit next to
// credential files, and one stray copy would put a token in a git history that
// is permanent. Belt and braces: the paths above already exclude the top-level
// credential files by being subdirectories.
var sessionExcludes = []string{"*.credentials.json", "auth.json", "*token*", "*.pem", "*.key"}

var sessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "Agent transcript history (collect from a box into the sessions repo)",
}

var sessionsCollectCmd = &cobra.Command{
	Use:   "collect [box]",
	Short: "Pull a box's agent transcripts and push them to the sessions repo",
	Long: `Copy the agent transcripts off a box's volume and commit them to the private
sessions repo named in megh.yaml.

Transcripts are the only thing on the volume that nothing else holds: code is in
git, dotfiles are in git, caches regenerate, and the tool logins under state/
expire or are re-minted in a command.

The box never holds a credential for that repo. This pulls over the SSH megh
already uses and pushes with the GitHub identity in the active profile, which is
why the on-box timer that used to do this was removed.

Files are mirrored, so a transcript deleted on the box is deleted in the repo.
Credential-shaped files are excluded on the way out.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if sessionsProvider != "runpod" {
			return fmt.Errorf("provider %q not implemented yet", sessionsProvider)
		}
		if cfg.Sessions.Repo == "" {
			return fmt.Errorf("no sessions repo configured (set sessions.repo in megh.yaml)")
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
		box := pod.DisplayName()
		d := dialFor(pod)

		clone, err := sessionsClone(cfg.Sessions.Repo)
		if err != nil {
			return err
		}

		var pulled int
		for _, src := range sessionSources {
			dest := filepath.Join(clone, box, src.name)
			n, err := pullDir(d, d.keyFor(cfg.SSHKeyFile), src.remote, dest)
			if err != nil {
				return fmt.Errorf("pull %s: %w", src.remote, err)
			}
			if n == 0 {
				fmt.Printf("  %-16s nothing on the box\n", src.name)
				continue
			}
			fmt.Printf("  %-16s %d file(s)\n", src.name, n)
			pulled += n
		}
		if pulled == 0 {
			fmt.Println("no transcripts to collect")
			return nil
		}
		if sessionsDryRun {
			fmt.Printf("\ndry run: staged in %s, not committed\n", clone)
			return nil
		}
		return commitSessions(clone, box, pulled, sessionsNoPush)
	},
}

// sessionsClone returns a local clone of the sessions repo, creating it on first
// use. It lives under the profile store rather than in repos/, because it is
// megh's own bookkeeping and not something you work in.
func sessionsClone(repo string) (string, error) {
	dir := filepath.Join(profile.Root(), "sessions", strings.ReplaceAll(repo, "/", "-"))
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return dir, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return "", err
	}
	url := "git@github.com:" + repo + ".git"
	fmt.Printf("megh: first run, cloning %s\n", repo)
	c := exec.Command("git", "clone", "--quiet", url, dir)
	c.Env = append(os.Environ(), gitSSHEnv())
	if out, err := c.CombinedOutput(); err != nil {
		// An empty repo cannot be cloned, which is the normal state the first
		// time. Start one locally and let the first push create the branch.
		if strings.Contains(string(out), "empty repository") {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return "", err
			}
			for _, a := range [][]string{{"init", "--quiet", "-b", "main"}, {"remote", "add", "origin", url}} {
				g := exec.Command("git", append([]string{"-C", dir}, a...)...)
				if o, err := g.CombinedOutput(); err != nil {
					return "", fmt.Errorf("git %s: %s", a[0], strings.TrimSpace(string(o)))
				}
			}
			return dir, nil
		}
		return "", fmt.Errorf("clone %s: %s", repo, strings.TrimSpace(string(out)))
	}
	return dir, nil
}

// gitSSHEnv makes git authenticate with the profile's GitHub key rather than
// whatever the ambient agent holds, which on this machine is a different
// account entirely.
func gitSSHEnv() string {
	cmd := "ssh -o StrictHostKeyChecking=accept-new"
	if p := activeProfile; p != nil {
		if keys := p.GHKeyFiles(); len(keys) > 0 {
			cmd += " -o IdentitiesOnly=yes -i " + keys[0]
		}
	}
	return "GIT_SSH_COMMAND=" + cmd
}

// sessionTarCmd is the command run on the box: stream one directory as a tar,
// minus anything credential-shaped, and exit 3 rather than fail if the directory
// is simply not there (a box may never have run codex).
func sessionTarCmd(remote string) string {
	var ex strings.Builder
	for _, p := range sessionExcludes {
		fmt.Fprintf(&ex, " --exclude=%s", shQuote(p))
	}
	return fmt.Sprintf("[ -d %s ] || exit 3; tar czf -%s -C %s .",
		shQuote(remote), ex.String(), shQuote(remote))
}

// pullDir streams one remote directory into dest and returns the file count.
//
// tar over ssh rather than rsync: rsync would be incremental, but it is not
// installed by default in Termux, and the control machine is becoming a phone.
// Transcripts are small and git dedupes on commit, so a full transfer each time
// costs little and keeps the dependency list to ssh and tar.
func pullDir(d dial, boxKey, remote, dest string) (int, error) {
	remoteCmd := sessionTarCmd(remote)

	args := []string{"-o", "StrictHostKeyChecking=accept-new", "-o", "BatchMode=yes"}
	if boxKey != "" {
		args = append(args, "-i", config.ExpandPath(boxKey), "-o", "IdentitiesOnly=yes")
	}
	if d.port != 0 {
		args = append(args, "-p", strconv.Itoa(d.port))
	}
	args = append(args, d.userHost(), remoteCmd)

	var buf bytes.Buffer
	ssh := exec.Command("ssh", args...)
	ssh.Stdout = &buf
	ssh.Stderr = os.Stderr
	if err := ssh.Run(); err != nil {
		if ec, ok := err.(*exec.ExitError); ok && ec.ExitCode() == 3 {
			return 0, nil // directory absent on the box
		}
		return 0, err
	}
	if buf.Len() == 0 {
		return 0, nil
	}
	// Mirror: drop what was there so a transcript deleted on the box goes too.
	if err := os.RemoveAll(dest); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return 0, err
	}
	untar := exec.Command("tar", "xzf", "-", "-C", dest)
	untar.Stdin = &buf
	untar.Stderr = os.Stderr
	if err := untar.Run(); err != nil {
		return 0, err
	}
	n := 0
	_ = filepath.Walk(dest, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			n++
		}
		return nil
	})
	return n, nil
}

func commitSessions(clone, box string, n int, noPush bool) error {
	run := func(a ...string) (string, error) {
		c := exec.Command("git", append([]string{"-C", clone}, a...)...)
		c.Env = append(os.Environ(), gitSSHEnv())
		out, err := c.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := run("add", "-A"); err != nil {
		return err
	}
	if _, err := run("diff", "--cached", "--quiet"); err == nil {
		fmt.Println("no change since the last collection")
		return nil
	}
	msg := fmt.Sprintf("sessions: %s, %d file(s), %s", box, n, time.Now().UTC().Format(time.RFC3339))
	if out, err := run("-c", "user.name=megh", "-c", "user.email=megh@localhost", "commit", "-q", "-m", msg); err != nil {
		return fmt.Errorf("commit: %s", out)
	}
	if noPush {
		fmt.Printf("committed locally in %s (--no-push)\n", clone)
		return nil
	}
	if out, err := run("push", "-q", "origin", "HEAD:main"); err != nil {
		return fmt.Errorf("push: %s", out)
	}
	fmt.Printf("pushed %d transcript file(s) for %s to %s\n", n, box, cfg.Sessions.Repo)
	return nil
}

func init() {
	f := sessionsCollectCmd.Flags()
	f.StringVar(&sessionsProvider, "provider", "runpod", "provider (runpod)")
	f.BoolVar(&sessionsDryRun, "dry-run", false, "stage the files locally and stop")
	f.BoolVar(&sessionsNoPush, "no-push", false, "commit locally but do not push")
	sessionsCmd.AddCommand(sessionsCollectCmd)
	rootCmd.AddCommand(sessionsCmd)
}
