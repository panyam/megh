package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// C6: a path that resolves on exactly one machine must not reach a file that
// more than one machine reads. Both halves of this test spell the pattern they
// forbid, so both exempt this file — the preamble trap in CONSTRAINTS.md.
const hygieneSelf = "hygiene_test.go"

// A per-user home directory. Deliberately not anchored to one OS: /Users/<name>
// is the Mac, /home/<name> a Linux box or CI runner, and either one in a tracked
// file is the same bug.
var perUserHome = regexp.MustCompile(`/(Users|home)/[a-zA-Z0-9._-]+/`)

func gitLines(t *testing.T, args ...string) []string {
	t.Helper()
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			t.Fatalf("git %s: %v", strings.Join(args, " "), err)
		}
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// The file contents are read in-process rather than with `git grep` so that Go's
// test cache tracks them: it records files the test itself consults, but cannot
// see through a child process, and a cached pass on this gate would hide exactly
// the drift it exists to catch. The tracked-file LIST still comes from git, so
// run the Verify with `-count=1` after adding files.
func TestNoMachineLocalPathsInTrackedFiles(t *testing.T) {
	// A tracked symlink must be relative and must stay inside the repo. An
	// absolute one is a machine-local fact committed to a shared artifact: it
	// resolves where it was made and dangles everywhere else. This is how the
	// `gaps` skill came to exist only on the Mac.
	for _, line := range gitLines(t, "ls-files", "-s") {
		mode, rest, ok := strings.Cut(line, " ")
		if !ok || mode != "120000" {
			continue
		}
		_, path, ok := strings.Cut(rest, "\t")
		if !ok {
			continue
		}
		target, err := exec.Command("git", "cat-file", "blob", ":"+path).Output()
		if err != nil {
			t.Fatalf("reading tracked symlink %s: %v", path, err)
		}
		dest := strings.TrimSpace(string(target))
		if strings.HasPrefix(dest, "/") {
			t.Errorf("absolute tracked symlink %s -> %s: resolves on one machine only (C6)", path, dest)
			continue
		}
		if joined := filepath.Join(filepath.Dir(path), dest); strings.HasPrefix(joined, "..") {
			t.Errorf("tracked symlink %s -> %s escapes the repo (C6)", path, dest)
		}
	}

	// The same rule spelled out in text rather than in a link.
	for _, path := range gitLines(t, "ls-files") {
		if path == hygieneSelf {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			continue // a tracked symlink's target, or a file removed in the work tree
		}
		if bytes.IndexByte(body, 0) >= 0 {
			continue // binary
		}
		if m := perUserHome.Find(body); m != nil {
			t.Errorf("%s carries a per-user home path (%q): use $HOME, a repo-relative path, or derive it at run time (C6)", path, m)
		}
	}
}
