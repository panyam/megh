package cmd

import (
	"strings"
	"testing"
)

// The reported case: `megh browse 6080 devbox` on a slim box, which ships no vnc
// at all. It used to print a working-looking URL and then leave ssh repeating
// "channel N: open failed". The message has to say three things, because any one
// alone sends you looking in the wrong place: the port is dead, the box is
// otherwise healthy, and how to get the surface.
func TestNotListeningMsgNamesWhatIsUpAndHowToFixIt(t *testing.T) {
	msg := notListeningMsg(6080, []int{7681, 7682, 8080}, "devbox")

	for _, want := range []string{
		"nothing is listening on 6080 (vnc)",
		"7682 (webterm)",         // what IS up, which is the actual question
		"megh enable vnc devbox", // the fix, named for this box
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
}

// A port with no `megh enable` feature behind it must not invent one.
func TestNotListeningMsgOmitsFixForBakedSurfaces(t *testing.T) {
	msg := notListeningMsg(7682, []int{7681}, "devbox")
	if strings.Contains(msg, "megh enable") {
		t.Errorf("webterm is baked into every image; no enable hint should be offered:\n%s", msg)
	}
}

// A box that is still booting has nothing listening. Suggesting `megh enable`
// would be actively misleading there, but the empty-list case still has to
// explain itself rather than print a bare header.
func TestNotListeningMsgHandlesNothingUp(t *testing.T) {
	msg := notListeningMsg(6080, nil, "devbox")
	if !strings.Contains(msg, "still booting") {
		t.Errorf("expected a hint that the box may still be coming up:\n%s", msg)
	}
}

// The box's login shell is zsh (baked in provision.sh), and /dev/tcp is a
// bash-only feature. Sending this loop to the login shell made the probe find
// nothing on every box, so browse reported "no web surfaces are up" while ttyd
// was plainly listening. It must name bash explicitly.
func TestProbeDoesNotDependOnTheLoginShell(t *testing.T) {
	probeCmd := probeCmd(probePorts(0))
	if strings.Contains(probeCmd, "/dev/tcp") && !strings.HasPrefix(probeCmd, "bash -c") {
		t.Errorf("probe uses /dev/tcp but does not invoke bash explicitly; zsh will find nothing:\n%s", probeCmd)
	}
	if !strings.Contains(probeCmd, "exit 0") {
		t.Errorf("probe must end in `exit 0`, or a closed last port makes ssh exit 1 "+
			"and the caller discards a good answer:\n%s", probeCmd)
	}
}

// A dev server is not in the catalog. Probing only the catalog made
// `megh browse 5173` report "nothing is listening" for a server that was up.
func TestProbeIncludesARequestedNonCatalogPort(t *testing.T) {
	if !strings.Contains(probeCmd(probePorts(5173)), " 5173") {
		t.Errorf("requested port 5173 is not probed: %v", probePorts(5173))
	}
	if n := len(probePorts(7682)); n != len(probePorts(0)) {
		t.Errorf("a catalog port must not be probed twice: %v", probePorts(7682))
	}
}

// 127.0.0.1 misses a server that binds ::1 only (Vite on a recent Node), which
// the -L forward to localhost would have reached. Probe and forward must agree.
func TestProbeDialsLocalhostNotV4Loopback(t *testing.T) {
	c := probeCmd(probePorts(0))
	if !strings.Contains(c, "/dev/tcp/localhost/") || strings.Contains(c, "127.0.0.1") {
		t.Errorf("probe must dial localhost so an IPv6-only listener is found:\n%s", c)
	}
}

// An arbitrary port has no catalog label; the message should not call it "(port)".
func TestNotListeningMsgForNonCatalogPort(t *testing.T) {
	msg := notListeningMsg(5173, []int{7682}, "devbox")
	if !strings.Contains(msg, "nothing is listening on 5173 on devbox") || strings.Contains(msg, "megh enable") {
		t.Errorf("unexpected message for a non-catalog port:\n%s", msg)
	}
}
