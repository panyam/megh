package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runInstall runs install.sh with fake gh and curl on PATH. ghOK says whether
// gh is logged in and can download the release; curlOK whether plain curl can.
// The fakes record what they were asked for in calls.log.
func runInstall(t *testing.T, ghOK, curlOK bool) (out string, err error, home string) {
	return runInstallEnv(t, ghOK, curlOK, "MEGH_TARGET=linux-amd64")
}

// runInstallEnv is runInstall with extra environment; without MEGH_TARGET in it
// the script detects the target itself, the way a real install does.
func runInstallEnv(t *testing.T, ghOK, curlOK bool, extra ...string) (out string, err error, home string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("posix sh")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	home = filepath.Join(dir, "home")
	os.MkdirAll(bin, 0o755)
	os.MkdirAll(home, 0o755)
	log := filepath.Join(dir, "calls.log")

	gh := `#!/bin/sh
echo "gh $*" >> ` + log + `
ok=` + map[bool]string{true: "0", false: "1"}[ghOK] + `
case "$1 $2" in
  "auth status") exit $ok ;;
  "api user") [ $ok = 0 ] && echo someone; exit $ok ;;
  "release download")
    [ $ok = 0 ] || exit 1
    while [ $# -gt 0 ]; do
      case "$1" in -D) shift; d="$1" ;; -p) shift; printf 'bin' > "$d/$1" ;; esac
      shift
    done
    exit 0 ;;
  "api repos/someone/dotfiles/contents/megh/megh.yaml") printf 'default_flavor: slim\n'; exit $ok ;;
esac
exit 1
`
	curl := `#!/bin/sh
echo "curl $*" >> ` + log + `
[ ` + map[bool]string{true: "0", false: "1"}[curlOK] + ` = 0 ] || exit 22
while [ $# -gt 0 ]; do case "$1" in -o) shift; printf 'bin' > "$1" ;; esac; shift; done
`
	os.WriteFile(filepath.Join(bin, "gh"), []byte(gh), 0o755)
	os.WriteFile(filepath.Join(bin, "curl"), []byte(curl), 0o755)

	cmd := exec.Command("sh", "install.sh")
	cmd.Env = []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"HOME=" + home,
		"MEGH_INSTALL_DIR=" + filepath.Join(dir, "out"),
	}
	cmd.Env = append(cmd.Env, extra...)
	b, err := cmd.CombinedOutput()
	calls, _ := os.ReadFile(log)
	return string(b) + "\n--- calls\n" + string(calls), err, home
}

// A logged-in gh fetches the release itself (so a private repo works), and the
// config comes from the signed-in user's own dotfiles, not a fixed account.
func TestInstallPrefersGhAndTheUsersOwnConfigRepo(t *testing.T) {
	out, err, home := runInstall(t, true, false)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "gh release download latest -R panyam/megh") || strings.Contains(out, "curl ") {
		t.Errorf("expected gh, not curl:\n%s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".config/megh/megh.yaml")); string(b) != "default_flavor: slim\n" {
		t.Errorf("config not fetched from someone/dotfiles:\n%s", out)
	}
}

func TestInstallFallsBackToCurlWithoutGh(t *testing.T) {
	out, err, _ := runInstall(t, false, true)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "curl ") || !strings.Contains(out, "installed:") {
		t.Errorf("expected a curl install:\n%s", out)
	}
}

func TestInstallSaysToLogInWhenNothingCanFetch(t *testing.T) {
	out, err, _ := runInstall(t, false, false)
	if err == nil || !strings.Contains(out, "gh auth login") {
		t.Errorf("a private repo with no gh must fail naming the fix: %v\n%s", err, out)
	}
}

// A plain Linux box has no PREFIX (only Termux sets it), and the script runs
// under set -u, so the Termux check must not expand an unset PREFIX. The other
// tests force MEGH_TARGET and skip detection, which is how this got through.
func TestInstallDetectsLinuxWithNoPrefixSet(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("detection runs against this machine's uname")
	}
	out, err, _ := runInstallEnv(t, true, false)
	if err != nil || strings.Contains(out, "parameter not set") || !strings.Contains(out, "target:  linux-") {
		t.Fatalf("%v\n%s", err, out)
	}
}
