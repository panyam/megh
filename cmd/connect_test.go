package cmd

import (
	"errors"
	"os"
	"testing"
	"time"

	"golang.org/x/term"
)

func TestSSHStayOpenHonorsOptOut(t *testing.T) {
	t.Setenv("MEGH_SSH_STAY_OPEN", "0")
	done := make(chan struct{})
	go func() {
		sshStayOpenAfterDisconnect(errors.New("disconnect"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("sshStayOpenAfterDisconnect blocked with MEGH_SSH_STAY_OPEN=0")
	}
}

func TestSSHStayOpenSkipsNonTTY(t *testing.T) {
	if sessionTTYAvailable() {
		t.Skip("session has a tty")
	}
	done := make(chan struct{})
	go func() {
		sshStayOpenAfterDisconnect(errors.New("disconnect"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("sshStayOpenAfterDisconnect blocked without a tty")
	}
}

func sessionTTYAvailable() bool {
	f, err := os.Open("/dev/tty")
	if err != nil {
		return term.IsTerminal(int(os.Stdin.Fd()))
	}
	defer f.Close()
	return term.IsTerminal(int(f.Fd()))
}
