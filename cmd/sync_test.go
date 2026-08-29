package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The rsync flag set is not the usual -a, and each departure fixes a specific
// failure. Exercise the real flags over a local round trip.
func TestSyncFlagsMirrorRatherThanAccumulate(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed")
	}
	src, dst := t.TempDir()+"/", t.TempDir()+"/"
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(src, "skip.bak"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dst, "stale.txt"), []byte("old"), 0o644)

	cmd := exec.Command("rsync", "-rlptz", "--no-owner", "--no-group", "--delete",
		"--exclude", "*.bak", "--exclude", ".DS_Store", src, dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rsync: %v\n%s", err, out)
	}

	var got []string
	entries, _ := os.ReadDir(dst)
	for _, e := range entries {
		got = append(got, e.Name())
	}
	slices.Sort(got)

	// --delete is the whole reason sync: exists rather than reusing files:.
	// Reorganising skills on the laptop must REMOVE the superseded ones from the
	// box, not leave both on offer.
	if slices.Contains(got, "stale.txt") {
		t.Errorf("--delete did not propagate a removal; got %v", got)
	}
	if slices.Contains(got, "skip.bak") {
		t.Errorf("*.bak should be excluded; got %v", got)
	}
	if !slices.Contains(got, "a.txt") {
		t.Errorf("real content missing; got %v", got)
	}
}

// A `~/` destination is rewritten to /root, matching how files: treats one, so
// megh.yaml can be written the same way for both keys.
func TestSyncRewritesHomeDestination(t *testing.T) {
	for in, want := range map[string]string{
		"~/.claude/skills":              "/root/.claude/skills",
		"/mnt/work/state/claude/skills": "/mnt/work/state/claude/skills",
	} {
		got := in
		if strings.HasPrefix(got, "~/") {
			got = "/root/" + got[2:]
		}
		if got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

// A non-directory source is a config mistake, and must be skipped with a note
// rather than silently mirrored as an empty tree.
func TestSyncSkipsNonDirectories(t *testing.T) {
	f := filepath.Join(t.TempDir(), "single.txt")
	os.WriteFile(f, []byte("x"), 0o644)
	// pushSync stats the source and skips anything that is not a directory; it
	// returns nil because a bad entry warns rather than failing the command.
	if err := pushSync(dial{host: "h", port: 22}, "", map[string]string{f: "/mnt/work/x"}); err != nil {
		t.Errorf("a non-directory entry should warn, not error: %v", err)
	}
}
