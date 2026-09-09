package cmd

import (
	"strings"
	"testing"
)

const probeOut = "S\tmain\t2\tattached\tWed Sep  9 18:36:22 2026\n" +
	"S\tdesk\t1\tdetached\tWed Sep  9 18:50:25 2026\n" +
	"W\tmain\t1\teditor\t \tvim\n" +
	"W\tmain\t2\tbuild\t*\tgo\n" +
	"W\tdesk\t1\tzsh\t*\tzsh\n"

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
	out := "S\t3\t1\tdetached\tWed Sep  9 18:58:34 2026\n" +
		"S\tmain\t2\tattached\tWed Sep  9 18:55:21 2026\n" +
		"W\t3\t1\tzsh\t*\tzsh\n" +
		"W\tmain\t2\tzsh\t*\tzsh\n" +
		"W\tmain\t3\tzsh\t \tzsh\n"
	got := renderTmuxLs(out, "box1")
	n := 0
	for _, line := range strings.Split(got, "\n") {
		if !strings.Contains(line, "└") {
			continue
		}
		n++
		if !strings.Contains(line, "window ") {
			t.Errorf("window line does not say 'window': %q", line)
		}
	}
	if n != 3 {
		t.Errorf("want 3 window lines, got %d:\n%s", n, got)
	}
	// The session NAMED "3" must not be mistakable for the window numbered 3.
	if !strings.Contains(got, "\n3        1  ") {
		t.Errorf("the session named 3 should head its own row:\n%s", got)
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
