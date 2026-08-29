package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/panyam/megh/internal/config"
)

// pushSync mirrors the megh.yaml `sync:` entries (local dir -> box dir) onto a
// box. It is the directory counterpart to `files:`, and the two differ in
// intent rather than only in shape:
//
//   - `files:` copies individual files at mode 0600. It is for secrets and rc
//     files, and it only ever adds.
//   - `sync:` MIRRORS a tree, deletes included. That is the point. Skills and
//     commands get reorganised on the laptop, and a copy that only ever adds
//     leaves the superseded ones behind, so a box ends up offering both the old
//     and the new. Deleting is what makes a consolidation propagate.
//
// A `/mnt/work/...` destination lands on the volume and so survives rebuilds,
// which is the normal choice; a `~/...` destination is ephemeral.
func pushSync(d dial, boxKey string, dirs map[string]string) error {
	if len(dirs) == 0 {
		return nil
	}
	if _, err := exec.LookPath("rsync"); err != nil {
		return fmt.Errorf("sync: needs rsync on this machine: %w", err)
	}
	for local, dest := range dirs {
		src := config.ExpandPath(local)
		info, err := os.Stat(src)
		if err != nil {
			fmt.Fprintf(os.Stderr, "megh: sync: skip %s: %v\n", local, err)
			continue
		}
		if !info.IsDir() {
			fmt.Fprintf(os.Stderr, "megh: sync: skip %s: not a directory (use files: for a single file)\n", local)
			continue
		}
		boxDest := dest
		if strings.HasPrefix(boxDest, "~/") {
			boxDest = "/root/" + boxDest[2:]
		}
		// Trailing slashes: copy the CONTENTS of src into dest, rather than
		// nesting src inside it.
		if err := rsyncDir(d, boxKey, strings.TrimSuffix(src, "/")+"/", strings.TrimSuffix(boxDest, "/")+"/"); err != nil {
			return fmt.Errorf("sync %s: %w", local, err)
		}
		fmt.Fprintf(os.Stderr, "megh: synced %s -> %s\n", local, boxDest)
	}
	return nil
}

// rsyncDir runs one mirror. The flags are not the usual -a on purpose: the
// scratch volume is NFS with root squashed, so preserving ownership fails the
// whole transfer with "chown: Invalid argument". -rlptz keeps the parts that do
// work (recursion, links, permissions, times) and drops the parts that cannot.
func rsyncDir(d dial, boxKey, src, dest string) error {
	ssh := []string{"ssh", "-o", "StrictHostKeyChecking=accept-new", "-o", "BatchMode=yes"}
	if boxKey != "" {
		ssh = append(ssh, "-i", config.ExpandPath(boxKey), "-o", "IdentitiesOnly=yes")
	}
	if d.port != 0 {
		ssh = append(ssh, "-p", strconv.Itoa(d.port))
	}
	cmd := exec.Command("rsync",
		"-rlptz", "--no-owner", "--no-group", "--delete",
		"--exclude", "*.bak", "--exclude", ".DS_Store",
		"-e", strings.Join(ssh, " "),
		src, d.userHost()+":"+dest,
	)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
