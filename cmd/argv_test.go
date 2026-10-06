package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The reported case, from a phone: every command failed with
// `unknown command "/data/data/com.termux/files/usr/bin/megh"`.
func TestStripLinkerArg(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "megh")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"self path repeated", []string{bin, bin, "profile", "list"}, []string{bin, "profile", "list"}},
		{"bare argv0", []string{"megh", bin, "up", "x"}, []string{bin, "up", "x"}},
		{"argv0 is the linker", []string{"/system/bin/linker64", bin, "list"}, []string{bin, "list"}},
		{"normal invocation", []string{bin, "list"}, []string{bin, "list"}},
		{"no args", []string{bin}, []string{bin}},
		// A real absolute-path argument that is not megh must survive.
		{"other absolute path", []string{bin, "--config", "/etc/megh/megh.yaml"}, []string{bin, "--config", "/etc/megh/megh.yaml"}},
		{"same name but missing", []string{"megh", "/nope/megh", "list"}, []string{"megh", "/nope/megh", "list"}},
	}
	for _, c := range cases {
		if got := stripLinkerArg(c.in); !slices.Equal(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
