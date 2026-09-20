package cmd

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/providers"
)

// The key is the whole reason a local box is joined after it is up rather than
// at create: it must not end up anywhere durable. stdin is not durable; an argv
// is visible in the box's process list, and a container env outlives the run.
func TestBringUpKeepsTheKeyOffTheArgv(t *testing.T) {
	d := dial{host: "127.0.0.1", port: 49153, boxKey: true}
	args := tsBringUpArgs(d, "up")
	stdin := string(tsBringUpStdin("devlocal", "tskey-auth-SECRET"))

	if strings.Contains(strings.Join(args, " "), "SECRET") {
		t.Errorf("the key reached the argv: %v", args)
	}
	if !strings.Contains(stdin, "tskey-auth-SECRET") {
		t.Error("the key never reached stdin, so the box cannot authenticate")
	}
	if !strings.Contains(stdin, "export TS_HOSTNAME='devlocal'") {
		t.Errorf("the box must come up under its BARE name (C1):\n%s", firstLines(stdin, 3))
	}
	if !strings.HasSuffix(strings.Join(args, " "), "bash -s -- up") {
		t.Errorf("bring-up must run the embedded script under bash: %v", args)
	}
}

// With no key at all the script reuses whatever auth the box already has, which
// is what makes `megh mesh join` re-runnable and what a restarted box relies on.
func TestBringUpWithoutAKeyExportsNone(t *testing.T) {
	stdin := string(tsBringUpStdin("devlocal", ""))
	if strings.Contains(stdin, "export TS_AUTHKEY") {
		t.Errorf("an empty key must not be exported at all:\n%s", firstLines(stdin, 3))
	}
}

// `megh down` announced "asked <box> to leave the tailnet" for any box on a
// backend with a mesh, including a local box that never joined one. The box
// reports whether it was connected and the message follows.
func TestLeaveMessageFollowsWhatTheBoxReported(t *testing.T) {
	if got := meshLeaveMessage("devlocal", "left\n", nil); !strings.Contains(got, "left the") {
		t.Errorf("a box that was connected should report leaving, got %q", got)
	}
	if got := meshLeaveMessage("devlocal", "", nil); got != "" {
		t.Errorf("a box that was never on the mesh should say nothing, got %q", got)
	}
	if got := meshLeaveMessage("devlocal", "", io.EOF); !strings.Contains(got, "could not reach") {
		t.Errorf("an unreachable box should say so, got %q", got)
	}
}

// `megh mesh ls` asks the box four things in one round trip. The probe speaks
// key=value so a tailscale output format change cannot silently shift a column.
func TestParseMeshProbe(t *testing.T) {
	got := parseMeshProbe("state=Running\nip=100.94.12.7\nname=devlocal.tail1a2b.ts.net\nports=7681,7682\n")
	if !got.Up || got.IP != "100.94.12.7" || got.Name != "devlocal.tail1a2b.ts.net" {
		t.Errorf("parsed %+v", got)
	}
	if got.Ports != "7681,7682" {
		t.Errorf("ports = %q", got.Ports)
	}
	if off := parseMeshProbe("state=Stopped\n"); off.Up {
		t.Error("a stopped daemon is not on the mesh")
	}
	if none := parseMeshProbe(""); none.Up || none.IP != "" {
		t.Errorf("an empty probe is not a box on the mesh: %+v", none)
	}
}

// A key minted for a box that cannot be handed one is a key spent on nothing.
// The local backend joins later, over SSH, so `up` must not even try.
func TestBootAuthKeyOnlyMintsForBackendsThatJoinAtBoot(t *testing.T) {
	old := cfg
	t.Cleanup(func() { cfg = old })
	cfg = config.Config{Tailscale: config.Tailscale{MintKeys: true, Tag: "tag:megh"}}

	if key, warned := captureMint(t, providers.Mesh{Vendor: "tailscale"}); key != "" || warned != "" {
		t.Errorf("a backend that joins later must not mint at up (key=%q, stderr=%q)", key, warned)
	}
	// Minting at boot is attempted; with no credential configured it warns and
	// falls back, which is the existing contract and proves the attempt happened.
	if key, warned := captureMint(t, providers.Mesh{Vendor: "tailscale", AtBoot: true}); key != "" && warned == "" {
		t.Error("a boot-key backend should have attempted to mint")
	} else if warned == "" {
		t.Error("expected the mint attempt to report why it produced no key")
	}
}

func captureMint(t *testing.T, m providers.Mesh) (key, stderr string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w
	key = bootAuthKey(context.Background(), m, "devlocal")
	os.Stderr = old
	w.Close()
	out, _ := io.ReadAll(r)
	return key, string(out)
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
