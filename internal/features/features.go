// Package features holds the on-demand capability scripts for `megh enable`.
// They are embedded in the megh binary, so `megh enable <name>` works from the
// control machine (piped to the box over SSH) or on a box (`--local`), without
// depending on the box's image having them baked in.
package features

import (
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed *.sh
var scripts embed.FS

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
	return b, nil
}
