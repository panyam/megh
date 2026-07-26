// Package tsops carries the Tailscale bring-up helper (ts-up.sh) embedded in the
// megh binary. It is the single source of truth for connecting a box to the
// tailnet and serving its surfaces: the entrypoint runs it at boot (via
// `megh doctor ts start --local`) and `megh doctor ts` pipes it over SSH to
// diagnose or repair a box, so both paths run identical bytes.
package tsops

import _ "embed"

//go:embed ts-up.sh
var script []byte

// Script returns the ts-up.sh helper bytes.
func Script() []byte { return script }
