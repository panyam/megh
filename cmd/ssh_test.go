package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// The remote command must attach an existing session rather than start a second
// one, or the desktop and the phone end up in different places.
func TestTmuxAttachReusesTheSession(t *testing.T) {
	got := tmuxAttachCmd("main", false)
	if !strings.Contains(got, "tmux new -A -s 'main'") {
		t.Errorf("want `tmux new -A -s` (attach-or-create), got: %s", got)
	}
	if !strings.Contains(got, "exec tmux") {
		t.Error("should exec tmux, so detaching ends the ssh session rather than dropping to a shell")
	}
}

// Missing tmux must not cost you a shell.
func TestTmuxAttachFallsBackToALoginShell(t *testing.T) {
	got := tmuxAttachCmd("main", false)
	if !strings.Contains(got, "command -v tmux") {
		t.Error("should check for tmux before exec'ing it")
	}
	if !strings.Contains(got, "$SHELL") {
		t.Errorf("should fall back to a login shell, got: %s", got)
	}
}

// Session names reach a remote shell, so they are quoted.
func TestTmuxAttachSessionNameIsQuoted(t *testing.T) {
	got := tmuxAttachCmd("we ird'; touch /tmp/pwned; #", false)
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
		t.Setenv("MEGH_TMUX", "work")
		if got := resolveTmuxSession(""); got != "work" {
			t.Errorf("got %q, want work", got)
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
	for _, ok := range []string{"main", "dev", "work-1", "feat_x"} {
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

// A missing pubkey used to yield "" and launch anyway, producing a running,
// billing pod with none of your keys in authorized_keys and no message saying so.
// The failure then surfaced minutes later as "Permission denied (publickey)",
// nowhere near its cause. With a profile active, root.go points SSHPubKeyFile at
// the profile's box.key.pub, so the error must fire ONLY when there is really no
// key to inject -- not on the working path.
func TestResolvePubKeyFailsLoudlyWhenThereIsNoKey(t *testing.T) {
	t.Setenv("MEGH_PUBKEY", "")
	cmd := &cobra.Command{}
	cmd.Flags().String("pubkey", "", "")

	missing := filepath.Join(t.TempDir(), "nope.pub")
	if _, err := resolvePubKey(cmd, "", missing); err == nil {
		t.Error("a missing key file must be an error, not an empty string and a launch")
	}

	real := filepath.Join(t.TempDir(), "id.pub")
	if err := os.WriteFile(real, []byte("ssh-ed25519 AAAAC3Nza test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := resolvePubKey(cmd, "", real)
	if err != nil {
		t.Errorf("an existing key file must resolve: %v", err)
	}
	if got != "ssh-ed25519 AAAAC3Nza test" {
		t.Errorf("key not read/trimmed correctly: %q", got)
	}

	// MEGH_PUBKEY wins over a missing file, so setting it is a real escape hatch.
	t.Setenv("MEGH_PUBKEY", "ssh-ed25519 AAAAfromenv env")
	if got, err := resolvePubKey(cmd, "", missing); err != nil || got != "ssh-ed25519 AAAAfromenv env" {
		t.Errorf("MEGH_PUBKEY should win: got %q, %v", got, err)
	}
}

// spawnAllowed is the guard's whole decision, and unlike TestRefuseToSpawnFromABox
// this runs everywhere -- including ON a box, which is the machine the rule
// actually governs and the one that test has to skip.
func TestSpawnAllowed(t *testing.T) {
	cases := []struct {
		name     string
		onABox   bool
		flag     bool
		declared string
		want     bool
	}{
		{"control machine, nothing set", false, false, "", true},
		{"box, nothing set", true, false, "", false},
		{"box, one-off flag", true, true, "", true},
		{"box, declared 1", true, false, "1", true},
		{"box, declared true", true, false, "TRUE", true},
		{"box, declared yes", true, false, " yes ", true},
		// "0" and "false" are the ones worth pinning: a truthiness check written
		// as "is it set" would read either as a declaration and elevate the box.
		{"box, declared 0", true, false, "0", false},
		{"box, declared false", true, false, "false", false},
		{"box, junk value", true, false, "maybe", false},
	}
	for _, c := range cases {
		if got := spawnAllowed(c.onABox, c.flag, c.declared); got != c.want {
			t.Errorf("%s: spawnAllowed(%v, %v, %q) = %v, want %v",
				c.name, c.onABox, c.flag, c.declared, got, c.want)
		}
	}
}

// C3. MEGH_CONTROL_PLANE says "this machine may spawn boxes". Forwarded, it says
// that to every box it reaches, which inverts the guard rather than opening it.
func TestMeghEnvNeverForwardsTheControlPlaneDeclaration(t *testing.T) {
	t.Setenv(controlPlaneEnv, "1")
	t.Setenv("MEGH_ORDINARY", "carried")
	out := string(meghEnv())
	if strings.Contains(out, controlPlaneEnv) {
		t.Errorf("%s must never reach a box; meghEnv produced:\n%s", controlPlaneEnv, out)
	}
	if !strings.Contains(out, "MEGH_ORDINARY") {
		t.Errorf("ordinary MEGH_ vars should still be forwarded; got:\n%s", out)
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

// Control mode is the SAME session reached differently: -CC is added and nothing
// else changes. That is what lets a laptop attach in control mode while a phone
// is attached normally, which is the arrangement `megh ssh --cc` is for.
func TestTmuxAttachControlModeAddsOnlyTheFlag(t *testing.T) {
	cc, plain := tmuxAttachCmd("desk", true), tmuxAttachCmd("desk", false)
	if !strings.Contains(cc, "tmux -CC new -A -s 'desk'") {
		t.Errorf("control-mode command = %q, want `tmux -CC new -A -s 'desk'`", cc)
	}
	if strings.Replace(cc, "-CC ", "", 1) != plain {
		t.Errorf("control mode changed more than the flag:\n cc:    %s\n plain: %s", cc, plain)
	}
}

// Control mode must stay opt-in. A raw %begin/%output stream in a terminal that
// does not speak the protocol is unreadable, and it would be the default for
// every phone and every ttyd client.
func TestTmuxAttachIsNotControlModeByDefault(t *testing.T) {
	if strings.Contains(tmuxAttachCmd("main", false), "-CC") {
		t.Error("a plain attach must carry no -CC")
	}
}

// Quoting still applies in control mode; the flag must not open a second path
// into the remote command.
func TestTmuxAttachQuotesTheSessionNameInControlMode(t *testing.T) {
	got := tmuxAttachCmd("a'b; rm -rf /", true)
	if !strings.Contains(got, `'a'\''b; rm -rf /'`) {
		t.Errorf("session name is not safely quoted: %s", got)
	}
}

// Control mode is a property of the terminal you are sitting at, so it is a
// per-machine env var with a per-connection override. The precedence is
// --cc/--no-cc, then $MEGH_SSH_CC, then off.
func TestResolveControlMode(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		args    []string
		want    bool
		wantErr bool
	}{
		{name: "off by default"},
		{name: "env on", env: "1", want: true},
		{name: "env on, spelled", env: "true", want: true},
		{name: "env on, cased", env: "YES", want: true},
		{name: "env off", env: "0"},
		{name: "flag beats unset env", args: []string{"--cc"}, want: true},
		{name: "flag beats env off", env: "0", args: []string{"--cc"}, want: true},
		{name: "no-cc beats env on", env: "1", args: []string{"--no-cc"}},
		{name: "both flags is an error", args: []string{"--cc", "--no-cc"}, wantErr: true},
		{name: "a typo is an error, not a silent off", env: "ture", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MEGH_SSH_CC", tc.env)
			sshCC, sshNoCC = false, false
			cmd := &cobra.Command{Use: "ssh", RunE: func(*cobra.Command, []string) error { return nil }}
			cmd.Flags().BoolVar(&sshCC, "cc", false, "")
			cmd.Flags().BoolVar(&sshNoCC, "no-cc", false, "")
			if err := cmd.Flags().Parse(tc.args); err != nil {
				t.Fatalf("parse %v: %v", tc.args, err)
			}
			got, err := resolveControlMode(cmd)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Errorf("control mode = %v, want %v", got, tc.want)
			}
		})
	}
}
