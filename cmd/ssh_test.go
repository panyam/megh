package cmd

import (
	"strings"
	"testing"
)

// The remote command must attach an existing session rather than start a second
// one, or the desktop and the phone end up in different places.
func TestTmuxAttachReusesTheSession(t *testing.T) {
	got := tmuxAttachCmd("main")
	if !strings.Contains(got, "tmux new -A -s 'main'") {
		t.Errorf("want `tmux new -A -s` (attach-or-create), got: %s", got)
	}
	if !strings.Contains(got, "exec tmux") {
		t.Error("should exec tmux, so detaching ends the ssh session rather than dropping to a shell")
	}
}

// Missing tmux must not cost you a shell.
func TestTmuxAttachFallsBackToALoginShell(t *testing.T) {
	got := tmuxAttachCmd("main")
	if !strings.Contains(got, "command -v tmux") {
		t.Error("should check for tmux before exec'ing it")
	}
	if !strings.Contains(got, "$SHELL") {
		t.Errorf("should fall back to a login shell, got: %s", got)
	}
}

// Session names reach a remote shell, so they are quoted.
func TestTmuxAttachSessionNameIsQuoted(t *testing.T) {
	got := tmuxAttachCmd("we ird'; touch /tmp/pwned; #")
	if strings.Contains(got, "touch /tmp/pwned") && !strings.Contains(got, `'\''`) {
		t.Errorf("session name must be shell-quoted, got: %s", got)
	}
	if !strings.HasPrefix(shQuote("main"), "'") {
		t.Error("shQuote should single-quote")
	}
}
