# megh — Architectural Constraints

Enforceable rules for megh. `/stack-audit` checks these as its highest-priority
finding category. Push back before violating one; if a change genuinely needs to,
update or remove the constraint deliberately rather than working around it.

## C1: The `megh-` prefix is an internal marker, never a user-facing name

RunPod has no pod tags, so megh stores each pod with a `megh-` name prefix as the
only durable marker it can filter its own boxes on (`ManagedPods`). That prefix
must stay invisible to the user. It is not what they type, not what megh prints,
and not the box's Tailscale hostname.

Concretely:

- **Lookups accept the bare name.** `runpod.Find` resolves an id, the full pod
  name, or the bare (unprefixed) name via `Pod.DisplayName()`, so
  `megh ssh <name>` / `doctor` / `down` never require the prefix.
- **The tailnet hostname is the bare name.** `TS_HOSTNAME` is set from
  `ShortName(o.Name)`, so a box joins the tailnet as `<name>`, not `megh-<name>`.
  Anything reaching a box by MagicDNS (`dialFor` tailnet path) must use the bare
  name to match.
- **Display uses the bare name.** Every place a command prints a box name uses
  `Pod.DisplayName()` (or `runpod.ShortName`). The one exception is
  `megh list --all`, which keeps raw pod names so megh boxes stay distinct from
  foreign pods on the account.

New code that reaches a box by name, prints a box name, or sets the tailnet
hostname routes through `ShortName`/`DisplayName`. It never hard-codes the prefix
or passes a raw `Pod.Name` to a user-facing string or a tailnet address.

**Verify:** `grep -n 'TS_HOSTNAME' internal/providers/runpod/runpod.go` must show
`ShortName(`, and `grep -n 'DisplayName() == idOrName' internal/providers/runpod/list.go`
must match (Find is bare-name aware). Then audit new `pod.Name`/`p.Name` uses in
`cmd/` (`grep -rn 'pod\.Name\|p\.Name' cmd/*.go`): each should be a raw-name
context (RunPod API call, `--all` listing, uniqueness check) rather than a
user-facing display or tailnet address, which use `DisplayName()`.

## C2: Tailscale bring-up has one source of truth

The logic to bring a box onto the tailnet (start `tailscaled` in userspace, run
`tailscale up`, and `tailscale serve` the surfaces) lives ONLY in
`internal/tsops/ts-up.sh`, embedded in the megh binary. The entrypoint must not
re-implement it inline; it runs the helper via `megh doctor ts start --local`.
`megh doctor ts` pipes the same embedded bytes over SSH. This keeps boot and
repair identical and lets `doctor ts` work on boxes built from older images
(the script rides in the CLI, not the box).

New tailscale bring-up or serve logic goes in `ts-up.sh`, not in `entrypoint.sh`
or a Go string literal.

**Verify:** `grep -q 'megh doctor ts start --local' env/base/entrypoint.sh` (the
entrypoint delegates) and `grep -qE 'tailscale.*\bup\b' internal/tsops/ts-up.sh &&
grep -q 'tailscale.*serve' internal/tsops/ts-up.sh` (the helper is where bring-up
lives). The only bare `tailscale` call left in `entrypoint.sh` should be the
shutdown `logout` in the SIGTERM trap; there must be no `tailscaled --tun` or
`tailscale up/serve` invocation there (matches in comments or `log "…"` strings
don't count).
