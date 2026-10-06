package cmd

import (
	"os"
	"path/filepath"
	"strings"
)

// stripLinkerArg undoes Termux's system-linker exec. Android forbids executing
// files from app data, so termux-exec runs `megh args...` as
// `/system/bin/linker64 /data/.../bin/megh args...`, and a static Go binary
// started that way sees its own path as the first argument. cobra then reports
// `unknown command "/data/data/com.termux/files/usr/bin/megh"` for every command.
//
// The extra argument is dropped only when it is an absolute path to an existing
// file named like this program (or argv[0] is the linker itself). No megh
// command takes megh's own path as its first argument, so this cannot eat a
// real one.
func stripLinkerArg(args []string) []string {
	if len(args) < 2 || !filepath.IsAbs(args[1]) {
		return args
	}
	self := filepath.Base(args[0])
	viaLinker := strings.HasPrefix(self, "linker")
	if !viaLinker && filepath.Base(args[1]) != self {
		return args
	}
	if fi, err := os.Stat(args[1]); err != nil || fi.IsDir() {
		return args
	}
	return append([]string{args[1]}, args[2:]...)
}
