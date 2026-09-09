package docker

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/providers"
)

// A local "volume" is a directory under volume_root, not a docker named volume.
// Two reasons. A box's whole point here is that its work trees are host paths
// you can open in an editor, and a named volume lives inside the VM where you
// cannot. And `megh storage` reports a size, which is answerable for a
// directory and not for a named volume without shelling into the daemon.
//
// The id and the name are the same string, because the directory name is the
// only identifier there is. That is fine for providers.Find, which matches on
// either.

func (p *Provider) Volumes(ctx context.Context) ([]providers.Volume, error) {
	root := config.ExpandPath(p.settings().volumeRoot)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no local volumes yet is not an error
		}
		return nil, fmt.Errorf("docker: read %s: %w", root, err)
	}
	var out []providers.Volume
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		out = append(out, providers.Volume{
			Provider:   "docker",
			ID:         e.Name(),
			Name:       e.Name(),
			DataCenter: "local",
			Size:       dirSizeGiB(filepath.Join(root, e.Name())),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// CreateVolume makes the directory. sizeGiB and dc are accepted and ignored:
// a host directory grows into whatever the disk has, and there is one place.
func (p *Provider) CreateVolume(ctx context.Context, name string, sizeGiB int, dc string) (*providers.Volume, error) {
	if name == "" {
		return nil, fmt.Errorf("a volume needs a name")
	}
	if strings.ContainsAny(name, `/\`) {
		return nil, fmt.Errorf("volume name %q must be a single path segment", name)
	}
	dir := p.volumePath(name)
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("volume %q already exists at %s", name, dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &providers.Volume{Provider: "docker", ID: name, Name: name, DataCenter: "local"}, nil
}

// DeleteVolume removes an EMPTY volume directory and refuses a populated one.
//
// This is deliberately stricter than the RunPod backend, where the API refuses a
// volume that is still attached and the volume itself is a managed resource
// rather than a path. Here the directory holds the box's real state: tool
// logins, transcripts, anything the work trees are not. `os.Remove` on a
// non-empty directory fails, and that failure is the guard, so removal is never
// a recursive delete megh performs on the user's behalf.
func (p *Provider) DeleteVolume(ctx context.Context, id string) error {
	dir := p.volumePath(id)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("no local volume %q at %s", id, dir)
	}
	if err := os.Remove(dir); err != nil {
		return fmt.Errorf("volume %q is not empty; it holds the box state (logins, transcripts). "+
			"Remove %s yourself if you are sure", id, dir)
	}
	return nil
}

// dirSizeGiB is a best-effort rounded-up size for the listing. It walks rather
// than shelling out to du, and a walk error yields 0 rather than failing the
// whole listing over one unreadable file.
func dirSizeGiB(dir string) int {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // an unreadable entry should not fail the listing
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	const giB = 1 << 30
	if total == 0 {
		return 0
	}
	if total < giB {
		return 1
	}
	return int((total + giB - 1) / giB)
}
