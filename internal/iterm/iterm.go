// Package iterm installs and uses an iTerm2 profile tuned for megh ssh/tmux.
package iterm

import (
	"crypto/sha1"
	"fmt"
)

const (
	// ProfileName is the default iTerm2 profile title when megh.yaml is silent.
	ProfileName = "megh"
)

// profileGUIDFor returns a stable Guid for dynamic profiles derived from name.
func profileGUIDFor(name string) string {
	sum := sha1.Sum([]byte("megh-iterm-profile:" + name))
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}
