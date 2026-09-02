# CLAUDE.md — megh

Disposable cloud dev boxes for agentic coding. Provider-abstracted CLI; RunPod
first (US), Hetzner next. Read `DESIGN.md` for the settled architecture and
`WORKFLOW.md` for the operational runbook. `SETUP.md` is first-run.

## Build / run

```
make build          # -> bin/megh
make install        # go install to GOBIN
make vars           # show which secrets are set (no values)
make test           # go vet + go test (incl. vendored-asset integrity check)
go build ./... && go vet ./...
```

The webterm page inlines vendored xterm.js/css (`internal/features/vendor/`),
pinned in `versions.env` and integrity-checked by `vendor_test.go` (+ CI). To
update them: `make vendor-check` (integrity + pinned->latest + a bump-readiness
verdict; a new xterm major is only READY once a stable `@xterm/addon-fit` peers
with it), bump `versions.env`, `make vendor` (re-fetch + rewrite `SHA256SUMS`),
verify, commit.

The Makefile sources `~/personal/envvars` for every recipe that needs a secret.
Run `megh` directly only after `source ~/personal/envvars`.

## Commands

```
megh up <name> [--volume <id> --dc <dc>] # launch; name is required + unique (= tailnet host)
megh list [--all]                 # megh boxes (name/status/dc/$hr/ssh); --all = every pod
megh ssh [name]                   # attaches tmux 'main' (same session webterm serves); --no-tmux, --session
megh browse [port]                # tunnel box web surfaces to localhost, print URLs
megh enable [feature]             # add webterm/vnc/playwright/code/lgtm to a box on demand
megh down [name] [-y]             # terminate a box (volume survives; leaves the tailnet first)
megh doctor [name]                # health probe: tailscale registered? surfaces up? scratch ok?
megh doctor ts <action> [name]    # tailscale ops: logs|status|start|stop|restart|setkey (setkey re-keys a box)
megh doctor ts gc [name...]       # delete tailnet nodes whose box is gone (control plane; needs MEGH_TAILSCALE_API_KEY)
megh storage list|create|rm       # network volumes, one global cross-provider view
megh regions list|probe|place     # find a DC that will actually rent (probe = real rent + immediate terminate)
megh hydrate [--check]            # clone repos onto a box's volume (or report drift)
megh profile create|use|list|show # profiles; profile gh add|list for GitHub identities
megh config                       # resolved settings + which secrets are set
megh registry ls                  # dev-env image tags
megh portal                       # publish a bookmarkable box+URL index (PORTAL.md) to a private repo; up/down auto-refresh
```

Make wrappers: `make up NAME=.. VOLUME=.. DC=..`, `make list`, `make ssh [BOX=..]`,
`make down [BOX=.. YES=1]`, `make storage-ls`, `make image` (push -> CI builds
image), `make registry`.

`megh up` defaults: `--provider` = `$MEGH_PROVIDER` else `runpod`; `--image` =
`$MEGH_IMAGE` else `ghcr.io/<namespace>/megh-<flavor>:latest` (flavor default
`slim`, so `ghcr.io/panyam/megh-slim:latest`; use `--flavor base` for frontend);
`--pubkey` = `$MEGH_PUBKEY` else
`~/.ssh/id_ed25519.pub`. `--volume`/`--dc` are still required (or
`$MEGH_VOLUME_ID`/`$MEGH_DC`) since placement is account-specific.

## Config (`megh.yaml`, checked in)

Settings live in `megh.yaml` (auto-discovered walking up from cwd, then
`~/.config/megh/megh.yaml`; override with `--config`/`$MEGH_CONFIG`). It holds
non-secret settings and **pointers** to secrets (env-var names), never secret
values, so it is safe in the repo. `megh config` shows the resolved settings and
which secrets are set (never values). `megh.yaml.example` + `secrets.env.example`
are the templates. Precedence for `megh up`: **flag > env var > megh.yaml >
built-in default**. So on a new machine: clone, `make install`, fill secrets in
the env, and `megh up` reads volume/DC/defaults from `megh.yaml`.

`requires:` declares env vars that must be set on the host: `envs` (megh blocks
`megh up` if any is missing) and `box_envs` (also copied into the box as pod env
for the repos/services there). `megh config` shows each as `set`/`MISSING`.

`persist:` entries may be prefixed `file:` or `dir:` to declare which they are
(`file:~/.gitconfig`). Do it for any FILE whose name has no second dot: the
fallback guess reads `.claude.json` as a file but `.gitconfig` as a directory,
and guessing wrong CREATES a directory where the file belongs, after which every
tool reports "Is a directory". `persist:` lists home paths the entrypoint symlinks onto the scratch volume
(`state/<name>`), so a one-time `claude login`/`codex login`/etc. survives box
rebuilds on the same volume. Entries may be dirs OR files: `~/.claude` (dir) plus
`~/.claude.json` (a FILE beside it holding claude's login/onboarding state — miss
it and interactive claude re-onboards each box). Passed as `MEGH_PERSIST`; defaults
to `~/.claude,~/.claude.json,~/.codex` when unset (old binaries too). Add a tool = add its
config/auth dir. This is per-volume; megh never handles the tokens (they live on
the volume). Cross-volume "baked" seeding was considered and deferred (approach B).

`symlinks:` maps home paths onto volume locations (`~/newstack -> repos/newstack`,
target relative to `/mnt/work` or absolute), so paths your local scripts expect
resolve on the box. Passed as `MEGH_SYMLINKS`. Same symlink primitive as `persist`
but orthogonal intent: `persist` keeps mutable tool STATE alive (auto slot,
migrates image defaults); `symlinks` MAPS paths to authoritative hydrated repo
trees (explicit target, no migration). It skips a link that already exists as real
content. Targets may be files or dirs and may not exist until `megh hydrate` runs.

`sync:` MIRRORS local directory trees onto a box over SSH (`local_dir: box_dir`,
on `megh ssh`/`hydrate`), deletes included. That is the difference from `files:`
and the reason it exists: skills and commands get reorganised on the laptop, and
a copy that only ever adds leaves the superseded ones on the box so both get
offered. Point it at `/mnt/work/...` to land on the volume and survive rebuilds.
It shells out to rsync with `-rlptz --no-owner --no-group`, NOT `-a`, because
the volume is NFS with root squashed and preserving ownership fails the whole
transfer.

`files:` copies LOCAL files onto a box over SSH (on `megh ssh`/`hydrate`, mode
0600) — rc files and **secret** files that must not live in a repo or image
(`local_path: box_path`). A `~/` box path is ephemeral (`/root`, re-copied each
connect); a `/mnt/work/` path persists. **Never copy a file with `RUNPOD_API_KEY`**
(a box with it can manage your other boxes). Split of concerns: versioned dotfiles
-> a repo via `repos:` + `symlinks:`; secrets/unversioned rc files -> `files:`.

## Profiles (`~/.megh/profiles/<name>/`)

A profile is a self-contained context so megh depends on nothing system-level.
It holds a dedicated **box key** (one; SSH into VMs), N **GitHub identity keys**
(`gh/<name>.key`), and `secrets.env` (values applied over ambient env; blanks
fall back to ambient). Keys are generated in-process via `oneauth/sshkeys`.
Active profile: `--profile` > `$MEGH_PROFILE` > `~/.megh/current` > `default`.

- **box key**: `megh ssh` connects with it; its pubkey is injected on `megh up`.
- **gh keys**: forwarded via a **scoped ssh-agent** (only the profile's keys, so
  a corporate key in your normal agent never reaches a third-party VM). On the
  box, `megh ssh`/`hydrate` write per-identity `~/.ssh/config` Host aliases
  (`gh-<name>`, `IdentitiesOnly`, pubkey only), and `hydrate` clones each repo
  via its alias. Private keys never touch the box.
- Repo -> identity: `megh.yaml` repo `key:`, else `default_gh_key`.
- Blast `~/.megh` and you lose only that profile's VM access (re-mintable). Set
  `MEGH_HOME=./.megh` for a repo-local store (gitignored).

## Secrets (env vars, NEVER in the repo — history is permanent)

- `RUNPOD_API_KEY` — provider access
- `GH_MEGH_TOKEN` — GHCR pull (classic PAT, `read:packages`); also set in RunPod
  console as the ghcr.io Container Registry Auth
- `TS_AUTHKEY` — optional, Tailscale NODE key (phone/tablet only); reusable +
  ephemeral. Goes to the box, which is how it joins the tailnet.
- `MEGH_TAILSCALE_CLIENT_ID` + `MEGH_TAILSCALE_CLIENT_SECRET` — optional,
  Tailscale CONTROL-PLANE credential, and the preferred form: it is what the
  console hands you (Settings > Trust credentials), it does not expire, and
  scoping it to `tag:megh` means it cannot touch a device that is not a megh
  box. `MEGH_TAILSCALE_API_KEY` is the older single-value form and holds either
  a PAT (`tskey-api-...`) or a bare OAuth secret. The pair wins when both are
  set, so a leftover PAT does not shadow a new credential. megh exchanges an
  OAuth secret for a short-lived token automatically. Lets `megh down` and
  `megh doctor ts gc` delete stale nodes, and `megh up` mint per-box keys when
  `tailscale.mint_keys` is on. Control machine ONLY, and never on a box:
  `meghEnv` denies all three by name despite the `MEGH_` prefix it forwards.
  How much damage a leaked one does depends on the form: an unscoped PAT can
  delete ANY node on the tailnet including your laptop's, while a credential
  scoped to `tag:megh` can only touch megh boxes. That is the main argument for
  the scoped pair over the PAT. See `CONSTRAINTS.md` C5.
- Agent transcripts need NO secret. They go to the `sessions.repo` in
  `megh.yaml`, pushed by the control machine with the profile's GitHub identity;
  nothing for that repo lives on a box.

## Architecture (one-liners; see DESIGN.md)

- Dev env is a container image built from `env/base/provision.sh` (single source
  of truth); two artifacts, container (RunPod) + VM (Hetzner, later).
- Two flavors from that one script via `MEGH_SLIM`: `base` (full, Playwright +
  code-server baked) and `slim` (lean, fast pull; no frontend stack; code-server
  background-installs to the box's local disk on boot). `megh up --flavor slim`.
- Provider abstraction is the CLI, not any provider's tooling. RunPod = REST API
  (`internal/providers/runpod`); its Terraform provider is too flaky.
- megh is **stateless**: the provider is the source of truth. No local state file.
  Managed resources identified by a `megh-` **name prefix** (RunPod has no tags).
- Canonical state = git (code) + `megh-sessions` git repo (agent transcripts).
  No restic. Scratch = per-provider network volume at `/mnt/work`, DC-pinned,
  shareable by multiple boxes in the same DC.

## Services on a box (no Docker)

RunPod pods cannot run containers (settled; see `DESIGN.md`), so services run
NATIVELY. postgres 18 + pgvector and redis are BAKED into both flavors (~11 MB,
~1% of the slim image); `megh enable postgres` / `megh enable redis` create the
cluster and install the control scripts. **Both keep data on the box's LOCAL
disk: these are dev boxes and a database is not expected to outlive one. Share
data with fixtures, not shared storage.** `pg dump` keeps logical dumps on the
volume if you do want a copy, and `pg reset` / `redisctl reset` wipe. The observability stack
stays OUT of the image at ~700 MB and caches on the volume instead. One postgres CLUSTER
with one DATABASE per project: per-database overhead is tiny, per-cluster memory
is not. Ports default to what the repos already expect (postgres **5433**, redis
**6399**), so a project connects with no config change.

```
pg start|stop|status|db add <name>|psql <db>     # postgres://<n>:<n>@127.0.0.1:5433/<n>
redisctl start|stop|status|cli                   # redis://127.0.0.1:6399
```

`megh enable lgtm` is the observability stack (Grafana + Loki + Tempo + Mimir
behind one OTel collector, OTLP on `:4317`/`:4318`, UI on `:3000`). One stack,
many projects: a project is a **tenant**, not another instance.

```
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
OTEL_EXPORTER_OTLP_HEADERS=X-Scope-OrgID=<project>
```

It does not start at boot; `lgtm start|stop|status|logs|tenants|purge <tenant>`
runs it on the box. Everything lives on the volume under `/mnt/work/state/lgtm`.
**Implementation lore and its gotchas: `internal/features/NOTES.md`.**

## Gotchas (things that bit us)

- **RunPod CPU pods CANNOT run containers.** No `cap_sys_admin`; `docker run`
  dies at `unshare: operation not permitted` even after `dockerd` is coaxed into
  starting. Testcontainers and `docker build` are impossible here. Full evidence
  and the native-services answer: `DESIGN.md`.
- **The volume is NFS with root squashed: `chown` is denied for EVERY uid,
  including root.** Measured. Root cannot hand a directory to another user there.
  But the export PRESERVES the creating process's uid, so a directory created BY
  that user is owned by it and needs no chown. (This bit us: postgres was briefly
  declared impossible on the volume when the real bug was creating the dir as
  root first.)
- **Container disk caps scale with instance size, and they MOVE.** Roughly 20-30
  GB at 2 vCPU, 40 at 4, 50+ at 8, but the real cap follows whichever CPU flavor
  is actually free at that instant, not the vCPU count you asked for. Measured in
  US-CA-2 within one minute on 2026-08-21: 30 GB accepted with no volume
  attached, then 20 GB was the stated maximum for the same 2 vCPU shape with the
  volume attached. The DB features keep data on this disk, which is why the
  default is 4 vCPU / 40 GB. This is the ephemeral OS disk, NOT scratch; real
  scratch is the 100 GB volume.
- **RunPod REST schema differs from its docs.** CPU pods use `computeType:"CPU"`,
  `vcpuCount`, `cpuFlavorIds` (enum `cpu3c/g/m`, `cpu5c/g/m`; c=2/g=4/m=8 GB per
  vCPU). `dataCenterIds` is an array, `ports` is an array, `env` is a map.
- **Placement: storage + CPU must coexist in one DC, and there's no reliable
  availability API.** "Flavor defined in a DC" != "rentable." Only a real rent
  attempt proves capacity (a failed one costs nothing). US-TX-3 was dry; US-CA-2
  worked. Volume-supporting US DCs are enumerated in WORKFLOW.md.
- **mosh is impossible on RunPod. SETTLED, two independent blockers** (checked
  2026-09-01). RunPod's `ports` takes only `http` or `tcp` per its own schema, so
  there is no way to expose the UDP 60000-61000 mosh needs. And Tailscale runs
  `--tun=userspace-networking` here (no TUN device), which proxies inbound TCP
  and HTTP through `tailscale serve` but cannot deliver inbound UDP to a local
  port, so the tailnet is not a way around it either. The mobile answer instead
  is tmux plus webterm, which reconnects in the browser: `megh ssh` attaches
  session `main`, sshd sends keepalives so a dropped mobile link is noticed in
  ~3 min, and `/etc/tmux.conf` turns the mouse on so a touchscreen can scroll.
  mosh WOULD work on the Hetzner VM backend, which has a real TUN and real UDP.
- **RunPod's public proxy is open and unauthenticated.** Never expose ttyd/noVNC
  there. They bind to `127.0.0.1`; reach via `megh ssh` (tunnels) or Tailscale.
  Only `22/tcp` is public (key auth).
- **RunPod containers have no TUN device.** Tailscale must run userspace mode +
  `tailscale serve` (not normal tun mode).
- **A forwarded agent dies with its connection, so tmux loses it.** `ssh -A`
  makes a NEW socket per connection and deletes it on logout, while tmux keeps
  whatever `SSH_AUTH_SOCK` held when the session was created. Reattach the next
  day, or open the same session in webterm, and git push fails with "Permission
  denied (publickey)" though the keys are fine. The entrypoint writes
  `/etc/profile.d/megh-ssh-agent.sh`, which repoints `~/.ssh/agent.sock` at the
  live socket on every login and exports that stable path, so an old session
  follows whichever connection is open now. It is deliberately NOT a persistent
  agent holding a key: with no session open there is no agent, so an unattended
  box cannot write to your repos. Pushing from the phone therefore needs a
  session open somewhere, webterm alone is not one.
- **Session flush needs a credential on the box** (background timer can't use SSH
  agent forwarding). Use a fine-grained PAT scoped to only `megh-sessions`.
- **"Box not on the tailnet" is usually not a code bug.** Tailscale comes up
  ~1-2 min after the pod is RUNNING (image pull, then `tailscale up`), so a check
  in the first minute sees nothing. `megh down` deregisters the node immediately
  (`tailscale logout` over SSH, plus a control-plane delete when
  `MEGH_TAILSCALE_API_KEY` is set), so a `down`+re-`up` of the same name can
  briefly race GC and land as `<name>-1` (a stale offline node still holding the
  name). The SSH logout alone cannot cover a box that was already unreachable,
  which is the case that leaves debris, and is why the API delete exists.
  Diagnose with `megh doctor <name>` (or `megh doctor ts logs <name>` for the raw
  tailscale logs). The `TS_AUTHKEY` must be reusable + ephemeral; a
  single-use/expired key fails silently. **If nodes pile up permanently instead
  of clearing on their own, suspect the key is NOT ephemeral** (an ephemeral node
  is removed by Tailscale a while after it goes offline, even when the box died
  without a logout). Check that before blaming megh, since no amount of GC fixes
  the source. Clear existing debris with `megh doctor ts gc <name>`, which also
  takes the `<name>-1` / `<name>-2` variants that made the name drift.
  Most common real cause: the box was launched with a **stale key** (the
  launching shell's `TS_AUTHKEY` was older than the box's). Fix in place without
  a rebuild: `megh doctor ts setkey <name>` re-authenticates with the control
  machine's current `TS_AUTHKEY` (or `--authkey`) and re-serves. Turning on
  `tailscale.mint_keys` makes BOTH of these impossible rather than diagnosable:
  the key is minted at launch, so it cannot be stale, and it is ephemeral, so
  the node cannot outlive the box.
- **"claude wants me to log in again" is usually token expiry, not broken
  persistence.** `persist:` symlinks `~/.claude` and `~/.claude.json` onto the
  volume and that works, but the credential inside has its own clock: measured
  2026-08-21, the access token lasts ~7h and the REFRESH token ~3.5 days. Rebuild
  a box within a few days of last use and there is no login; come back after a
  week and there is, however healthy the volume. Check
  `/mnt/work/state/claude/.credentials.json` for `expiresAt` /
  `refreshTokenExpiresAt` before suspecting megh. The giveaway that persistence
  is fine is old `projects/` and `history.jsonl` mtimes sitting next to a
  freshly written `.credentials.json`.
- **A persisted dir is not a logged-in tool.** `~/.config/gh` and `~/.codex`
  symlink to the volume from the first boot, which is easy to read as "gh is set
  up". It is not: nothing ever ran `gh auth login`, so the dir is empty and
  `gh auth status` reports no hosts. Git over SSH still works (forwarded agent),
  only the `gh` CLI is affected. One login on any box fixes it for every box
  after, since that is the point of persisting it.
- **Tailscale bring-up is one script** (`internal/tsops/ts-up.sh`), embedded in
  the megh binary. The entrypoint runs it at boot (`megh doctor ts start
  --local`) and `megh doctor ts` pipes the same bytes over SSH, so boot and
  repair never drift and `doctor ts` works on any box regardless of image age.
- **The `megh-` prefix is internal only.** It marks RunPod pods (no tags there)
  but is never the tailnet hostname or a name the user types/sees. Route box
  names through `runpod.ShortName`/`Pod.DisplayName`, not raw `Pod.Name`. See
  `CONSTRAINTS.md` C1.

## Live-validation debt

Validated on a live RunPod box (2026-07-26): Tailscale userspace `up --ssh` +
`serve --http`, the box joining the tailnet as its bare `<name>` (the `megh-`
prefix is the RunPod pod marker only, not the tailnet hostname), `megh doctor`
probing tailscale + surfaces over SSH, and the bare-name `up`/`list`/`doctor`/`down`
lifecycle.

Second pass on a live slim box (2026-08-10, image `f57e6a0`), which cleared the
rest of the list:

- **webterm** (`:7682`) works end to end. The baked
  `/opt/megh/webterm/term.html` renders with zero external resource references
  (only a favicon 404), keystrokes typed in the page land in tmux session `main`
  (the same session `:7681` serves), the `^C` key-bar button delivers a real
  SIGINT and `↑` recalls history, and autocorrect / autocapitalize / autocomplete
  / spellcheck are off on both the paste textarea and xterm's helper textarea.
  Note the key bar binds `touchstart` + `mousedown` with `preventDefault`
  (`webterm.sh:271`) so the soft keyboard can't steal focus. It does NOT bind
  `click`, so a synthetic `.click()` in a test harness is a no-op.
- **baked `megh` + `megh hydrate --local`** run on the box.
- **code-server** on slim background-installs and comes up on `:8080` a few
  minutes after boot (`doctor` shows it `down`, later `up`).
- **Codex transcript path** in the (now removed) `flush-sessions.sh` staged
  `codex-sessions/` and `claude-projects/` correctly and the allowlist excludes
  `.credentials.json`. The authenticated push was never proven and never will be
  from the box: that path is gone as of 2026-08-21, since it needed a standing
  GitHub credential on a VM. The control machine collects instead.

Gotcha found in that pass: `megh hydrate --local` run from inside
`/mnt/work/repos/megh` picks up THAT checkout's `megh.yaml` by upward discovery,
not the baked `/etc/megh/megh.yaml`. A stale on-volume checkout therefore reports
drift that does not exist. Run it from `/` or pass `--config /etc/megh/megh.yaml`.

RunPod DinD is now settled (see `DESIGN.md`); it is a definitive no.

Third pass on image `0cf66ed` (2026-08-12) cleared the rest: `gh` 2.97.0 and
pnpm 11.21.0 are baked, `~/.config/gh` persists to `state/config-gh`, the baked
`megh hydrate --local --check` now exits 0 with no phantom drift, and Grafana
renders per-tenant dashboards with correct data and no errors (verified through
`/api/ds/query`, the panel path, plus a real browser render). Agni's EDA tools
are all apt-installable on 24.04 (`xschem` 3.4.4, `lepton-eda`, `ngspice`,
`gerbv`, `kicad`), so Agni needs no extra provisioning; only `geda` itself has no
installable candidate, superseded by `lepton-eda`.

Fourth pass on a live slim box (2026-08-21) cleared the last two features.
`megh enable vnc` comes up in ~35s and noVNC really attaches to the Xvfb display
(page title becomes `<container>:99 - noVNC`), with the auto-started xterm
visible. `megh enable playwright` then drives headed Chromium on `DISPLAY=:99`,
rendered through noVNC over a `megh browse` tunnel. Two real bugs fell out of it
and are fixed: the npx install probe that never installed anything, and the
650 MB browser cache landing on the throwaway container disk. Both are written
up in `internal/features/NOTES.md`.

Also measured that pass: US-CA-2 would not rent 4 vCPU at all (the API really
does return "no longer any instances"), which is what `megh regions probe` is
for.

Fifth pass (2026-08-21) validated per-box key minting end to end on a live box
(`mintlab`, slim, US-CA-2). `megh up` minted a single-use ephemeral key tagged
`tag:megh` at launch, the box joined the tailnet as its BARE name with no `-1`
suffix, came up `authorized: True` (so pre-authorization skips manual approval)
with `keyExpiryDisabled: True` (tagged nodes do not expire), and `RunSSH: true`
box-side. On `megh down` the node removed ITSELF: the command printed "asked
mintlab to leave the tailnet" and then nothing further, because the ephemeral
logout had already deleted the node and the control-plane prune found nothing to
do. That silence is the fix working. The API delete stays as the safety net for
a box killed out of band, where no logout can run.

Not verifiable from the control machine: Tailscale SSH INTO a tagged box. The
ACL rule for `dst: ["tag:megh"]` is in place and the network grants preview
confirms reachability, but proving the SSH policy needs a client on the tailnet,
which this Mac is not. The phone is the natural way to confirm it, which folds
into the Termux item.

The live-validation debt list is now empty. The one item that never cleared, the
authenticated session-flush push, was retired rather than validated: it required
a standing GitHub credential on a box, which is what the control-machine
collection replaces.
