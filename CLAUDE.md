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
megh ssh [name]                   # plain interactive shell (git-ready)
megh browse [port]                # tunnel box web surfaces to localhost, print URLs
megh enable [feature]             # add webterm/vnc/playwright/code to a box on demand
megh down [name] [-y]             # terminate a box (volume survives; leaves the tailnet first)
megh doctor [name]                # health probe: tailscale registered? surfaces up? scratch ok?
megh doctor ts <action> [name]    # tailscale ops: logs|status|start|stop|restart|setkey (setkey re-keys a box)
megh storage list|create|rm       # network volumes, one global cross-provider view
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

`persist:` lists home paths the entrypoint symlinks onto the scratch volume
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
- `TS_AUTHKEY` — optional, Tailscale (phone/tablet only); reusable + ephemeral
- `MEGH_SESSIONS_REPO` / `MEGH_SESSIONS_TOKEN` — optional, session history push

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

## Gotchas (things that bit us)

- **RunPod REST schema differs from its docs.** CPU pods use `computeType:"CPU"`,
  `vcpuCount`, `cpuFlavorIds` (enum `cpu3c/g/m`, `cpu5c/g/m`; c=2/g=4/m=8 GB per
  vCPU). `dataCenterIds` is an array, `ports` is an array, `env` is a map.
- **Container disk is capped by instance size** (~20 at 2 vCPU, up to ~60). That
  is the ephemeral OS disk, NOT scratch. Real scratch is the 100 GB volume.
- **Placement: storage + CPU must coexist in one DC, and there's no reliable
  availability API.** "Flavor defined in a DC" != "rentable." Only a real rent
  attempt proves capacity (a failed one costs nothing). US-TX-3 was dry; US-CA-2
  worked. Volume-supporting US DCs are enumerated in WORKFLOW.md.
- **RunPod's public proxy is open and unauthenticated.** Never expose ttyd/noVNC
  there. They bind to `127.0.0.1`; reach via `megh ssh` (tunnels) or Tailscale.
  Only `22/tcp` is public (key auth).
- **RunPod containers have no TUN device.** Tailscale must run userspace mode +
  `tailscale serve` (not normal tun mode).
- **Session flush needs a credential on the box** (background timer can't use SSH
  agent forwarding). Use a fine-grained PAT scoped to only `megh-sessions`.
- **"Box not on the tailnet" is usually not a code bug.** Tailscale comes up
  ~1-2 min after the pod is RUNNING (image pull, then `tailscale up`), so a check
  in the first minute sees nothing. `megh down` deregisters the ephemeral node
  immediately, so a `down`+re-`up` of the same name can briefly race GC and land
  as `<name>-1` (a stale offline node still holding the name). Diagnose with
  `megh doctor <name>` (or `megh doctor ts logs <name>` for the raw tailscale
  logs). The `TS_AUTHKEY` must be reusable + ephemeral; a single-use/expired key
  fails silently. Most common real cause: the box was launched with a **stale
  key** (the launching shell's `TS_AUTHKEY` was older than the box's). Fix in
  place without a rebuild: `megh doctor ts setkey <name>` re-authenticates with
  the control machine's current `TS_AUTHKEY` (or `--authkey`) and re-serves.
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
lifecycle. Not yet run live: code-server, the baked `megh` binary +
`megh hydrate --local`, and the Codex session transcript path in
`flush-sessions.sh`. Validate on the next launch after the image rebuilds.

Also not yet run live: **webterm** (`:7682`, `internal/features/webterm.sh`). To
check on a box: `megh enable webterm` then `megh browse 7682` (or open it on the
tailnet from a phone). Verify the second ttyd attaches the same tmux session as
`:7681`, the key bar sends the right escape sequences (Ctrl-C, arrows, tmux
prefix), autocorrect is actually off, and the inlined xterm.js renders offline
(no CDN; assets are vendored in `internal/features/vendor/` and inlined by
`features.Script`). This also exercises the baked-`megh`-in-entrypoint path.
