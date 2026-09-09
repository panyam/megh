package cmd

import (
	"strings"
	"testing"
)

// The probe reports three levels plus a header line carrying the box's home and
// hostname, which are what let a pane's path collapse to ~ and a default
// pane_title (tmux seeds it with the hostname) be recognised as saying nothing.
const probeOut = "H\t/root\tbox1\n" +
	"S\tmain\t2\tattached\tWed Sep  9 18:36:22 2026\n" +
	"S\tdesk\t1\tdetached\tWed Sep  9 18:50:25 2026\n" +
	"W\tmain\t1\teditor\t \t1\n" +
	"W\tmain\t2\tbuild\t*\t2\n" +
	"W\tdesk\t1\tzsh\t*\t1\n" +
	"P\tmain\t1\t1\t*\tvim\t/root/repos/megh/cmd\tvim cmd/tmux.go\n" +
	"P\tmain\t2\t1\t \tgo\t/root/repos/megh\tbox1\n" +
	"P\tmain\t2\t2\t*\tzsh\t/root/repos/megh\tzsh\n" +
	"P\tdesk\t1\t1\t*\tzsh\t/root\tbox1\n"

// A box nobody has attached yet has no sessions, and tmux says so by exiting
// non-zero. That is the ANSWER to this question, not a failure, so it reads as
// an empty list and points at the command that would start one.
func TestRenderTmuxLsEmptyCases(t *testing.T) {
	for _, tc := range []struct{ out, want string }{
		{"MEGH_NO_SESSIONS\n", "no tmux sessions"},
		{"MEGH_NO_TMUX\n", "no tmux installed"},
		{"", "no tmux sessions"},
	} {
		got := renderTmuxLs(tc.out, "box1")
		if !strings.Contains(got, tc.want) {
			t.Errorf("renderTmuxLs(%q) = %q, want it to mention %q", tc.out, got, tc.want)
		}
	}
	if !strings.Contains(renderTmuxLs("MEGH_NO_SESSIONS\n", "box1"), "megh ssh box1") {
		t.Error("the empty case should name the command that starts a session")
	}
}

func TestRenderTmuxLsGroupsWindowsUnderSessions(t *testing.T) {
	got := renderTmuxLs(probeOut, "box1")
	for _, want := range []string{"main", "desk", "editor", "build", "attached", "detached"} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q:\n%s", want, got)
		}
	}
	lines := strings.Split(got, "\n")
	var mainAt, buildAt, deskAt int
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "main"):
			mainAt = i
		case strings.Contains(l, "build"):
			buildAt = i
		case strings.HasPrefix(l, "desk"):
			deskAt = i
		}
	}
	if !(mainAt < buildAt && buildAt < deskAt) {
		t.Errorf("main's windows must sit under main and above desk; got main=%d build=%d desk=%d\n%s",
			mainAt, buildAt, deskAt, got)
	}
}

// The window lines carry no tabs, and a tab-less line terminates a text/tabwriter
// column block, so an interleaved layout built on tabwriter misaligns every
// session row after the first. This asserts the columns actually line up.
func TestRenderTmuxLsColumnsAlign(t *testing.T) {
	got := renderTmuxLs(probeOut, "box1")
	var header, main, desk string
	for _, l := range strings.Split(got, "\n") {
		switch {
		case strings.HasPrefix(l, "SESSION"):
			header = l
		case strings.HasPrefix(l, "main"):
			main = l
		case strings.HasPrefix(l, "desk"):
			desk = l
		}
	}
	at := func(l, col string) int { return strings.Index(l, col) }
	if a, b := at(header, "WINDOWS"), at(main, "2"); a != b {
		t.Errorf("main's window count starts at %d, header column at %d:\n%s", b, a, got)
	}
	if a, b := at(main, "attached"), at(desk, "detached"); a != b {
		t.Errorf("CLIENTS column misaligned between rows (%d vs %d):\n%s", a, b, got)
	}
}

// tmux auto-names a session created without -s, so a box really can hold a
// session called "3" sitting next to a WINDOWS column and window indices that
// are also small numbers. The first version of this output rendered that as an
// unreadable pile of digits, and a reader could not tell which number was a
// session and which was a window. Every window line must say so in words.
func TestRenderTmuxLsDisambiguatesANumericSessionName(t *testing.T) {
	out := "H\t/root\tbox1\n" +
		"S\t3\t1\tdetached\tWed Sep  9 18:58:34 2026\n" +
		"S\tmain\t2\tattached\tWed Sep  9 18:55:21 2026\n" +
		"W\t3\t1\tzsh\t*\t1\n" +
		"W\tmain\t2\tzsh\t*\t1\n" +
		"W\tmain\t3\tzsh\t \t1\n" +
		"P\t3\t1\t1\t*\tzsh\t/root\tbox1\n" +
		"P\tmain\t2\t1\t*\tzsh\t/root\tbox1\n" +
		"P\tmain\t3\t1\t \tzsh\t/root\tbox1\n"
	got := renderTmuxLs(out, "box1")
	windows := 0
	for _, line := range strings.Split(got, "\n") {
		if !strings.Contains(line, "└") {
			continue
		}
		if strings.Contains(line, "window ") {
			windows++
			continue
		}
		if !strings.Contains(line, "pane ") {
			t.Errorf("nested line says neither 'window' nor 'pane': %q", line)
		}
	}
	if windows != 3 {
		t.Errorf("want 3 window lines, got %d:\n%s", windows, got)
	}
	// The session NAMED "3" must not be mistakable for the window numbered 3.
	if !strings.Contains(got, "\n3        1  ") {
		t.Errorf("the session named 3 should head its own row:\n%s", got)
	}
}

// A window line can only ever describe its ACTIVE pane, so a window holding a
// build beside an editor read as a window running whichever was focused. Every
// pane gets its own line, under the window that holds it.
func TestRenderTmuxLsListsPanesUnderWindows(t *testing.T) {
	got := renderTmuxLs(probeOut, "box1")
	lines := strings.Split(got, "\n")
	at := func(want string) int {
		for i, l := range lines {
			if strings.Contains(l, want) {
				return i
			}
		}
		t.Fatalf("output is missing %q:\n%s", want, got)
		return -1
	}
	build, goPane, zshPane := at("window 2: build"), at("pane 1: go"), at("pane 2: zsh")
	if !(build < goPane && goPane < zshPane) {
		t.Errorf("both of build's panes must sit under it in order: %d %d %d\n%s", build, goPane, zshPane, got)
	}
	if desk := at("desk"); desk < zshPane {
		t.Errorf("main's panes must sit above the next session:\n%s", got)
	}
	// The pane count belongs only on a window that has more than one; "(1 pane)"
	// on every other line is noise.
	if !strings.Contains(lines[build], "(2 panes)") {
		t.Errorf("a multi-pane window should say how many: %q", lines[build])
	}
	if strings.Contains(lines[at("window 1: editor")], "pane") {
		t.Errorf("a single-pane window should not carry a count: %q", lines[at("window 1: editor")])
	}
	// * marks focus at both levels, and a window's inactive pane must not wear it.
	if !strings.HasSuffix(lines[zshPane], "*") || strings.HasSuffix(lines[goPane], "*") {
		t.Errorf("focus marks are wrong:\n  %q\n  %q", lines[zshPane], lines[goPane])
	}
}

// A pane is worth listing for WHERE it is as much as what it runs, and the paths
// are long: they collapse against the box's own home, not an assumed /root.
func TestRenderTmuxLsShortensPanePathsAgainstTheBoxHome(t *testing.T) {
	got := renderTmuxLs(probeOut, "box1")
	if !strings.Contains(got, "~/repos/megh") {
		t.Errorf("pane paths should collapse to ~:\n%s", got)
	}
	if strings.Contains(got, "/root/repos") {
		t.Errorf("a shortened path should not also appear in full:\n%s", got)
	}
	for _, tc := range []struct{ path, home, want string }{
		{"/root", "/root", "~"},
		{"/root/x", "/root", "~/x"},
		{"/mnt/work", "/root", "/mnt/work"},
		{"/rootless/x", "/root", "/rootless/x"},
		{"/root/x", "", "/root/x"},
	} {
		if got := shortenHome(tc.path, tc.home); got != tc.want {
			t.Errorf("shortenHome(%q, %q) = %q, want %q", tc.path, tc.home, got, tc.want)
		}
	}
}

// tmux seeds pane_title with the box's hostname and most shells then set it to
// the command, so printing it unconditionally repeats one string down the whole
// listing. It earns a column only when a program set it to something else.
func TestRenderTmuxLsShowsOnlyInformativePaneTitles(t *testing.T) {
	got := renderTmuxLs(probeOut, "box1")
	if !strings.Contains(got, "vim cmd/tmux.go") {
		t.Errorf("a real pane title should be shown:\n%s", got)
	}
	for _, l := range strings.Split(got, "\n") {
		if strings.Contains(l, "pane ") && strings.Contains(l, "box1") {
			t.Errorf("the default hostname title should be dropped: %q", l)
		}
	}
	if strings.Contains(got, "zsh  ~/repos/megh  zsh") {
		t.Errorf("a title that just repeats the command should be dropped:\n%s", got)
	}
	if paneTitle("  ", "zsh", "box1") != "" {
		t.Error("a blank title should be dropped")
	}
}

// Control mode is the reason this listing gets read at all on a laptop, and
// iTerm2 renames both levels on screen: a tmux window becomes a tab and a pane
// becomes a split. Without the mapping the listing does not match what you see.
func TestRenderTmuxLsExplainsTheITerm2Mapping(t *testing.T) {
	got := renderTmuxLs(probeOut, "box1")
	for _, want := range []string{"TAB", "SPLIT", "ctrl-b o"} {
		if !strings.Contains(got, want) {
			t.Errorf("footer should mention %q:\n%s", want, got)
		}
	}
}

// The footer used to say `--session <name>`, which left the reader to work out
// that the SESSION column IS the name. That is exactly the inference that fails
// when a session is called "3", so it names a real one instead.
func TestRenderTmuxLsFooterNamesARealSession(t *testing.T) {
	got := renderTmuxLs(probeOut, "box1")
	if strings.Contains(got, "<name>") {
		t.Error("footer still uses a placeholder instead of a real session name")
	}
	if !strings.Contains(got, "megh tmux attach main box1") {
		t.Errorf("footer should show a runnable command:\n%s", got)
	}
	// With no session called main, it should still name one that exists.
	only := "S\tdesk\t1\tdetached\tnow\nW\tdesk\t1\tzsh\t*\tzsh\n"
	if g := renderTmuxLs(only, "box1"); !strings.Contains(g, "attach desk box1") {
		t.Errorf("footer should fall back to an existing session:\n%s", g)
	}
}

// `megh tmux attach` must be `megh ssh --session <name>` and not a second
// implementation of it. Both go through connectToBox, so this asserts the
// wiring: the subcommand exists, takes the session positionally, and carries the
// control-mode flags so MEGH_SSH_CC and --no-cc work there too.
func TestTmuxAttachIsWiredLikeSSH(t *testing.T) {
	if tmuxAttachSubCmd.Args == nil {
		t.Fatal("attach should constrain its args")
	}
	if err := tmuxAttachSubCmd.Args(tmuxAttachSubCmd, []string{}); err == nil {
		t.Error("attach with no session name should be rejected")
	}
	if err := tmuxAttachSubCmd.Args(tmuxAttachSubCmd, []string{"main", "box1"}); err != nil {
		t.Errorf("attach <session> <box> should be accepted: %v", err)
	}
	if err := tmuxAttachSubCmd.Args(tmuxAttachSubCmd, []string{"a", "b", "c"}); err == nil {
		t.Error("attach takes at most a session and a box")
	}
	for _, f := range []string{"cc", "no-cc", "provider"} {
		if tmuxAttachSubCmd.Flags().Lookup(f) == nil {
			t.Errorf("attach is missing --%s, so it would not behave like megh ssh", f)
		}
	}
}

// The ls footer should name the verb that attaches, not the longhand. It used to
// print `megh ssh <box> --session <name>`, which is the same thing said less
// directly now that attach exists.
func TestRenderTmuxLsFooterUsesTheAttachVerb(t *testing.T) {
	got := renderTmuxLs(probeOut, "box1")
	if !strings.Contains(got, "megh tmux attach main box1") {
		t.Errorf("footer should suggest the attach verb:\n%s", got)
	}
}
