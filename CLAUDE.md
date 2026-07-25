# CLAUDE.md — megh

Disposable cloud dev boxes for agentic coding. Provider-abstracted CLI; RunPod
first (US), Hetzner next. Read `DESIGN.md` for the settled architecture and
`WORKFLOW.md` for the operational runbook. `SETUP.md` is first-run.

## Build / run

```
make build          # -> bin/megh
make install        # go install to GOBIN
make vars           # show which secrets are set (no values)
go build ./... && go vet ./...
```

The Makefile sources `~/personal/envvars` for every recipe that needs a secret.
Run `megh` directly only after `source ~/personal/envvars`.

## Commands

```
megh up [--volume <id> --dc <dc>] # launch; all flags default from megh.yaml
megh list [--all]                 # megh boxes (name/status/dc/$hr/ssh); --all = every pod
megh ssh [name]                   # ssh + tunnel localhost:7681 (shell) / 6080 (vnc)
megh down [name] [-y]             # terminate a box (volume survives)
megh storage list|create|rm       # network volumes, one global cross-provider view
megh hydrate [--check]            # clone repos onto a box's volume (or report drift)
megh profile create|use|list|show # profiles; profile gh add|list for GitHub identities
megh config                       # resolved settings + which secrets are set
megh registry ls                  # dev-env image tags
```

Make wrappers: `make up VOLUME=.. DC=..`, `make list`, `make ssh [BOX=..]`,
`make down [BOX=.. YES=1]`, `make storage-ls`, `make image` (push -> CI builds
image), `make registry`.

`megh up` defaults: `--provider` = `$MEGH_PROVIDER` else `runpod`; `--image` =
`$MEGH_IMAGE` else `ghcr.io/<namespace>/megh-<flavor>:latest` (flavor default
`base`, so `ghcr.io/panyam/megh-base:latest`); `--pubkey` = `$MEGH_PUBKEY` else
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

## Live-validation debt

Validated on a live RunPod box: Tailscale userspace `up --ssh` + `serve --http`
(box joins the tailnet as `megh-<...>-box`, both web surfaces served). Not yet
run live: code-server, the baked `megh` binary + `megh hydrate --local`, and the
Codex session transcript path in `flush-sessions.sh`. Validate on the next launch
after the image rebuilds.
