package cmd

import (
	"os"
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

// Precedence, and the reason TMUX is handled last and carefully.
func TestResolveTmuxSession(t *testing.T) {
	clear := func(t *testing.T) {
		t.Helper()
		for _, v := range []string{"MEGH_TMUX", "MEGH_TMUX_SESSION", "TMUX"} {
			t.Setenv(v, "")
		}
	}

	t.Run("defaults to main", func(t *testing.T) {
		clear(t)
		if got := resolveTmuxSession(""); got != "main" {
			t.Errorf("got %q, want main", got)
		}
	})

	t.Run("MEGH_TMUX is honoured", func(t *testing.T) {
		clear(t)
		t.Setenv("MEGH_TMUX", "agni")
		if got := resolveTmuxSession(""); got != "agni" {
			t.Errorf("got %q, want agni", got)
		}
	})

	t.Run("the flag beats the environment", func(t *testing.T) {
		clear(t)
		t.Setenv("MEGH_TMUX", "fromenv")
		if got := resolveTmuxSession("fromflag"); got != "fromflag" {
			t.Errorf("got %q, want fromflag", got)
		}
	})

	t.Run("MEGH_TMUX beats the older MEGH_TMUX_SESSION", func(t *testing.T) {
		clear(t)
		t.Setenv("MEGH_TMUX", "new")
		t.Setenv("MEGH_TMUX_SESSION", "old")
		if got := resolveTmuxSession(""); got != "new" {
			t.Errorf("got %q, want new", got)
		}
	})

	t.Run("a plain TMUX value is honoured", func(t *testing.T) {
		clear(t)
		t.Setenv("TMUX", "work")
		if got := resolveTmuxSession(""); got != "work" {
			t.Errorf("got %q, want work", got)
		}
	})

	// tmux exports TMUX itself inside a session. Reading that as a session name
	// is the trap this guard exists for.
	t.Run("tmux's own TMUX value is ignored", func(t *testing.T) {
		for _, v := range []string{
			"/private/tmp/tmux-501/default,4242,0",
			"/tmp/tmux-1000/default,987,2",
		} {
			clear(t)
			t.Setenv("TMUX", v)
			if got := resolveTmuxSession(""); got != "main" {
				t.Errorf("TMUX=%q leaked through as %q; want the default", v, got)
			}
		}
	})
}

// tmux rejects these itself, so catch them before the remote command does and
// fails with nothing useful.
func TestValidTmuxSession(t *testing.T) {
	for _, bad := range []string{"", "has:colon", "has.dot", "has space"} {
		if err := validTmuxSession(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	for _, ok := range []string{"main", "agni", "work-1", "feat_x"} {
		if err := validTmuxSession(ok); err != nil {
			t.Errorf("%q should be accepted: %v", ok, err)
		}
	}
}

// CONSTRAINTS C3. megh already refuses to SEND a provider credential to a box;
// this covers the other half, where someone puts one there by hand and the
// property quietly stops holding. The marker is written into the image, so its
// presence is the signal.
func TestRefuseToSpawnFromABox(t *testing.T) {
	// On a laptop there is no marker, so up must proceed.
	if _, err := os.Stat(boxMarker); err == nil {
		t.Skip("running on a megh box; this test assumes a control machine")
	}
	upFromBox = false
	if err := refuseToSpawnFromABox(); err != nil {
		t.Errorf("should not refuse off a box: %v", err)
	}
	// The override exists so an intentionally elevated box still works.
	upFromBox = true
	defer func() { upFromBox = false }()
	if err := refuseToSpawnFromABox(); err != nil {
		t.Errorf("--i-am-the-control-plane should allow it: %v", err)
	}
}

// The default gh login lacks admin:public_key, so this is the first thing
// anyone enrolling a key from a new device will hit. gh's own message does not
// name the scope, so misclassifying it means the user gets a bare 403.
func TestIsGHScopeError(t *testing.T) {
	// Observed verbatim from gh against a token without the scope.
	real := "HTTP 403: Resource not accessible by personal access token (https://api.github.com/user/keys)"
	if !isGHScopeError(real) {
		t.Errorf("the real-world 403 must be recognised: %q", real)
	}
	for _, s := range []string{
		"error: missing required scope 'admin:public_key'",
		"HTTP 403",
	} {
		if !isGHScopeError(s) {
			t.Errorf("should be recognised: %q", s)
		}
	}
	for _, s := range []string{
		"key is already in use",
		"HTTP 422: Validation Failed",
		"could not connect to github.com",
	} {
		if isGHScopeError(s) {
			t.Errorf("should NOT be treated as a scope problem: %q", s)
		}
	}
}
