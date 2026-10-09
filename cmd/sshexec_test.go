package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// With no scoped agent, ssh must forward nothing: a profile with no GitHub
// identity used to pass -A with the AMBIENT agent, handing a work machine's
// keys to the box.
func TestUnscopedSSHForwardsNoAgent(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "argv")
	os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\necho \"$@\" > "+log+"\n"), 0o755)
	t.Setenv("PATH", dir)

	if err := runSSH("", nil, []string{"-A", "-t", "root@devbox", "tmux"}, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	got := strings.Fields(string(b))
	if slices.Contains(got, "-A") {
		t.Errorf("ssh got -A without a scoped agent: %q", got)
	}
	// First, so it beats a ForwardAgent yes in ssh_config.
	if len(got) < 2 || got[0] != "-o" || got[1] != "ForwardAgent=no" {
		t.Errorf("ForwardAgent=no must lead the args: %q", got)
	}
	if !slices.Contains(got, "-t") || got[len(got)-1] != "tmux" {
		t.Errorf("other args lost: %q", got)
	}
}

// A scoped agent holds only the profile's GitHub keys, so -A stays.
func TestScopedSSHKeepsForwarding(t *testing.T) {
	args := []string{"-A", "-t", "root@devbox"}
	if got := agentArgs(true, args); !slices.Equal(got, args) {
		t.Errorf("got %q", got)
	}
}

func TestSSHCaptureForwardsNoAgent(t *testing.T) {
	got := sshCaptureArgs("", dial{host: "devbox"}, "true")
	if got[0] != "-o" || got[1] != "ForwardAgent=no" || slices.Contains(got, "-A") {
		t.Errorf("got %q", got)
	}
}
