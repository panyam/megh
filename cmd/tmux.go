package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/panyam/megh/internal/providers"
	"github.com/spf13/cobra"
)

var tmuxProvider string

// tmuxLsScript asks the box what tmux sessions exist and what is in them.
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
tmux list-sessions -F 'S	#{session_name}	#{session_windows}	#{?session_attached,attached,detached}	#{t:session_created}'
tmux list-windows -a -F 'W	#{session_name}	#{window_index}	#{window_name}	#{?window_active,*, }	#{pane_current_command}'
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
	Short:   "List the tmux sessions on a box, and the windows in them",
	Long: `List a box's tmux sessions, how many windows each holds, whether anything is
attached, and what each window is running.

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

	type session struct{ name, windows, clients, created string }
	var sessions []session
	windows := map[string][]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		switch {
		case f[0] == "S" && len(f) >= 5:
			sessions = append(sessions, session{f[1], f[2], f[3], f[4]})
		case f[0] == "W" && len(f) >= 6:
			windows[f[1]] = append(windows[f[1]], fmt.Sprintf("   %s%s: %s (%s)", f[4], f[2], f[3], f[5]))
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

	var b strings.Builder
	fmt.Fprintf(&b, "%-*s  %-*s  %-*s  %s\n", nameW, "SESSION", winW, "WINDOWS", cliW, "CLIENTS", "CREATED")
	for _, s := range sessions {
		fmt.Fprintf(&b, "%-*s  %-*s  %-*s  %s\n", nameW, s.name, winW, s.windows, cliW, s.clients, s.created)
		for _, w := range windows[s.name] {
			b.WriteString(w + "\n")
		}
	}
	fmt.Fprintf(&b, "\n* is the window a client last had focused. Attach with `megh ssh %s`,\n"+
		"or a named one with `--session <name>`.\n", box)
	return b.String()
}

func init() {
	tmuxLsCmd.Flags().StringVar(&tmuxProvider, "provider", "", "provider (default: config default_provider, else runpod)")
	tmuxCmd.AddCommand(tmuxLsCmd)
	rootCmd.AddCommand(tmuxCmd)
}
