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

The command has moved before (it used to be `megh doctor ts start --local`) and
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

This rule used to read "megh never sends a provider credential to a box", which
assumed the control plane is always a device you physically hold. Once the
machine running `megh up` can itself be a box, a rule forbidding that gets worked
around instead of followed, which is how an every-box env file once carried a
live provider key under a header claiming it held none. So the rule states what
actually decides it: the channel the key arrives by.

**Holding a launch-capable provider credential IS being the control plane.**
There is no separate declaration (an earlier `MEGH_CONTROL_PLANE` flag and
`megh up` guard were removed). That declaration only existed because the key sat
in the every-box channel, where "holds the key" proved nothing about intent; it
was a workaround for a misplaced credential, not a control. Fix the channel and
it has no work left to do.

The cost is worth stating: nothing in the code stops a box that holds the key
from launching boxes. That was always true of the key itself, and the old guard
only caught the case where the key was already somewhere it should not have been.

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

Any env file listed in `files:` is the **every-box** channel: megh copies it to
each box it touches, cloud and local alike. A provider credential placed there
is therefore elevated on every box, which is broader than any intent that
motivated the elevation, and it is silent, since nothing at launch says a pod
just received it.

Elevate through a channel scoped to the boxes meant to be elevated. For local
boxes that is `providers.docker.mounts:`, which the cloud backends never read.
Keep provider credentials out of every file `files:` copies.

**Verify:** `go test ./internal/providers/docker/ -run 'TestRunArgsMountsOnlyWhatConfigAllows|TestRunArgsNeverSendsATailscaleKey'`
(the mount allowlist and the deny list, both red-checked). Then
`grep -n 'MEGH_' cmd/enable.go` must show the prefix filter in `meghEnv`, and
`grep -nE '^[[:space:]]*export[[:space:]]+(RUNPOD|VAST|LAMBDA)_API_KEY=' <every-box env file>`
(the local source of each env file your `files:` entry copies) must return
NOTHING, not because a box may never hold the key, but because that file reaches
boxes this constraint has not elevated. **That grep is now the whole
enforcement**, not a supplement to a code guard, so it is the one to run when a
box turns out to be able to spawn and should not. The other `os.Environ()` uses
in `cmd/` are NOT violations: they set the environment of a LOCAL child process
(the ssh client in `sshexec.go`, the local bash in `doctorts.go --local`), and megh
never configures ssh `SendEnv`, so the calling shell's environment is not forwarded
to a box. Confirm that with `grep -rn 'SendEnv' cmd/ internal/`, which must return
nothing.

**Recommended practice: elevate no box.** Launch from a phone (SETUP.md §6) or
from meghplane (SETUP.md §7), and reach boxes from other machines with a key in
`extra_pubkeys:` rather than by giving those machines megh credentials. The
elevation rules above describe what is sanctioned, not what you need. If a box
must launch for a while, put the keys in a RAM-only file (`/dev/shm`), never on
the volume, so they die with the box.

meghplane is not a box. It runs on App Engine, holds a key only in Secret
Manager when `serve.secret` is set, and otherwise takes keys from the browser per
request. **Keep that key set minimal**: `RUNPOD_API_KEY` alone is enough (the
Tailscale pair is optional, since a box can join the tailnet later from a phone),
stored in one place, and nothing else (service tokens, SSH keys) ever goes to
meghplane. Secret Manager holds only what a machine must read unattended;
everything a person uses stays in their password manager (e.g. Bitwarden).

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

The sweep is a NEGATIVE test, so a script that binds nothing recognisable passes
it. A feature that writes a launcher rather than starting a service therefore
owes a positive assertion too, naming the loopback address it binds:
`TestPlaywrightViewerBindsLoopback` is the shape, for the `pw-ui` script inside
playwright.sh.

**One exception: the gateway container** (`megh gateway`, DESIGN.md "Roles").
Its proxy binds the container's `0.0.0.0`, because a docker publish forwards to
the container's `eth0` and a loopback bind inside would accept nothing (the same
measurement that makes 22 the only publishable port on a local box). What keeps
it off the network is the other end of the publish, which is always the HOST's
`127.0.0.1`. **Verify:** `go test ./cmd -run TestGatewayPublishesOnlyToTheHostsLoopback`.

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
that variable is gone (see C3) and the union collapsed back to
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
one machine reads. The long-lived host machine (usually a laptop) is where this
drift originates, and not because it is special: it is the one machine that is
never rebuilt. A box-specific assumption dies at the next `megh up`, loudly,
within a day. A host-specific one can be load-bearing for months, because
nothing ever tears the host down to expose it. So every shared artifact slowly
acquires the shape of the one machine that never gets rebuilt, and the box being
disposable is what tests the config.

The shapes it takes, all the same failure:

- A shared `~/.zshrc` **sets** `PATH` rather than appending, so a box gets the
  host's list (a macOS-only path on a Linux box is the tell).
- A dotfiles-managed directory whose entries are absolute symlinks into the
  host's dotfiles checkout gets mounted into a box. The entrypoint REPLACES a
  dangling symlink rather than skipping it: read-only the `ln` kills PID 1,
  read-write it rewrites the host's copy. Mount such a tree at a path nothing
  symlinks into instead.
- A `repos:` entry whose `dir` mirrors where the host happens to keep that repo,
  rather than where every box expects it.
- A dotfiles repo with git-tracked symlinks pointing into one machine's home
  directory, so whatever they link to exists on that machine only.
- A tool installer appending a host-only path to a shared rc file that every box
  reads.

**A hostname can be machine-local too, and that is the same bug.** A
`portal.repo` of the form `git@gh-alias:you/dotfiles.git`, where `gh-alias` is an
`~/.ssh/config` Host alias defined only on one machine (for example to keep two
GitHub accounts apart), works there and dies on every box with "Could not
resolve hostname gh-alias". Nothing about it is a path, and a check that only
looks for paths sails straight past it. `aliasedURL` compounded it: that function rewrites
`git@github.com:` to a per-identity alias and returns anything else untouched,
so the alias form was passed through verbatim rather than corrected. A real host
is a FQDN and has a dot; an SSH url whose host has none is an alias.

The rule is not "avoid absolute paths". A path may be absolute when it names
something the box genuinely has (`/mnt/work`, `/usr/local/bin`, `/etc/megh`).
The test is whether the path exists on every machine that reads the file. Use
`$HOME`, a repo-relative path, `${CLAUDE_PLUGIN_ROOT}` in a Claude Code plugin,
or derive it at run time.

**Per-machine files are the escape hatch, and they must be named as such.** A
dotfiles repo might keep `hosts/<machine>/` for one machine and `box/` for boxes;
an absolute path in `hosts/<machine>/` is correct by construction. An allowlist
file (like `.machine-paths-allow`) should exempt that one directory and nothing
else. A shared file needing a real home path is a `$HOME` fix, never a new
exemption.

**Verify:** `go test ./ -run TestNoMachineLocalPathsInTrackedFiles -count=1` (fails on a
tracked symlink that is absolute or escapes the repo, on a per-user home path in
tracked text, and on an SSH url naming a dotless host). Rule 3 skips `_test.go`
and the placeholder hosts documentation uses (`git@host:owner/repo`), because a
parser test and a doc comment must both be able to spell the form they describe
(the narrowing C5 prescribes, never dropping the check). A dotfiles repo that
boxes mount is where this drift lands first, so the same rules are worth running
in its CI too. Any such check spells the pattern it forbids and so has to exempt
itself, which is the trap this file's preamble describes.

`-count=1` is not optional here. The tracked-file list comes from `git`, which
Go's test cache cannot see through, so a stale `ok (cached)` is possible after
adding a file. That is this file's preamble in a second costume: the gate was
caching a pass while a planted absolute symlink sat in the index.
