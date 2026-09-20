# megh — Architectural Constraints

Enforceable rules for megh. `/stack-audit` checks these as its highest-priority
finding category. Push back before violating one; if a change genuinely needs to,
update or remove the constraint deliberately rather than working around it.

**A Verify that names a `go test -run` pattern can pass while proving nothing.**
`go test -run` whose pattern matches no test prints `ok <pkg>  0.4s [no tests to
run]` and exits 0, so a Verify still naming a test that was renamed or deleted
reads as green. It has already happened here: an over-wide edit deleted three
tests, the gate stayed green because deleted tests do not fail, and C3's Verify
went on reporting `ok` for two tests that no longer existed. When running a
Verify, read the output for `[no tests to run]`, not just the exit code.

**A constraint's own grep can be tripped by the code that enforces it.** A deny
list must spell the name it denies, and so must a test asserting the name never
travels. Both read as violations to a naive `grep`. The fix is to make the grep
precise (exclude `_test.go`, keep the names in one permitted package), never to
drop the check. C5 carries a worked example.

## C1: The `megh-` prefix is an internal marker, never a user-facing name

RunPod has no pod tags, so megh stores each pod with a `megh-` name prefix as the
only durable marker it can filter its own boxes on (`ManagedPods`). That prefix
must stay invisible to the user. It is not what they type, not what megh prints,
and not the box's Tailscale hostname.

Concretely:

- **Lookups accept the bare name.** `providers.Find` resolves an id, the full
  stored name, or the bare (unprefixed) name via `Box.DisplayName()`, so
  `megh ssh <name>` / `doctor` / `down` never require the prefix. It is a shared
  helper over `Provider.List` rather than a method precisely so a new backend
  cannot reimplement this rule and drift from it.
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

**Verify:** `go test ./internal/providers/ -run 'TestFindAcceptsTheBareName|TestFindIgnoresUnmanagedBoxes'`,
which covers both halves: a bare name resolves, and an unprefixed box does not.
Both have been red-checked (drop the `DisplayName()` clause from `Find` and the
first fails; drop the `Managed` filter and the second fails). Also
`grep -n 'TS_HOSTNAME' internal/providers/runpod/runpod.go` must show
`ShortName(`. Then audit new `pod.Name`/`b.Name` uses in `cmd/`
(`grep -rn 'pod\.Name\|b\.Name' cmd/*.go`): each should be a raw-name context
(a provider API call, `--all` listing, uniqueness check) rather than a
user-facing display or tailnet address, which use `DisplayName()`.

## C2: Tailscale bring-up has one source of truth

The logic to bring a box onto the tailnet (start `tailscaled` in userspace, run
`tailscale up`, and `tailscale serve` the surfaces) lives ONLY in
`internal/tsops/ts-up.sh`, embedded in the megh binary. The entrypoint must not
re-implement it inline; it runs the helper via `megh mesh join --local`.
`megh mesh join|status|logs|restart` pipes the same embedded bytes over SSH.
This keeps boot and repair identical and lets those verbs work on boxes built
from older images (the script rides in the CLI, not the box).

The command moved (it was `megh doctor ts start --local` until 2026-09-20) and
may move again; what this constraint fixes is that ONE copy of the logic exists
and both paths run it, not what the copy is called.

New tailscale bring-up or serve logic goes in `ts-up.sh`, not in `entrypoint.sh`
or a Go string literal.

**Verify:** `go test ./ -run TestEntrypointDelegatesBringUpToTheMeshCommand` (the
entrypoint delegates rather than reimplementing) and `grep -qE 'tailscale.*\bup\b' internal/tsops/ts-up.sh &&
grep -q 'serve --bg' internal/tsops/ts-up.sh` (the helper is where bring-up +
serve live; serve goes through the `ts` wrapper). The only bare `tailscale` call
left in `entrypoint.sh` should be the shutdown `logout` in the SIGTERM trap; there
must be no `tailscaled --tun` or `tailscale up`/`serve --bg` invocation there
(matches in comments or `log "…"` strings don't count).

## C3: a provider credential reaches a box only by deliberate elevation

**Relaxed 2026-09-12.** This used to read "megh never sends a provider credential
to a box" and treated a box holding one as a defect with no legitimate case. That
assumed the control plane is a device you physically hold and every box is a work
box. Development moved predominantly off the Mac, so the machine running `megh up`
is now itself a box, and a rule that forbids the thing being done daily does not
survive contact — it gets worked around, which is how the old `box-envvars` came
to hold a live `RUNPOD_API_KEY` for months under a header claiming it held none.
State the real rule instead.

**Simplified 2026-09-14.** The relaxation left behind a second signal,
`MEGH_CONTROL_PLANE`, plus a `--i-am-the-control-plane` flag and a `megh up`
guard that refused to launch without one of them. Both are gone. **Holding a
launch-capable provider credential IS being the control plane** — there is
nothing further to declare, and a machine that has the key but has not said so
was never a state worth distinguishing.

The declaration existed because the key was in the every-box channel: with
`RUNPOD_API_KEY` in `box-envvars`, every box held it, so "holds the key" proved
nothing about intent and a separate, non-propagating variable had to carry the
intent instead. That is a workaround for a misplaced credential, not a control.
Fix the channel and it has no work left to do. What decides elevation is the
channel the key arrives by, which is the rest of this constraint.

The cost of dropping it is real and worth stating: nothing in the code now stops
a box that holds the key from launching boxes. That was always true of the key
itself — the guard only ever caught the case where the key was already somewhere
it should not have been.

**What did not change: megh never sends one on its own.** Elevation is something
you do deliberately, to one box, by a channel you name. It is never something
megh infers, and no allowlist below is loosened:

- `megh enable` forwards only `MEGH_`-prefixed environment (`meghEnv` in
  `cmd/enable.go`), never the ambient environment.
- `files:` copies only what `megh.yaml` names.
- `box_envs:` is an explicit opt-in list, never a wildcard.
- **`providers.docker.mounts:` is the local backend's allowlist.** A bind mount is
  a fourth channel to a box, and a wider one than the other three: it exposes a
  live host path rather than a copied value. Every `-v` the docker backend passes
  comes from that map or is the work mount itself. Nothing is inferred from the
  ambient environment, the working directory, or what happens to exist next to a
  mounted path.

New code that ships environment, files, scripts, or MOUNTS to a box still passes
an allowlist, never the caller's whole environment or filesystem.

### What elevation means, and where it is safe

A credential on a box can terminate and launch every other box on the account.
That cost does not go away; what changed is that it is now sometimes worth paying.
The axis that decides it is **who owns the hardware**, not which command is being
run:

- **A local (docker) box runs on a machine you physically hold.** The credential
  is already on that machine — the container is a boundary around the rest of your
  filesystem, not around your secrets (CLAUDE.md: "a local box is not a security
  sandbox"). Elevating it adds no party who could not already read the key. This
  is the sanctioned case.
- **A cloud box runs on someone else's hardware.** Elevating it exposes the
  credential to the provider's host and to anyone who gets a shell on the pod,
  and the blast radius is every box on the account. Sanctioned only with a
  **restricted, read-only** provider key, and never with one that can launch or
  terminate.

`megh up` no longer asks. It launches if the provider credential is there and
fails with the provider's own "unauthorized" if it is not, which is the same
answer a laptop gets and needs no megh-specific concept. The enforcement lives
entirely in the channels below: a box you did not elevate has no key, so it
cannot spawn, and nothing had to be declared for that to be true.

`megh doctor control-plane` reports the elevation rather than gating it. On a box
with the key set, the `provider key` row reads "this box is elevated (C3)"; with
it unset, the fix line names the scoped channel instead of telling you to export
something.

### The channel matters as much as the box

`box-envvars` is the **every-box** channel: `files:` copies it to each box megh
touches, cloud and local alike. A provider credential placed there is therefore
elevated on every box, which is broader than any intent that motivated the
elevation, and it is silent — nothing at launch says a pod just received it.

Elevate through a channel scoped to the boxes meant to be elevated. For local
boxes that is `providers.docker.mounts:`, which the cloud backends never read.
Keep provider credentials out of `box-envvars`.

**Verify:** `go test ./internal/providers/docker/ -run 'TestRunArgsMountsOnlyWhatConfigAllows|TestRunArgsNeverSendsATailscaleKey'`
(the mount allowlist and the deny list, both red-checked). Then
`grep -n 'MEGH_' cmd/enable.go` must show the prefix filter in `meghEnv`, and
`grep -nE '^[[:space:]]*export[[:space:]]+(RUNPOD|VAST|LAMBDA)_API_KEY=' ~/personal/box-envvars`
must return NOTHING — not because a box may never hold the key, but because that
file reaches boxes this constraint has not elevated. **That grep is now the whole
enforcement**, not a supplement to a code guard, so it is the one to run when a
box turns out to be able to spawn and should not. The other `os.Environ()` uses
in `cmd/` are NOT violations: they set the environment of a LOCAL child process
(the ssh client in `sshexec.go`, the local bash in `doctorts.go --local`), and megh
never configures ssh `SendEnv`, so the calling shell's environment is not forwarded
to a box. Confirm that with `grep -rn 'SendEnv' cmd/ internal/`, which must return
nothing.

Tailnet control-plane credentials are a separate and stricter case: they are
denied by name regardless of elevation. See C5.

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
  box needs it to join the tailnet. It can enrol a machine, nothing more. The
  same is true of a key megh mints per box (`tailscale.mint_keys`): still a node
  key, still goes to the box, and single-use plus short-lived on top, so it is
  strictly less exposure than the shared static one.
- `MEGH_TAILSCALE_API_KEY`, or the preferred `MEGH_TAILSCALE_CLIENT_ID` +
  `MEGH_TAILSCALE_CLIENT_SECRET` pair, is a CONTROL-PLANE credential. It can enumerate and DELETE
  every node on the tailnet, which is a wider blast radius than the RunPod key:
  it reaches machines megh never created, including your laptop and phone.

The API key is used only by the control machine, in `internal/tsapi`, for
`megh down`, `megh mesh gc`, and minting per-box node keys in `megh up`.
It must never reach a box. Note the asymmetry that makes this easy to get wrong:
`megh up` uses the API key to PRODUCE something the box does receive, so the
minted key travels while the credential that made it does not. This is C3's
reasoning applied to a credential that is not a provider key, so C3's letter
does not cover it while its spirit plainly does.

Concretely: never add it to `box_envs:`, never name it in `files:`, never put it
in the pod env map in `internal/providers/runpod/runpod.go` or the container env
in `internal/providers/docker/docker.go`, and never let a feature script read it.
All three names begin with `MEGH_`, which is exactly the prefix `meghEnv` in
`cmd/enable.go` forwards, so each must be denied. Adding a fourth control-plane
variable means adding it to the list; the prefix rule makes leaking it the
default, not the accident.

**There is ONE deny check**, `config.IsControlPlaneSecret`, used by both
`meghEnv` and the docker backend's box env. It was briefly the union of two
lists, `controlPlaneSecrets` plus a `boxDeniedEnv` holding `MEGH_CONTROL_PLANE`;
that variable is gone (C3, simplified 2026-09-14) and the union collapsed back to
this constraint's tailnet credentials alone. If a future variable needs denying
for a reason other than being a credential, split the lists again rather than
widening this one — THIS constraint's Verify greps for the tailnet names and
should keep meaning exactly what it says. The list started as a private map in
`cmd/enable.go`; when the docker backend needed the same rule, copying it would
have created two lists that could drift, and spelling the names inside
`internal/providers/` would have tripped this constraint's own grep. It lives in
`internal/config`, which is one of the packages named below and already knows
these variables by name.

**Verify:** `go test ./cmd/ -run TestMeghEnvNeverForwardsTheTailscaleAPIKey`
(fails if any survives `meghEnv`) and
`go test ./internal/providers/docker/ -run TestRunArgsNeverSendsATailscaleKey`
(fails if any reaches a container's env). Then
`grep -rn --include='*.go' MEGH_TAILSCALE env/ internal/features/ internal/providers/ | grep -v _test.go`
and `grep -rn 'TAILSCALE_API' internal/providers/ | grep -v _test.go` must both
return nothing. The key may appear only in `internal/tsapi/`, `internal/config/`,
`cmd/`, the docs, and a TEST asserting it never travels: a deny-list test has to
spell the name it denies, and excluding tests from the grep is what keeps that
from reading as the violation it is the opposite of.

## C6: a shared artifact never encodes one machine's paths

A path that resolves on exactly one machine must not reach a file that more than
one machine reads. The Mac is where this drift originates, and not because it is
special: it is the only machine here that is never rebuilt. A box-specific
assumption dies at the next `megh up`, loudly, within a day. A Mac-specific one
is load-bearing for months, because nothing ever tears down the Mac to expose
it. So every shared artifact slowly acquires the shape of the one machine that
never gets rebuilt, and the box being disposable is what tests the config.

This was five separate gotcha entries before it was one constraint. All five are
the same failure:

- `~/.zshrc` **set** `PATH` rather than appending, so a box got the Mac's list
  (`/Users/<you>/.lmstudio/bin` on a Linux box is the tell). Fixed.
- `~/personal`'s entries are absolute symlinks into the Mac's dotfiles checkout,
  and the entrypoint REPLACES a dangling symlink rather than skipping it: read-only
  the `ln` kills PID 1, read-write it rewrites the Mac's copy. Hence the
  `/root/personal-mac` mount.
- megh's own `repos:` entry is leafless "because that is where the Mac keeps it",
  and gap-tracker's was not, so its skill resolved on a cloud box and nowhere else.
- The dotfiles repo carried two git-tracked symlinks pointing at
  `/Users/<user>/newstack/gap-tracker/...`, so the `gaps` skill and `/gap-track`
  existed only on the Mac. That is what sent us looking.
- The LM Studio installer appended a Mac path to `shared/zshrc` AND
  `shared/bashrc`, which every box mounts.

**A hostname can be machine-local too, and that is the same bug.** `megh.yaml`
carried `portal.repo: git@panyam-github:panyam/dotfiles.git`. `panyam-github` is
an `~/.ssh/config` Host alias defined only on the Mac (it keeps the personal
GitHub account apart from an enterprise one there), so `megh portal` worked on
the Mac and died on every box with "Could not resolve hostname panyam-github".
Nothing about it is a path, and the first version of this constraint's check
sailed straight past it. `aliasedURL` compounded it: that function rewrites
`git@github.com:` to a per-identity alias and returns anything else untouched,
so the alias form was passed through verbatim rather than corrected. A real host
is a FQDN and has a dot; an SSH url whose host has none is an alias.

The rule is not "avoid absolute paths". A path may be absolute when it names
something the box genuinely has (`/mnt/work`, `/usr/local/bin`, `/etc/megh`).
The test is whether the path exists on every machine that reads the file. Use
`$HOME`, a repo-relative path, `${CLAUDE_PLUGIN_ROOT}` in a Claude Code plugin,
or derive it at run time.

**Per-machine files are the escape hatch, and they must be named as such.** The
dotfiles repo has `gmmac/` for the Mac and `box/` for boxes; an absolute path in
`gmmac/` is correct by construction. `.machine-paths-allow` exempts that one
directory and nothing else. A shared file needing a real home path is a `$HOME`
fix, never a new exemption.

**Verify:** `go test ./ -run TestNoMachineLocalPathsInTrackedFiles -count=1` (fails on a
tracked symlink that is absolute or escapes the repo, on a per-user home path in
tracked text, and on an SSH url naming a dotless host). Rule 3 skips `_test.go`
and the placeholder hosts documentation uses (`git@host:owner/repo`), because a
parser test and a doc comment must both be able to spell the form they describe
— the narrowing C5 prescribes, never dropping the check. The dotfiles repo runs the same two rules as
`shared/checks/no-machine-paths.sh` in CI, because that repo is mounted on every
box and is where this drift lands first. Both spell the pattern they forbid, so
each exempts itself — the trap this file's preamble describes.

`-count=1` is not optional here. The tracked-file list comes from `git`, which
Go's test cache cannot see through, so a stale `ok (cached)` is possible after
adding a file. That is this file's preamble in a second costume: the gate was
caching a pass while a planted absolute symlink sat in the index.
