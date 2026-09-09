// Package providers holds the vocabulary every megh backend speaks: a Box, a
// Volume, the launch Options, and the Provider interface itself. A backend is a
// sibling package (runpod, docker) that implements Provider; cmd/ talks only to
// this package, so a command works against any backend without knowing which.
//
// The types here are deliberately backend-neutral and carry no wire tags. A
// backend that unmarshals JSON does it into its own private struct and converts,
// so one provider's API shape never leaks into the type every other provider has
// to return.
package providers

import "strings"

// NamePrefix marks a box as megh-managed. RunPod has no pod tags/labels, so the
// name is the only durable identifier the CLI can filter on without a local
// state file. Docker does have labels and uses one, but keeps the prefix too so
// the display and lookup rules below hold identically on every backend.
//
// The prefix is internal. It is not what the user types, not what megh prints,
// and not the box's Tailscale hostname (CONSTRAINTS.md C1).
const NamePrefix = "megh-"

// Box is a provisioned dev box, summarized to what `megh list` and `megh ssh`
// need. A backend fills what it has: DataCenter is a real region on a cloud
// provider and "local" on docker, and CostPerHr is 0 where compute is free.
type Box struct {
	ID         string
	Name       string
	Status     string
	DataCenter string
	CostPerHr  float64
	PublicIP   string
	SSHPort    int    // host port mapped to the box's 22/tcp (0 until mapped)
	Image      string // image the box was created from
}

// SSHReady reports whether the box has a resolvable SSH endpoint yet.
func (b Box) SSHReady() bool { return b.PublicIP != "" && b.SSHPort != 0 }

// ShortName is a box's display and tailnet name: the stored name minus the megh-
// discovery prefix. Foreign resources (shown under `list --all`) have no prefix,
// so this passes them through unchanged.
func ShortName(name string) string { return strings.TrimPrefix(name, NamePrefix) }

// DisplayName is ShortName of this box: what the user typed at `up`, its
// Tailscale MagicDNS hostname, and how megh prints it.
func (b Box) DisplayName() string { return ShortName(b.Name) }

// PrefixName applies the megh- marker idempotently. Callers that need the stored
// name before a box exists (the uniqueness check in `up`) use this so they and
// the backend agree on the final name.
func PrefixName(name string) string {
	if strings.HasPrefix(name, NamePrefix) {
		return name
	}
	return NamePrefix + name
}

// Volume is a backend's scratch store: a RunPod network volume, or a host
// directory on docker. Size is GiB.
//
// Provider is the backend that owns it, and every backend must fill it. The
// cross-provider listing has no other way to say where a volume lives, and
// before this field existed `megh storage ls` printed the literal "runpod" in
// its PROVIDER column for every row.
type Volume struct {
	Provider   string
	ID         string
	Name       string
	DataCenter string
	Size       int
}
