package cmd

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/panyam/megh/internal/features"
	"github.com/panyam/megh/internal/providers"
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

// A remote probe must dial a box the way every other command dials it. Docker
// recycles host ports, so a recreated local box behind a recycled port trips a
// host-key MISMATCH: the probe pinned the key while `megh ssh` did not, so
// browse and `mesh ls` failed where an interactive session worked.
func TestSSHCaptureUsesTheSameDialOptions(t *testing.T) {
	local := strings.Join(sshCaptureArgs("", dial{host: "127.0.0.1", port: 49153}, "bash -s"), " ")
	if !strings.Contains(local, "UserKnownHostsFile=/dev/null") {
		t.Errorf("a loopback box must not pin a host key: %s", local)
	}
	if !strings.Contains(local, "-p 49153") || !strings.Contains(local, "BatchMode=yes") {
		t.Errorf("port and non-interactive options lost: %s", local)
	}
	remote := strings.Join(sshCaptureArgs("", dial{host: "devbox"}, "bash -s"), " ")
	if !strings.Contains(remote, "StrictHostKeyChecking=accept-new") {
		t.Errorf("a remote box keeps the normal host-key policy: %s", remote)
	}
}

// Every other command takes the box first (megh ssh <box>, megh mesh join
// <box>), and browse advertised `[port] [box]`. Parsing stays tolerant of
// either order — a numeric argument is a port, anything else is the box — so
// old muscle memory keeps working while the help teaches one shape.
func TestBrowseArgsTakeTheBoxFirstAndSeveralPorts(t *testing.T) {
	got := parseBrowseArgs([]string{"dev", "5678", "3000"})
	if got.box != "dev" || !slices.Equal(got.ports, []int{5678, 3000}) {
		t.Errorf("parsed %+v", got)
	}
	if old := parseBrowseArgs([]string{"6080", "dev"}); old.box != "dev" || !slices.Equal(old.ports, []int{6080}) {
		t.Errorf("the old port-first order must still parse: %+v", old)
	}
	if bare := parseBrowseArgs([]string{"3000"}); bare.box != "" || !slices.Equal(bare.ports, []int{3000}) {
		t.Errorf("a lone port means the sole box: %+v", bare)
	}
	if none := parseBrowseArgs(nil); none.box != "" || len(none.ports) != 0 {
		t.Errorf("no args means every live surface on the sole box: %+v", none)
	}
	if !strings.HasPrefix(browseCmd.Use, "browse [box]") {
		t.Errorf("help still advertises ports first: %q", browseCmd.Use)
	}
}

// A backgrounded tunnel needs a handle to close later. ssh's own control socket
// is that handle, so there is no state file to go stale: if the socket is gone
// the tunnel is gone.
func TestBackgroundTunnelIsAddressableByItsControlSocket(t *testing.T) {
	d := dial{host: "127.0.0.1", port: 49153, boxKey: true}
	sock := tunnelSocket("docker", "dev")
	if !strings.Contains(sock, "dev") || !strings.Contains(sock, "docker") {
		t.Errorf("socket path should name the provider and box: %s", sock)
	}
	// Measured: ssh hard-links its master socket into place, and ~/.megh is
	// persisted onto the work mount, which on a local box is a bind mount from
	// macOS. Linking there fails with "Bad file descriptor", so a tunnel opened
	// from a BOX never comes up. Temp is local to the machine.
	if strings.Contains(sock, ".megh") {
		t.Errorf("the control socket must not live on the persisted volume: %s", sock)
	}
	if !strings.HasPrefix(sock, os.TempDir()) {
		t.Errorf("socket should be under the temp dir, got %s", sock)
	}
	if len(sock) > maxSocketPath {
		t.Errorf("socket path %s is %d chars; a unix socket path is capped near 104", sock, len(sock))
	}

	bg := strings.Join(browseSSHArgs(d, []int{5678}, sock), " ")
	for _, want := range []string{"-f", "-N", "-M", "-S " + sock, "-L 5678:localhost:5678"} {
		if !strings.Contains(bg, want) {
			t.Errorf("background tunnel missing %q: %s", want, bg)
		}
	}

	fg := strings.Join(browseSSHArgs(d, []int{5678}, ""), " ")
	if strings.Contains(fg, "-f") || strings.Contains(fg, "-M") {
		t.Errorf("a foreground tunnel must stay attached: %s", fg)
	}
	if !strings.Contains(strings.Join(tunnelStopArgs(d, sock), " "), "-O exit") {
		t.Error("stopping a tunnel should ask ssh to close its control connection")
	}
	if !strings.Contains(strings.Join(tunnelCheckArgs(d, sock), " "), "-O check") {
		t.Error("a leftover socket file is told from a live tunnel by asking ssh to check it")
	}
}

// Naming ports that are not listening should not throw away the ones that are:
// `megh browse dev 5678 3000` with only 5678 up forwards 5678 and says why 3000
// is missing.
func TestRequestedPortsSplitIntoLiveAndDead(t *testing.T) {
	live, dead := splitRequested([]int{5678, 3000}, []int{7682, 5678})
	if !slices.Equal(live, []int{5678}) || !slices.Equal(dead, []int{3000}) {
		t.Errorf("live=%v dead=%v", live, dead)
	}
	all, none := splitRequested(nil, []int{7682, 5678})
	if !slices.Equal(all, []int{7682, 5678}) || len(none) != 0 {
		t.Errorf("no request means every live port: live=%v dead=%v", all, none)
	}
}

// The Playwright viewer is the surface you reach for when you do NOT want a
// desktop, so asking for it on a box that has not enabled playwright is the
// common first move. It has to name the port as the viewer and offer the
// feature, exactly as vnc does — a bare "nothing is listening on 9323" sends
// you looking at your test config instead.
func TestNotListeningMsgNamesThePlaywrightViewer(t *testing.T) {
	msg := notListeningMsg(9323, []int{7681, 8080}, "devbox")

	for _, want := range []string{
		"nothing is listening on 9323 (playwright)",
		"megh enable playwright devbox",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
}

// The catalog and the launcher are two files that have to agree on one number,
// and nothing else would notice them drifting: browse would tunnel a dead port
// while the viewer served a live one nobody forwards.
func TestViewerPortMatchesTheCatalog(t *testing.T) {
	var port int
	for _, s := range providers.Surfaces {
		if s.Label == "playwright" {
			port = s.Port
		}
	}
	if port == 0 {
		t.Fatal("no playwright surface in the catalog")
	}
	script, err := features.Script("playwright")
	if err != nil {
		t.Fatalf("Script: %v", err)
	}
	if want := fmt.Sprintf("PW_UI_PORT:-%d", port); !strings.Contains(string(script), want) {
		t.Errorf("the launcher does not default to the catalog's port (%q not in playwright.sh)", want)
	}
}
