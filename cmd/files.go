package cmd

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/panyam/megh/internal/config"
)

// pushFiles copies the megh.yaml `files:` entries (local path -> box path) onto a
// box over SSH. This is for things that must NOT live in a repo — secrets, machine
// rc files — so they never touch the image or git. Files land with mode 0600. The
// box is always root, so a "~/" dest expands to /root; a "/mnt/work/" dest persists
// on the volume, anything else is ephemeral (re-copied on the next connect).
//
// Security: never put a broad credential (e.g. RUNPOD_API_KEY, which would let the
// box manage your OTHER boxes) in a copied file — scope it to what the box needs.
func pushFiles(d dial, boxKey string, files map[string]string) error {
	if len(files) == 0 {
		return nil
	}
	// One remote script writes them all: `put <dest>` reads base64 from its heredoc.
	var b strings.Builder
	b.WriteString("set -e\nput() { mkdir -p \"$(dirname \"$1\")\"; base64 -d > \"$1\"; chmod 600 \"$1\"; }\n")
	n := 0
	for local, dest := range files {
		data, err := os.ReadFile(config.ExpandPath(local))
		if err != nil {
			fmt.Fprintf(os.Stderr, "megh: files: skip %s: %v\n", local, err)
			continue
		}
		boxDest := dest
		if strings.HasPrefix(boxDest, "~/") {
			boxDest = "/root/" + boxDest[2:]
		}
		fmt.Fprintf(&b, "put %s <<'__MEGHB64__'\n%s\n__MEGHB64__\n",
			shQuote(boxDest), base64.StdEncoding.EncodeToString(data))
		n++
	}
	if n == 0 {
		return nil
	}
	sshArgs := append(d.opts(), d.userHost(), "bash -s")
	fmt.Fprintf(os.Stderr, "megh: copying %d file(s) to the box\n", n)
	return runSSH(boxKey, nil, sshArgs, strings.NewReader(b.String()))
}
