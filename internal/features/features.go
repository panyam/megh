// Package features holds the on-demand capability scripts for `megh enable`.
// They are embedded in the megh binary, so `megh enable <name>` works from the
// control machine (piped to the box over SSH) or on a box (`--local`), without
// depending on the box's image having them baked in.
package features

import (
	"bytes"
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed *.sh
var scripts embed.FS

// Vendored web assets inlined into feature scripts at assembly time (see
// assetMarkers). Kept in the binary so a script piped over SSH is fully
// self-contained — no CDN or network dependency on the box or the client. Only
// the assets are embedded; the version manifest, checksums, and update.sh next
// to them stay out of the binary. Versions are pinned in vendor/versions.env and
// their integrity is enforced by vendor_test.go (refresh with vendor/update.sh).
//
//go:embed vendor/xterm.js vendor/xterm.css vendor/addon-fit.js
var vendorFS embed.FS

// assetMarkers maps a placeholder token in a feature script to the vendored
// asset whose bytes replace it. webterm.sh uses these to inline xterm.js.
var assetMarkers = map[string]string{
	"@@XTERM_CSS@@": "vendor/xterm.css",
	"@@XTERM_JS@@":  "vendor/xterm.js",
	"@@FIT_JS@@":    "vendor/addon-fit.js",
}

// List returns the available feature names, sorted.
func List() []string {
	entries, _ := scripts.ReadDir(".")
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sh") {
			names = append(names, strings.TrimSuffix(e.Name(), ".sh"))
		}
	}
	sort.Strings(names)
	return names
}

// Script returns the bytes of the named feature script.
func Script(name string) ([]byte, error) {
	b, err := scripts.ReadFile(name + ".sh")
	if err != nil {
		return nil, fmt.Errorf("unknown feature %q", name)
	}
	// Inline any vendored assets the script references, so the bytes we hand to
	// SSH (or run locally) carry everything and need no network to run.
	for marker, path := range assetMarkers {
		if !bytes.Contains(b, []byte(marker)) {
			continue
		}
		asset, err := vendorFS.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("inlining %s into %q: %w", path, name, err)
		}
		b = bytes.ReplaceAll(b, []byte(marker), asset)
	}
	return b, nil
}
