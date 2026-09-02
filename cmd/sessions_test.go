package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Transcript dirs sit next to credential files, and this repo's history is
// permanent, so an exclude slipping would put a token in git forever.
func TestSessionTarCmdExcludesCredentials(t *testing.T) {
	got := sessionTarCmd("/mnt/work/state/claude/projects")
	for _, want := range sessionExcludes {
		if !strings.Contains(got, "--exclude='"+want+"'") {
			t.Errorf("missing exclude %q in: %s", want, got)
		}
	}
	if !strings.Contains(got, "'*.credentials.json'") {
		t.Error("the credentials file must be excluded by name")
	}
}

// A box that never ran codex has no codex dir, and that is not a failure.
func TestSessionTarCmdTreatsAMissingDirAsExit3(t *testing.T) {
	got := sessionTarCmd("/nope")
	if !strings.Contains(got, "|| exit 3") {
		t.Errorf("a missing directory should exit 3, got: %s", got)
	}
	// Prove the shell really behaves that way, rather than trusting the string.
	c := exec.Command("sh", "-c", got)
	err := c.Run()
	ec, ok := err.(*exec.ExitError)
	if !ok || ec.ExitCode() != 3 {
		t.Errorf("expected exit 3 for a missing dir, got %v", err)
	}
}

// The excludes have to work in the actual tar, not just appear in the string.
func TestSessionTarCmdReallyDropsCredentialFiles(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "a.jsonl"), []byte("transcript"), 0o600)
	os.WriteFile(filepath.Join(src, ".credentials.json"), []byte("SECRET"), 0o600)
	os.WriteFile(filepath.Join(src, "auth.json"), []byte("SECRET"), 0o600)

	out, err := exec.Command("sh", "-c", sessionTarCmd(src)).Output()
	if err != nil {
		t.Fatalf("tar: %v", err)
	}
	dest := t.TempDir()
	untar := exec.Command("tar", "xzf", "-", "-C", dest)
	untar.Stdin = strings.NewReader(string(out))
	if err := untar.Run(); err != nil {
		t.Fatalf("untar: %v", err)
	}
	var got []string
	filepath.Walk(dest, func(p string, fi os.FileInfo, _ error) error {
		if fi != nil && !fi.IsDir() {
			got = append(got, filepath.Base(p))
		}
		return nil
	})
	for _, f := range got {
		if strings.Contains(f, "credentials") || f == "auth.json" {
			t.Errorf("a credential file was transferred: %v", got)
		}
	}
	if len(got) != 1 || got[0] != "a.jsonl" {
		t.Errorf("want just the transcript, got %v", got)
	}
}
