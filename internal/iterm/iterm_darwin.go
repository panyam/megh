//go:build darwin

package iterm

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/term"
)

// dynamicProfilesDir returns where iTerm2 reads Dynamic Profiles, overridable in tests.
func dynamicProfilesDir() string {
	if d := os.Getenv("MEGH_ITERM_DYNAMIC_PROFILES_DIR"); d != "" {
		return d
	}
	return filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "iTerm2", "DynamicProfiles")
}

// Available reports whether iTerm2 is installed.
func Available() bool {
	for _, app := range []string{"/Applications/iTerm.app", "/Applications/iTerm2.app"} {
		if _, err := os.Stat(app); err == nil {
			return true
		}
	}
	_, err := exec.LookPath("osascript")
	return err == nil
}

// InMeghProfile reports whether this process runs in an iTerm session using the named profile.
func InMeghProfile(name string) bool {
	if name == "" {
		name = ProfileName
	}
	return os.Getenv("ITERM_SESSION_ID") != "" && os.Getenv("ITERM_PROFILE") == name
}

func profileSpec(name string) map[string]any {
	// Keys match iTerm2 "Save Profile as JSON". Parent inherits colors/font; we set
	// session behavior that matters for cloud boxes.
	return map[string]any{
		"Name":                        name,
		"Guid":                        profileGUIDFor(name),
		"Dynamic Profile Parent Name": "Default",
		"Tags":                        []string{"megh"},
		"Custom Command":              "No",
		"Command":                     "",
		"Unlimited Scrollback":        true,
		"Scrollback Lines":            100000,
		// 0 = never close the tab/window when the shell exits (see iTerm enum).
		"Close Sessions On End": 0,
		// Clipboard via OSC 52 (pbcopy on the box).
		"Allow Terminal Apps to Access Clipboard": true,
		"Silence Bell": true,
	}
}

// TryDelegate opens this megh command in an iTerm tab/window using the configured profile.
// Returns true when the caller should exit (the session continues in iTerm).
func TryDelegate(argv []string, set Settings) bool {
	if !set.Auto {
		return false
	}
	if os.Getenv("MEGH_ITERM_REEXEC") == "1" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MEGH_ITERM"))) {
	case "0", "false", "no", "off":
		return false
	}
	if !Available() {
		return false
	}
	profile := set.profileName()
	if InMeghProfile(profile) {
		return false
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false
	}
	if len(argv) < 2 {
		return false
	}
	// Only interactive attach commands, not every subcommand that might dial ssh.
	switch argv[1] {
	case "ssh", "tmux":
	default:
		return false
	}
	if argv[1] == "tmux" && (len(argv) < 3 || argv[2] != "attach") {
		return false
	}

	if !ProfileLoaded(profile) {
		if set.StoreDir == "" {
			fmt.Fprintf(os.Stderr, "megh: iTerm profile %q not loaded; run `megh iterm load %s`\n", profile, profile)
			return false
		}
		if _, err := Load(set.StoreDir, profile); err != nil {
			fmt.Fprintf(os.Stderr, "megh: could not load iTerm profile %q: %v\n", profile, err)
			return false
		}
	}

	megh, err := resolveMeghBinary()
	if err != nil {
		fmt.Fprintf(os.Stderr, "megh: iTerm delegate: %v\n", err)
		return false
	}
	shellCmd := buildReexecShell(megh, argv[1:])
	if err := openInMeghProfile(shellCmd, profile); err != nil {
		fmt.Fprintf(os.Stderr, "megh: could not open iTerm profile %q: %v\n", profile, err)
		return false
	}
	return true
}

// resolveMeghBinary is the megh we re-exec in iTerm. os.Executable can point at
// a moved or removed path; fall back to PATH via LookPath before opening a tab.
func resolveMeghBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if st, err := os.Stat(exe); err == nil && !st.IsDir() {
		return exe, nil
	}
	if p, err := exec.LookPath("megh"); err == nil {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			p = resolved
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("megh binary not found at %q (run make install)", exe)
}

// buildReexecShell returns a command iTerm can exec. Profile "command" is not
// run under a shell unless we wrap it; a bare "export …; exec …" line fails
// with execvp errno 2 because iTerm tries to execute the first token as the binary.
func buildReexecShell(meghBin string, args []string) string {
	var inner strings.Builder
	inner.WriteString("export MEGH_ITERM_REEXEC=1; megh=")
	inner.WriteString(shellQuote(meghBin))
	inner.WriteString(`; [ -x "$megh" ] || megh="$(command -v megh)"; "$megh"`)
	for _, a := range args {
		inner.WriteByte(' ')
		inner.WriteString(shellQuote(a))
	}
	// Do not exec megh: keep zsh as the session leader so iTerm honors
	// "Close Sessions On End: Never" and scrollback survives ssh ending.
	inner.WriteString(`; _rc=$?; exit $_rc`)
	return "/bin/zsh -lic " + shellQuote(inner.String())
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n'\"\\$`") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"\''"`) + "'"
}

func appleScriptQuote(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}

func openInMeghProfile(shellCommand, profileName string) error {
	script := fmt.Sprintf(`
tell application "iTerm2"
  activate
  set cmd to "%s"
  if (count of windows) = 0 then
    create window with profile "%s" command cmd
  else
    tell current window
      create tab with profile "%s" command cmd
    end tell
  end if
end tell
`, appleScriptQuote(shellCommand), appleScriptQuote(profileName), appleScriptQuote(profileName))
	return exec.Command("osascript", "-e", script).Run()
}
