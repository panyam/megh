package cmd

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panyam/megh/internal/config"
)

func TestHydrateRunsHereOnABoxUnlessABoxIsNamed(t *testing.T) {
	withBoxMarker(t, true)
	if !hydrateRunsHere(false, nil) {
		t.Error("on a box, bare `megh hydrate` must run locally (no RUNPOD_API_KEY)")
	}
	if hydrateRunsHere(false, []string{"other"}) {
		t.Error("naming a box must target it")
	}
	withBoxMarker(t, false)
	if hydrateRunsHere(false, nil) {
		t.Error("off a box, hydrate targets a box")
	}
	if !hydrateRunsHere(true, nil) {
		t.Error("--local always runs here")
	}
}

func TestGithubHTTPS(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:panyam/megh.git":       "https://github.com/panyam/megh.git",
		"git@github.com:panyam/megh":           "https://github.com/panyam/megh.git",
		"ssh://git@github.com/panyam/megh.git": "https://github.com/panyam/megh.git",
		"https://github.com/panyam/megh.git":   "https://github.com/panyam/megh.git",
		"git@gitlab.com:someone/thing.git":     "git@gitlab.com:someone/thing.git",
	} {
		if got := githubHTTPS(in); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
}

// The script picks ssh with an agent and https without one, per run, and
// stops before any clone when neither an agent nor a gh login exists.
func TestApplyScriptFallsBackToHTTPSWithoutAnAgent(t *testing.T) {
	c := config.Config{DefaultGHKey: "personal", Repos: []config.Repo{{URL: "git@github.com:panyam/megh.git"}}}
	s := applyScript(c)
	for _, want := range []string{
		"ssh-add -l",
		"gh auth status",
		"gh auth setup-git",
		"GIT_TERMINAL_PROMPT=0",
		`u="git@gh-personal:panyam/megh.git"`,
		`u="https://github.com/panyam/megh.git"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script missing %q:\n%s", want, s)
		}
	}
	if strings.Index(s, "gh auth status") > strings.Index(s, "git clone") {
		t.Error("the gh login check must come before any clone")
	}
}

// Run the transport preamble for real with a fake ssh-add and gh, to prove the
// branch it takes rather than only that the text is present.
func TestCloneTransportChoosesByAgentAndRefusesWithoutGh(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	run := func(agent, ghLoggedIn bool) (string, error) {
		bin := t.TempDir()
		exit := func(ok bool) string {
			if ok {
				return "exit 0"
			}
			return "exit 2"
		}
		os.WriteFile(filepath.Join(bin, "ssh-add"), []byte("#!/bin/sh\n"+exit(agent)+"\n"), 0o755)
		os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\n[ \"$1 $2\" = 'auth status' ] && "+exit(ghLoggedIn)+"\nexit 0\n"), 0o755)
		cmd := exec.Command(bash, "-c", cloneTransport+`echo "via=$via"`)
		cmd.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run(true, false); err != nil || !strings.Contains(out, "via=ssh") {
		t.Errorf("with an agent: %v %s", err, out)
	}
	if out, err := run(false, true); err != nil || !strings.Contains(out, "via=https") {
		t.Errorf("no agent, gh logged in: %v %s", err, out)
	}
	out, err := run(false, false)
	var ee *exec.ExitError
	if !errors.As(err, &ee) || !strings.Contains(out, "gh auth login") || strings.Contains(out, "via=") {
		t.Errorf("no agent and no gh must stop with the fix: %v %s", err, out)
	}
}

// On a box, ~/.config/megh/megh.yaml is a symlink onto the volume whose target
// may not exist yet; the pull must land at the target and keep the link.
func TestWriteThroughFollowsADanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "volume", "state", "megh", "megh.yaml")
	link := filepath.Join(dir, "home", ".config", "megh", "megh.yaml")
	os.MkdirAll(filepath.Dir(link), 0o755)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	wrote, err := writeThrough(link, []byte("repos: []\n"))
	if err != nil || wrote != target {
		t.Fatalf("wrote %q err %v", wrote, err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a file")
	}
	if b, _ := os.ReadFile(link); string(b) != "repos: []\n" {
		t.Errorf("read through link: %q", b)
	}
	plain := filepath.Join(dir, "plain", "megh.yaml")
	if wrote, err := writeThrough(plain, []byte("x: 1\n")); err != nil || wrote != plain {
		t.Errorf("plain path: %q %v", wrote, err)
	}
}

func stubGhLogin(t *testing.T, login string, err error) {
	t.Helper()
	old := ghLogin
	t.Cleanup(func() { ghLogin = old })
	ghLogin = func() (string, error) { return login, err }
}

// The default config repo is the signed-in user's own dotfiles, never a
// hardcoded account; an explicit setting wins without asking gh.
func TestConfigRepoDefaultsToTheSignedInUser(t *testing.T) {
	t.Setenv("MEGH_CONFIG_REPO", "")
	stubGhLogin(t, "someone", nil)
	if r, err := configRepo(); err != nil || r != "someone/dotfiles" {
		t.Errorf("got %q %v", r, err)
	}
	t.Setenv("MEGH_CONFIG_REPO", "org/config")
	stubGhLogin(t, "", errors.New("gh must not be asked"))
	if r, err := configRepo(); err != nil || r != "org/config" {
		t.Errorf("env should win: %q %v", r, err)
	}
	t.Setenv("MEGH_CONFIG_REPO", "")
	if _, err := configRepo(); err == nil {
		t.Error("no setting and no gh login must be an error naming the fix")
	}
}

func TestConfigPullRefusesNonYAMLBeforeWriting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MEGH_CONFIG_REPO", "")
	stubGhLogin(t, "someone", nil)
	old := ghRawFile
	t.Cleanup(func() { ghRawFile = old })
	ghRawFile = func(repo, path string) ([]byte, error) { return []byte("<html>404</html>\n: : :"), nil }
	if err := configPullCmd.RunE(configPullCmd, nil); err == nil {
		t.Fatal("garbage must be refused")
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "megh", "megh.yaml")); err == nil {
		t.Error("nothing should have been written")
	}
	ghRawFile = func(repo, path string) ([]byte, error) {
		if repo != "someone/dotfiles" || path != "megh/megh.yaml" {
			t.Errorf("fetched %s/%s", repo, path)
		}
		return []byte("default_flavor: slim\n"), nil
	}
	if err := configPullCmd.RunE(configPullCmd, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".config", "megh", "megh.yaml")); string(b) != "default_flavor: slim\n" {
		t.Errorf("wrote %q", b)
	}
}
