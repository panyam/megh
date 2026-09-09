package docker

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/panyam/megh/internal/config"
)

// Mount is one resolved bind mount: an absolute host path, an absolute path
// inside the box, and whether the box may write through it.
type Mount struct {
	Host     string
	Box      string
	ReadOnly bool
}

// Arg renders the mount as a docker -v value.
func (m Mount) Arg() string {
	if m.ReadOnly {
		return m.Host + ":" + m.Box + ":ro"
	}
	return m.Host + ":" + m.Box
}

// ParseMounts resolves the megh.yaml `mounts:` map into bind mounts against a
// work mount.
//
// The box path follows the convention `symlinks:` already establishes: relative
// to the work mount unless absolute. That is what lets one megh.yaml serve both
// backends. `~/projects: repos/projects` bind-mounts the host tree at
// <workMount>/repos/projects, and the existing `symlinks:` entry of the same
// name then makes the in-box ~/projects resolve to it, exactly as it resolves to
// a hydrated clone on a cloud box.
//
// A relative target must NOT be resolved against /mnt/work even though that is
// the path everything in the box uses. /mnt/work is a symlink the entrypoint
// creates from the work mount, and bind mounts are applied before the entrypoint
// runs. Pre-creating /mnt/work as a real directory makes the entrypoint's
// `ln -sfn "${WORK_MOUNT}" /mnt/work` fail against an existing directory, and
// under `set -euo pipefail` that kills PID 1 and the box never boots.
//
// Results are sorted by box path so a nested mount is always created after the
// mount it sits inside.
func ParseMounts(spec map[string]string, workMount string) ([]Mount, error) {
	out := make([]Mount, 0, len(spec))
	for host, target := range spec {
		if host == "" || target == "" {
			return nil, fmt.Errorf("mounts: empty host path or target (host=%q target=%q)", host, target)
		}
		ro := false
		if t, cut := strings.CutSuffix(target, ":ro"); cut {
			ro, target = true, t
		} else if t, cut := strings.CutSuffix(target, ":rw"); cut {
			target = t
		}
		if target == "" {
			return nil, fmt.Errorf("mounts: %q has a mode but no path", host)
		}
		box := target
		if !path.IsAbs(box) {
			box = path.Join(workMount, box)
		}
		hostPath := config.ExpandPath(host)
		if !strings.HasPrefix(hostPath, "/") {
			return nil, fmt.Errorf("mounts: host path %q must be absolute or ~-relative", host)
		}
		out = append(out, Mount{Host: hostPath, Box: box, ReadOnly: ro})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Box < out[j].Box })
	return out, nil
}
