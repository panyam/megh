package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/panyam/megh/internal/tsapi"
)

// mintKeyExpiry bounds how long a freshly minted key can be redeemed, not how
// long the box lives. The entrypoint runs `tailscale up` during boot, well
// inside this, and a key that outlives its box is exactly what we are getting
// away from.
const mintKeyExpiry = 15 * time.Minute

// mintBoxAuthKey returns a Tailscale node key minted for this box alone, or ""
// to mean "use the ambient static TS_AUTHKEY".
//
// Why bother, when a shared reusable key already works. Three things get fixed
// at once. The key is ephemeral, so Tailscale removes the node on its own once
// the box is gone, which is the root cause of names drifting to <name>-1 (a
// persistent node survives even a clean logout). The key is single-use and
// short-lived, so the copy sitting in the pod's env is worthless minutes later,
// where the shared key stays valid for its full 90 days. And it is minted at
// launch, which makes the most common real failure in this project impossible:
// a box booted with a key older than the one the tailnet now expects.
//
// Every failure path here is a warning and a fallback, never an error. Boxes
// must keep launching when the tailnet is misconfigured, unreachable, or simply
// not set up, because the control machine reaches a box over public SSH.
func mintBoxAuthKey(ctx context.Context, box string) string {
	if !cfg.Tailscale.MintKeys {
		return ""
	}
	c, err := tsClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "megh: %v; using the static TS_AUTHKEY\n", err)
		return ""
	}
	var tags []string
	if cfg.Tailscale.Tag != "" {
		tags = []string{cfg.Tailscale.Tag}
	}
	key, err := c.MintAuthKey(ctx, tsapi.AuthKeyOptions{Box: box, Tags: tags, Expiry: mintKeyExpiry})
	if err != nil {
		// The overwhelmingly likely cause is the tag not existing in the tailnet
		// ACL yet, since Tailscale requires one on OAuth-minted keys and rejects
		// a tag with no tagOwners entry. Say so rather than echoing a bare 4xx.
		fmt.Fprintf(os.Stderr, "megh: could not mint a Tailscale key (%v)\n", err)
		if len(tags) > 0 {
			fmt.Fprintf(os.Stderr, "megh: check that %q exists in the tailnet ACL tagOwners, and that the credential is scoped to it\n", tags[0])
		}
		fmt.Fprintln(os.Stderr, "megh: falling back to the static TS_AUTHKEY")
		return ""
	}
	fmt.Printf("minted a single-use ephemeral tailnet key for %s", box)
	if len(tags) > 0 {
		fmt.Printf(" (%s)", tags[0])
	}
	fmt.Println()
	return key
}
