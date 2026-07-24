package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/panyam/megh/internal/config"
)

// runSSH runs ssh. It connects to the box authenticating ONLY with boxKey (if
// set), and forwards an agent scoped to exactly fwdKeys, so a corporate key in
// your normal agent never reaches a third-party VM, and each GitHub identity is
// available on the box for git. If boxKey and fwdKeys are both empty it uses the
// ambient agent/config (legacy). stdin defaults to os.Stdin.
func runSSH(boxKey string, fwdKeys []string, sshArgs []string, stdin io.Reader) error {
	env := os.Environ()
	if len(fwdKeys) > 0 {
		expanded := make([]string, len(fwdKeys))
		for i, k := range fwdKeys {
			expanded[i] = config.ExpandPath(k)
		}
		sock, cleanup, err := scopedAgent(expanded)
		if err != nil {
			return err
		}
		defer cleanup()
		env = append(os.Environ(), "SSH_AUTH_SOCK="+sock)
	}
	if boxKey != "" {
		// Authenticate to the box with this key only.
		sshArgs = append([]string{"-i", config.ExpandPath(boxKey), "-o", "IdentitiesOnly=yes"}, sshArgs...)
	}
	c := exec.Command("ssh", sshArgs...)
	c.Env = env
	if stdin == nil {
		stdin = os.Stdin
	}
	c.Stdin = stdin
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}

// scopedAgent starts a throwaway ssh-agent holding exactly keyFiles, returning
// its socket path and a cleanup func. The agent (and the forwarded keys) live
// only for the duration of the ssh call.
func scopedAgent(keyFiles []string) (string, func(), error) {
	sock := filepath.Join(os.TempDir(), fmt.Sprintf("megh-agent-%d.sock", os.Getpid()))
	_ = os.Remove(sock)
	ag := exec.Command("ssh-agent", "-D", "-a", sock)
	ag.Stderr = os.Stderr
	if err := ag.Start(); err != nil {
		return "", nil, fmt.Errorf("start scoped ssh-agent: %w", err)
	}
	cleanup := func() {
		_ = ag.Process.Kill()
		_, _ = ag.Process.Wait()
		_ = os.Remove(sock)
	}
	ok := false
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(sock); err == nil {
			ok = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ok {
		cleanup()
		return "", nil, fmt.Errorf("scoped ssh-agent socket did not appear")
	}
	for _, key := range keyFiles {
		add := exec.Command("ssh-add", key)
		add.Env = append(os.Environ(), "SSH_AUTH_SOCK="+sock)
		add.Stdout, add.Stderr = os.Stderr, os.Stderr
		if err := add.Run(); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("load %s into scoped agent: %w", key, err)
		}
	}
	return sock, cleanup, nil
}
