package cmd

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/panyam/megh/internal/providers"
	"github.com/spf13/cobra"
)

var tmuxProvider string

// tmuxLsScript asks the box what tmux sessions exist and what is in them, all
// three levels of it: sessions, the windows in them, and the panes in those.
//
// The pane level is a separate `list-panes -a` rather than more fields on the
// window line, because a window line can only ever describe its ACTIVE pane.
// A window running vim beside a build looked like a window running vim.
//
// It is deliberately READ-ONLY, which is the whole reason this command exists.
// `megh ssh` runs `tmux new -A -s <name>`, so it cannot answer "what is running
// on that box" without possibly CREATING the session it was asked about. There
// was no way to look without touching until this.
//
// tmux exits non-zero with "no server running" when nothing is up, which is a
// normal answer to this question rather than a failure, so it is caught here and
// reported as an empty list.
const tmuxLsScript = `
if ! command -v tmux >/dev/null 2>&1; then echo "MEGH_NO_TMUX"; exit 0; fi
if ! tmux list-sessions >/dev/null 2>&1; then echo "MEGH_NO_SESSIONS"; exit 0; fi
printf 'H\t%s\t%s\n' "${HOME}" "$(hostname)"
tmux list-sessions -F 'S	#{session_name}	#{session_windows}	#{?session_attached,attached,detached}	#{t:session_created}'
tmux list-windows -a -F 'W	#{session_name}	#{window_index}	#{window_name}	#{?window_active,*, }	#{window_panes}'
tmux list-panes -a -F 'P	#{session_name}	#{window_index}	#{pane_index}	#{?pane_active,*, }	#{pane_current_command}	#{pane_current_path}	#{pane_title}'
`

var tmuxCmd = &cobra.Command{
	Use:   "tmux",
	Short: "Inspect a box's tmux sessions",
	Long: `Look at the tmux sessions on a box without attaching to one.

This is read-only on purpose. 'megh ssh' attaches-or-creates, so it cannot tell
you what is already running without possibly creating a session; this can.`,
}

var tmuxLsCmd = &cobra.Command{
	Use:     "ls [box-name-or-id]",
	Aliases: []string{"list"},
	Short:   "List the tmux sessions on a box, and the windows and panes in them",
	Long: `List a box's tmux sessions, the windows in each, the panes in each window,
whether anything is attached, and what every pane is running and where.

Nothing is created or changed. With no argument it uses the only box.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		prov, err := resolveProvider(cmd, tmuxProvider)
		if err != nil {
			return err
		}
		ctx := context.Background()
		box, err := providers.FindOrSole(ctx, prov, args)
		if err != nil {
			return err
		}
		box = awaitSSHReady(ctx, prov, box)
		d := dialFor(box)
		out, err := sshCapture(d.keyFor(cfg.SSHKeyFile), d, tmuxLsScript)
		if err != nil {
			return err
		}
		fmt.Print(renderTmuxLs(out, box.DisplayName()))
		return nil
	},
}

// renderTmuxLs turns the probe's output into a table. Split from the SSH call so
// the formatting, including the two empty cases, is testable without a box.
//
// Widths are computed rather than delegated to text/tabwriter: the window lines
// are indented and carry no tabs, and a tab-less line TERMINATES a tabwriter
// column block, so interleaving them silently misaligns every session row after
// the first.
func renderTmuxLs(out, box string) string {
	switch {
	case strings.Contains(out, "MEGH_NO_TMUX"):
		return fmt.Sprintf("%s has no tmux installed\n", box)
	case strings.Contains(out, "MEGH_NO_SESSIONS"):
		// Not an error: a box nobody has connected to yet has no sessions, and
		// that is the answer rather than a failure.
		return fmt.Sprintf("%s has no tmux sessions (megh ssh %s starts one)\n", box, box)
	}

	type pane struct{ index, focus, cmd, path, title string }
	type window struct {
		index, name, focus, panes string
		list                      []pane
	}
	type session struct{ name, windows, clients, created string }

	var sessions []session
	var home, host string
	windows := map[string][]*window{}
	byIndex := map[string]*window{}
	focusOf := func(f string) string {
		if strings.TrimSpace(f) != "" {
			return "  *"
		}
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		switch {
		case f[0] == "H" && len(f) >= 3:
			home, host = f[1], f[2]
		case f[0] == "S" && len(f) >= 5:
			sessions = append(sessions, session{f[1], f[2], f[3], f[4]})
		case f[0] == "W" && len(f) >= 6:
			w := &window{index: f[2], name: f[3], focus: focusOf(f[4]), panes: f[5]}
			windows[f[1]] = append(windows[f[1]], w)
			byIndex[f[1]+"\t"+f[2]] = w
		case f[0] == "P" && len(f) >= 8:
			// Panes are listed after every window, so the window they hang off
			// exists by now. One that does not is dropped rather than invented.
			if w := byIndex[f[1]+"\t"+f[2]]; w != nil {
				w.list = append(w.list, pane{f[3], focusOf(f[4]), f[5], shortenHome(f[6], home), paneTitle(f[7], f[5], host)})
			}
		}
	}
	if len(sessions) == 0 {
		return fmt.Sprintf("%s has no tmux sessions (megh ssh %s starts one)\n", box, box)
	}

	nameW, winW, cliW := len("SESSION"), len("WINDOWS"), len("CLIENTS")
	for _, s := range sessions {
		nameW = max(nameW, len(s.name))
		winW = max(winW, len(s.windows))
		cliW = max(cliW, len(s.clients))
	}
	// Pane commands are padded to one width across the WHOLE listing, not per
	// window, so the paths form a single column the eye can run down.
	cmdW := 0
	for _, ws := range windows {
		for _, w := range ws {
			for _, p := range w.list {
				cmdW = max(cmdW, len(p.cmd))
			}
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%-*s  %-*s  %-*s  %s\n", nameW, "SESSION", winW, "WINDOWS", cliW, "CLIENTS", "CREATED")
	for _, s := range sessions {
		fmt.Fprintf(&b, "%-*s  %-*s  %-*s  %s\n", nameW, s.name, winW, s.windows, cliW, s.clients, s.created)
		for _, w := range windows[s.name] {
			// Spell out "window" and "pane" rather than leaning on indentation.
			// A session can be NAMED a number (tmux auto-names one created
			// without -s, so you get a session called "3"), and then a bare
			// indented "1:" next to a SESSION column reading "3" is unreadable:
			// nothing on screen says which number is which.
			count := ""
			if n, err := strconv.Atoi(w.panes); err == nil && n > 1 {
				count = fmt.Sprintf(" (%d panes)", n)
			}
			fmt.Fprintf(&b, "  └ window %s: %s%s%s\n", w.index, w.name, count, w.focus)
			for _, p := range w.list {
				fmt.Fprintf(&b, "      └ pane %s: %-*s  %s%s%s\n", p.index, cmdW, p.cmd, p.path, p.title, p.focus)
			}
		}
	}
	// A concrete example beats a placeholder. `--session <name>` left the reader
	// to work out that the SESSION column IS the name, which is exactly the step
	// that fails when a session is called "3".
	example := sessions[0].name
	for _, s := range sessions {
		if s.name == defaultTmuxSession {
			example = s.name
		}
	}
	fmt.Fprintf(&b, "\nAttach a session, then switch windows with ctrl-b <number> and panes with ctrl-b o:\n"+
		"  megh tmux attach %s %s\n", example, box)
	b.WriteString("* marks the window and the pane a client last had focused.\n")
	b.WriteString("In iTerm2 control mode (--cc) a window is a TAB and a pane is a SPLIT.\n")
	return b.String()
}

// shortenHome rewrites the box's home directory back to ~, which is how you
// would have typed the path. The home comes from the probe rather than being
// assumed to be /root, since the box user is not guaranteed.
func shortenHome(path, home string) string {
	switch {
	case home == "":
		return path
	case path == home:
		return "~"
	case strings.HasPrefix(path, home+"/"):
		return "~" + path[len(home):]
	}
	return path
}

// paneTitle decides whether a pane's title is worth a column.
//
// tmux seeds pane_title with the box's HOSTNAME and many shells then set it to
// the running command, so printing it unconditionally repeats the same string
// down the whole listing and says nothing. It earns its place only when a
// program has set it to something neither of those.
func paneTitle(title, cmd, host string) string {
	title = strings.TrimSpace(title)
	if title == "" || title == cmd || title == host {
		return ""
	}
	return "  " + title
}

// tmuxAttachSubCmd is `megh ssh --session <name>` with the session named
// positionally, which is how you say it out loud. It shares connectToBox with
// `megh ssh` rather than reimplementing the key forwarding and file push.
//
// The session is the FIRST argument and the box the optional second, which
// inverts `megh tmux ls [box]`. That is deliberate: attach's subject is the
// session, and the box is nearly always the only one you have.
var tmuxAttachSubCmd = &cobra.Command{
	Use:     "attach <session> [box-name-or-id]",
	Aliases: []string{"a"},
	Short:   "Attach a named tmux session on a box",
	Long: `Attach the named tmux session, creating it if it does not exist.

There is no separate "create": tmux attaches-or-creates, so a name you have
never used starts a fresh session and a name from 'megh tmux ls' returns you to
what is running in it. Identical to 'megh ssh --session <name>'.

You attach a SESSION, not a window. A session holds N windows (which iTerm2
shows as tabs in control mode) and each window holds panes; once attached,
switch windows with ctrl-b <number>.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		prov, err := resolveProvider(cmd, tmuxProvider)
		if err != nil {
			return err
		}
		controlMode, err := resolveControlMode(cmd)
		if err != nil {
			return err
		}
		return connectToBox(context.Background(), prov, args[1:], connectOpts{
			session:     args[0],
			controlMode: controlMode,
		})
	},
}

func init() {
	tmuxLsCmd.Flags().StringVar(&tmuxProvider, "provider", "", "provider (default: config default_provider, else runpod)")
	tmuxAttachSubCmd.Flags().StringVar(&tmuxProvider, "provider", "", "provider (default: config default_provider, else runpod)")
	tmuxAttachSubCmd.Flags().BoolVar(&sshCC, "cc", false, "attach in tmux control mode (iTerm2 renders tmux windows as native tabs)")
	tmuxAttachSubCmd.Flags().BoolVar(&sshNoCC, "no-cc", false, "force a normal attach, overriding $MEGH_SSH_CC")
	tmuxCmd.AddCommand(tmuxLsCmd, tmuxAttachSubCmd)
	rootCmd.AddCommand(tmuxCmd)
}
