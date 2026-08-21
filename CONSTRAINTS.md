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
grep -q 'serve --bg' internal/tsops/ts-up.sh` (the helper is where bring-up +
serve live; serve goes through the `ts` wrapper). The only bare `tailscale` call
left in `entrypoint.sh` should be the shutdown `logout` in the SIGTERM trap; there
must be no `tailscaled --tun` or `tailscale up`/`serve --bg` invocation there
(matches in comments or `log "…"` strings don't count).

## C3: megh never sends a provider credential to a box

A box with `RUNPOD_API_KEY` can terminate and launch your OTHER boxes, so nothing
megh copies to a box may carry one. This is an invariant across every channel
megh has, not a property of any one command:

- `megh enable` forwards only `MEGH_`-prefixed environment (`meghEnv` in
  `cmd/enable.go`), never the ambient environment.
- `files:` copies only what `megh.yaml` names, which is why the pattern is a
  scoped `~/personal/box-envvars` rather than the real `~/personal/envvars`.
- `box_envs:` is an explicit opt-in list, never a wildcard.

New code that ships environment, files, or scripts to a box passes an allowlist,
never the caller's whole environment.

**Verify:** `grep -n 'MEGH_' cmd/enable.go` must show the prefix filter in
`meghEnv`. The other `os.Environ()` uses in `cmd/` are NOT violations: they set
the environment of a LOCAL child process (the ssh client in `sshexec.go`, the
local bash in `doctorts.go --local`), and megh never configures ssh `SendEnv`, so
the calling shell's environment is not forwarded to a box. Confirm that with
`grep -rn 'SendEnv' cmd/ internal/`, which must return nothing. What must never
happen is a provider key reaching a box through pod env (`box_envs`), a `files:`
copy, or a script piped over SSH.

## C4: Every box service binds loopback

RunPod's public proxy is open and unauthenticated, and only `22/tcp` is meant to
be reachable. A feature that binds a wildcard address therefore puts a dev
service (a root shell, a database, a metrics store) on the public internet.

Every service a feature script starts binds `127.0.0.1` and is reached over
Tailscale (`tailscale serve`, for HTTP surfaces) or an SSH tunnel. This is not a
per-feature judgment call: it applies to HTTP surfaces, databases, and anything
else that listens.

**Verify:** `go test ./internal/features/ -run TestFeatureScriptsBindLoopback`.
The test scans every embedded feature script for a wildcard bind (`0.0.0.0`,
`[::]`, or `bind *`) outside a comment and fails the build on a match, so this is
enforced in CI rather than by review.

## C5: the Tailscale API key stays on the control machine

megh holds two Tailscale secrets and they are not interchangeable.

- `TS_AUTHKEY` is a NODE auth key. It is sent to a box as pod env, because the
  box needs it to join the tailnet. It can enrol a machine, nothing more.
- `MEGH_TAILSCALE_API_KEY` is a CONTROL-PLANE token. It can enumerate and DELETE
  every node on the tailnet, which is a wider blast radius than the RunPod key:
  it reaches machines megh never created, including your laptop and phone.

The API key is used only by the control machine, in `internal/tsapi`, for
`megh down` and `megh doctor ts gc`. It must never reach a box. This is C3's
reasoning applied to a credential that is not a provider key, so C3's letter
does not cover it while its spirit plainly does.

Concretely: never add it to `box_envs:`, never name it in `files:`, never put it
in the pod env map in `internal/providers/runpod/runpod.go`, and never let a
feature script read it (the `MEGH_` prefix means `meghEnv` in `cmd/enable.go`
WOULD forward it, so a feature must not want it).

**Verify:** `go test ./cmd/ -run TestMeghEnvNeverForwardsTheTailscaleAPIKey`,
which fails if the key ever survives `meghEnv`. Also
`grep -rn 'MEGH_TAILSCALE_API_KEY' env/ internal/features/` and
`grep -rn 'TAILSCALE_API' internal/providers/` must both return nothing. The key
may appear only in `internal/tsapi/`, `internal/config/`, `cmd/`, and the docs.
