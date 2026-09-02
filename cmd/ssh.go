package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/panyam/megh/internal/providers/runpod"
	"github.com/spf13/cobra"
)

var (
	sshProvider string
	sshNoTmux   bool
	sshSession  string
)

// defaultTmuxSession is the session `megh ssh` attaches, and it matches the one
// webterm and ttyd serve. Sharing the name is the point: the desktop and the
// phone land in the same place with no session id to carry between them.
const defaultTmuxSession = "main"

// resolveTmuxSession picks the session name to attach: --session, then
// MEGH_TMUX, then MEGH_TMUX_SESSION, then TMUX, then "main".
//
// TMUX is accepted last and only when it does not look like tmux's own value.
// tmux EXPORTS TMUX inside every session, set to "<socket>,<pid>,<index>", so
// running `megh ssh` from inside a local tmux would otherwise try to attach a
// session named /tmp/tmux-501/default,4242,0 on the box. That is not a session
// name tmux will even accept, since names may not contain ':' or '.', so it
// fails in a way that points nowhere near the cause. A value containing a comma
// or starting with a slash is therefore tmux's, not yours, and is ignored.
func resolveTmuxSession(flag string) string {
	if flag != "" {
		return flag
	}
	for _, v := range []string{os.Getenv("MEGH_TMUX"), os.Getenv("MEGH_TMUX_SESSION")} {
		if v != "" {
			return v
		}
	}
	if v := os.Getenv("TMUX"); v != "" && !strings.ContainsAny(v, ",") && !strings.HasPrefix(v, "/") {
		return v
	}
	return defaultTmuxSession
}

// validTmuxSession rejects names tmux itself will not take, so the failure is a
// clear message here rather than a bare non-zero exit from the remote command.
func validTmuxSession(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("tmux session name is empty")
	case strings.ContainsAny(name, ":."):
		return fmt.Errorf("tmux session name %q cannot contain ':' or '.'", name)
	case strings.ContainsAny(name, "\n\r\t "):
		return fmt.Errorf("tmux session name %q cannot contain whitespace", name)
	}
	return nil
}

// tmuxAttachCmd is the remote command for an interactive session.
//
// `tmux new -A -s <name>` attaches the session if it exists and creates it
// otherwise, so it is the same command on the first connect and the hundredth.
// Working in a plain shell is the failure this removes: the shell dies with the
// connection and takes any running work with it, which on a flaky mobile link
// is a matter of when rather than whether.
//
// Falls back to a login shell if tmux is somehow missing, because failing to
// find tmux should not mean failing to get a shell.
func tmuxAttachCmd(session string) string {
	q := shQuote(session)
	return "if command -v tmux >/dev/null 2>&1; then exec tmux new -A -s " + q +
		"; else echo 'megh: tmux not found, plain shell' >&2; exec \"$SHELL\" -l; fi"
}

var sshCmd = &cobra.Command{
	Use:   "ssh [box-name-or-id]",
	Short: "Open an interactive shell on a box (git-ready via forwarded keys)",
	Long: `SSH into a box with the profile's box key, forwarding the profile's GitHub
identity keys (private keys never touch the box) and configuring per-identity
Host aliases so git works.

Attaches the tmux session 'main', the same one webterm and ttyd serve, so work
survives a disconnect and the desktop and phone share one session with no id to
carry between them. --no-tmux gives a plain shell. Pick another session with --session, or
MEGH_TMUX=<name> (MEGH_TMUX_SESSION also works).

For browser access to the box's web surfaces, use 'megh browse' (localhost
tunnels) or Tailscale.

If the box exposes public SSH it connects over its IP; otherwise it connects to
the box's Tailscale MagicDNS name (requires this machine on the tailnet). With no
argument it connects to the only box.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if sshProvider != "runpod" {
			return fmt.Errorf("provider %q not implemented yet", sshProvider)
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
		if d.tailnet() {
			fmt.Fprintf(os.Stderr, "megh: %q has no public SSH endpoint (still initializing, or tailnet-only). "+
				"Trying its tailnet name — this needs THIS machine on the tailnet; otherwise wait and retry `megh ssh`.\n",
				pod.DisplayName())
		}

		// Set up per-identity GitHub Host aliases on the box, and forward the
		// profile's GH keys so git works in the shell.
		var fwdKeys []string
		if activeProfile != nil {
			fwdKeys = activeProfile.GHKeyFiles()
			setup, serr := ghSetupScript(activeProfile)
			if serr != nil {
				return serr
			}
			if setup != "" {
				setupArgs := append(d.opts(), d.userHost(), "bash -s")
				if err := runSSH(d.keyFor(cfg.SSHKeyFile), nil, setupArgs, strings.NewReader(setup)); err != nil {
					fmt.Fprintf(os.Stderr, "megh: warning: gh key setup failed: %v\n", err)
				}
			}
		}

		// Copy any megh.yaml `files:` (secrets/rc files not in a repo) onto the box.
		if err := pushFiles(d, d.keyFor(cfg.SSHKeyFile), cfg.Files); err != nil {
			fmt.Fprintf(os.Stderr, "megh: warning: file copy failed: %v\n", err)
		}
		// Mirror megh.yaml `sync:` dirs. Best effort like the copy above: a
		// failed sync must not stop you getting onto the box.
		if err := pushSync(d, d.keyFor(cfg.SSHKeyFile), cfg.Sync); err != nil {
			fmt.Fprintf(os.Stderr, "megh: warning: sync failed: %v\n", err)
		}

		if sshNoTmux {
			sshArgs := append(d.opts("-A"), d.userHost())
			fmt.Fprintf(os.Stderr, "megh: ssh %s (plain shell; browser access: megh browse)\n", d.userHost())
			return runSSH(d.keyFor(cfg.SSHKeyFile), fwdKeys, sshArgs, nil)
		}
		// -t forces a TTY: without it a remote command gets none and tmux refuses
		// to start.
		session := resolveTmuxSession(sshSession)
		if err := validTmuxSession(session); err != nil {
			return err
		}
		sshArgs := append(d.opts("-A", "-t"), d.userHost(), tmuxAttachCmd(session))
		fmt.Fprintf(os.Stderr, "megh: ssh %s (tmux %q; detach with ctrl-b d, --no-tmux for a plain shell)\n",
			d.userHost(), session)
		return runSSH(d.keyFor(cfg.SSHKeyFile), fwdKeys, sshArgs, nil)
	},
}

func init() {
	sshCmd.Flags().StringVar(&sshProvider, "provider", "runpod", "provider (runpod)")
	sshCmd.Flags().BoolVar(&sshNoTmux, "no-tmux", false, "plain shell instead of attaching tmux")
	sshCmd.Flags().StringVar(&sshSession, "session", "", "tmux session to attach (default: $MEGH_TMUX, else main)")
	rootCmd.AddCommand(sshCmd)
}
