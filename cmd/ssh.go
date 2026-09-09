package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/panyam/megh/internal/providers"
	"github.com/spf13/cobra"
)

var (
	sshProvider string
	sshNoTmux   bool
	sshSession  string
	sshCC       bool
	sshNoCC     bool
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
// That is what makes `megh ssh` its own reattach command: detach, run it again,
// and you are back in the same session. Working in a plain shell is the failure
// this removes: the shell dies with the connection and takes any running work
// with it, which on a flaky mobile link is a matter of when rather than whether.
//
// controlMode adds -CC, which switches tmux from drawing its own interface to
// REPORTING its structure over a protocol the terminal emulator renders. iTerm2
// speaks it and turns tmux windows into native tabs, with native scrollback,
// find and copy. The session is an ordinary tmux session either way, so a box
// can be attached in control mode from a laptop and normally from a phone.
//
// Falls back to a login shell if tmux is somehow missing, because failing to
// find tmux should not mean failing to get a shell.
func tmuxAttachCmd(session string, controlMode bool) string {
	flags := ""
	if controlMode {
		flags = "-CC "
	}
	q := shQuote(session)
	return "if command -v tmux >/dev/null 2>&1; then exec tmux " + flags + "new -A -s " + q +
		"; else echo 'megh: tmux not found, plain shell' >&2; exec \"$SHELL\" -l; fi"
}

// resolveControlMode decides whether to attach in tmux control mode:
// --cc / --no-cc, then $MEGH_SSH_CC, then off.
//
// Control mode is a property of the TERMINAL YOU ARE SITTING AT, not of the box
// or the project, which is why it is an environment variable and deliberately
// NOT a megh.yaml key. megh.yaml lives in a dotfiles repo and is installed on
// every control device including the phone, so a setting there would follow you
// onto Termux, which cannot render control mode at all.
//
// It is also why control mode is OFF by default rather than on. The two failure
// directions are not symmetric. Forgetting --cc in iTerm2 costs you native tabs
// and nothing else: tmux works normally. Getting control mode in a terminal that
// does not speak it costs you the shell entirely — tmux reads stdin as CONTROL
// COMMANDS, so typing `whoami` answers "parse error: unknown command", and `ls`
// silently runs tmux's list-sessions instead of the shell's ls, which looks like
// it half-works. A default has to fail in the graceful direction, so the machine
// with the capable terminal opts in:
//
//	export MEGH_SSH_CC=1   # in the Mac's shell config, never in megh.yaml
func resolveControlMode(cmd *cobra.Command) (bool, error) {
	if cmd.Flags().Changed("cc") && cmd.Flags().Changed("no-cc") {
		return false, fmt.Errorf("--cc and --no-cc are opposites; pass one")
	}
	if cmd.Flags().Changed("cc") {
		return sshCC, nil
	}
	if cmd.Flags().Changed("no-cc") {
		return !sshNoCC, nil
	}
	v := strings.TrimSpace(os.Getenv("MEGH_SSH_CC"))
	switch strings.ToLower(v) {
	case "", "0", "false", "no", "off":
		return false, nil
	case "1", "true", "yes", "on":
		return true, nil
	}
	// A typo here would silently drop you back to a normal attach, and the whole
	// point of the variable is that you stop thinking about it.
	return false, fmt.Errorf("MEGH_SSH_CC=%q is not a boolean (use 1/true/yes/on or 0/false/no/off)", v)
}

var sshCmd = &cobra.Command{
	Use:   "ssh [box-name-or-id]",
	Short: "Open an interactive shell on a box (git-ready via forwarded keys)",
	Long: `SSH into a box with the profile's box key, forwarding the profile's GitHub
identity keys (private keys never touch the box) and configuring per-identity
Host aliases so git works.

Attaches the tmux session 'main', the same one webterm and ttyd serve, so work
survives a disconnect and the desktop and phone share one session with no id to
carry between them. Because it attaches-or-creates, this IS the reattach command:
detach with ctrl-b d, run 'megh ssh' again, and you are back where you were.
--no-tmux gives a plain shell. Pick another session with --session, or
MEGH_TMUX=<name> (MEGH_TMUX_SESSION also works).

--cc attaches in tmux CONTROL MODE, which iTerm2 renders as native tabs with
native scrollback, find and copy. Same session either way, so a box can be in
control mode on a laptop and a normal attach on a phone at the same time.

Set MEGH_SSH_CC=1 on a machine whose terminal speaks the protocol and it becomes
that machine's default; --no-cc overrides it for one connection. Keep it in the
shell config and out of megh.yaml, which is shared with devices (Termux) that
cannot render control mode: there, tmux would read your keystrokes as tmux
COMMANDS rather than shell input, so you would get no shell at all.

For browser access to the box's web surfaces, use 'megh browse' (localhost
tunnels) or Tailscale.

If the box exposes public SSH it connects over its IP; otherwise it connects to
the box's Tailscale MagicDNS name (requires this machine on the tailnet). With no
argument it connects to the only box.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		prov, err := resolveProvider(cmd, sshProvider)
		if err != nil {
			return err
		}
		controlMode, err := resolveControlMode(cmd)
		if err != nil {
			return err
		}
		if sshNoTmux && controlMode && cmd.Flags().Changed("cc") {
			return fmt.Errorf("--cc and --no-tmux are opposites: --cc attaches tmux in control mode, --no-tmux attaches no tmux at all")
		}
		return connectToBox(context.Background(), prov, args, connectOpts{
			session:     resolveTmuxSession(sshSession),
			controlMode: controlMode,
			noTmux:      sshNoTmux,
		})
	},
}

// connectOpts is what a connect needs once the flags are resolved. It exists so
// `megh ssh` and `megh tmux attach` share one implementation: the second is the
// first with the session named positionally, and duplicating fifty lines of key
// forwarding and file pushing to say that would guarantee they drift.
type connectOpts struct {
	session     string
	controlMode bool
	noTmux      bool
}

// connectToBox resolves the box, sets up git identity forwarding, pushes the
// megh.yaml `files:`, and execs ssh.
func connectToBox(ctx context.Context, prov providers.Provider, args []string, o connectOpts) error {
	pod, err := providers.FindOrSole(ctx, prov, args)
	if err != nil {
		return err
	}

	pod = awaitSSHReady(ctx, prov, pod)
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

	if o.noTmux {
		sshArgs := append(d.opts("-A"), d.userHost())
		fmt.Fprintf(os.Stderr, "megh: ssh %s (plain shell; browser access: megh browse)\n", d.userHost())
		return runSSH(d.keyFor(cfg.SSHKeyFile), fwdKeys, sshArgs, nil)
	}
	if err := validTmuxSession(o.session); err != nil {
		return err
	}
	// -t forces a TTY: without it a remote command gets none and tmux refuses
	// to start.
	sshArgs := append(d.opts("-A", "-t"), d.userHost(), tmuxAttachCmd(o.session, o.controlMode))
	if o.controlMode {
		fmt.Fprintf(os.Stderr, "megh: ssh %s (tmux %q in control mode; close the window to detach)\n",
			d.userHost(), o.session)
	} else {
		fmt.Fprintf(os.Stderr, "megh: ssh %s (tmux %q; detach with ctrl-b d, --no-tmux for a plain shell)\n",
			d.userHost(), o.session)
	}
	return runSSH(d.keyFor(cfg.SSHKeyFile), fwdKeys, sshArgs, nil)
}

func init() {
	sshCmd.Flags().StringVar(&sshProvider, "provider", "", "provider (default: config default_provider, else runpod)")
	sshCmd.Flags().BoolVar(&sshNoTmux, "no-tmux", false, "plain shell instead of attaching tmux")
	sshCmd.Flags().StringVar(&sshSession, "session", "", "tmux session to attach (default: $MEGH_TMUX, else main)")
	sshCmd.Flags().BoolVar(&sshCC, "cc", false, "attach in tmux control mode (iTerm2 renders tmux windows as native tabs)")
	sshCmd.Flags().BoolVar(&sshNoCC, "no-cc", false, "force a normal attach, overriding $MEGH_SSH_CC")
	rootCmd.AddCommand(sshCmd)
}
