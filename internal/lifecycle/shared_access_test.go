package lifecycle

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/providers"
)

func fakeKey(blob, comment string) string {
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString([]byte(blob)) + " " + comment
}

var (
	phoneKey = fakeKey("phone-key-blob", "phone")
	bwKey    = fakeKey("bitwarden-key-blob", "bitwarden")
)

// A box launched from one device must also let in a key held elsewhere (say a
// password manager's SSH agent), one key per line, because that is what the
// entrypoint appends to authorized_keys.
func TestWithExtraPubKeysAppendsOnePerLine(t *testing.T) {
	got, err := withExtraPubKeys(phoneKey, []string{" " + bwKey + " ", ""})
	if err != nil {
		t.Fatal(err)
	}
	if want := phoneKey + "\n" + bwKey; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The same key listed twice (or equal to the launcher's own) is authorized once.
func TestWithExtraPubKeysDedupesByKeyBody(t *testing.T) {
	got, err := withExtraPubKeys(phoneKey, []string{bwKey, fakeKey("phone-key-blob", "other-comment"), bwKey})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, "\n") != 1 {
		t.Errorf("duplicates should collapse to two keys, got:\n%s", got)
	}
}

// No extras is a no-op, so existing configs launch exactly as before.
func TestWithExtraPubKeysNoneIsUnchanged(t *testing.T) {
	if got, _ := withExtraPubKeys(phoneKey, nil); got != phoneKey {
		t.Errorf("got %q", got)
	}
}

// Pasting the private half is the mistake to catch: it would land in the box's
// env. It must fail the launch, not be silently dropped.
func TestWithExtraPubKeysRejectsNonKeys(t *testing.T) {
	for _, bad := range []string{
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"not a key",
		"ssh-ed25519 !!!notbase64!!!",
	} {
		if _, err := withExtraPubKeys(phoneKey, []string{bad}); err == nil {
			t.Errorf("should reject %q", bad)
		}
	}
}

// A machine off the tailnet needs the public SSH endpoint, with -L before the
// host (after it, ssh would treat it as the remote command).
func TestRenderPortalListsPublicSSH(t *testing.T) {
	md := RenderPortal(config.Config{}, []providers.Box{{Name: "megh-mybox", Status: "RUNNING", PublicIP: "203.0.113.7", SSHPort: 41234}})
	for _, want := range []string{
		"`ssh -p 41234 root@203.0.113.7`",
		"`ssh -p 41234 -L 7682:127.0.0.1:7682 root@203.0.113.7`",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("portal missing %q:\n%s", want, md)
		}
	}
}

// A local box's endpoint is the launcher's loopback, meaningless anywhere else;
// a box with no mapped port yet has nothing to show.
func TestRenderPortalOmitsUnusableSSH(t *testing.T) {
	md := RenderPortal(config.Config{}, []providers.Box{
		{Name: "megh-local", Status: "running", PublicIP: "127.0.0.1", SSHPort: 2222},
		{Name: "megh-booting", Status: "RUNNING"},
	})
	if strings.Contains(md, "ssh -p") {
		t.Errorf("no ssh line expected:\n%s", md)
	}
}
