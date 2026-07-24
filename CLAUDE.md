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
megh up --provider runpod --volume <id> --dc <dc> [--vcpu 2 --ram 8 --disk 20]
megh list [--all]                 # megh boxes (name/status/dc/$hr/ssh); --all = every pod
megh ssh [name]                   # ssh + tunnel localhost:7681 (shell) / 6080 (vnc)
megh storage list|create|rm       # network volumes, one global cross-provider view
megh registry ls                  # dev-env image tags
```

Make wrappers: `make up VOLUME=.. DC=..`, `make list`, `make ssh [BOX=..]`,
`make storage-ls`, `make image` (push -> CI builds image), `make registry`.

`megh up` defaults: `--provider` = `$MEGH_PROVIDER` else `runpod`; `--image` =
`$MEGH_IMAGE` else `ghcr.io/<namespace>/megh-<flavor>:latest` (flavor default
`base`, so `ghcr.io/panyam/megh-base:latest`); `--pubkey` = `$MEGH_PUBKEY` else
`~/.ssh/id_ed25519.pub`. `--volume`/`--dc` are still required (or
`$MEGH_VOLUME_ID`/`$MEGH_DC`) since placement is account-specific.

## Secrets (in `~/personal/envvars`)

- `RUNPOD_API_KEY` — provider access
- `GH_MEGH_TOKEN` — GHCR pull (classic PAT, `read:packages`); also set in RunPod
  console as the ghcr.io Container Registry Auth
- `TS_AUTHKEY` — optional, Tailscale (phone/tablet only); reusable + ephemeral
- `MEGH_SESSIONS_REPO` / `MEGH_SESSIONS_TOKEN` — optional, session history push

## Architecture (one-liners; see DESIGN.md)

- Dev env is a container image built from `env/base/provision.sh` (single source
  of truth); two artifacts, container (RunPod) + VM (Hetzner, later).
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

The Tailscale bring-up (`tailscale serve --http`) and the exact Codex session
transcript path in `flush-sessions.sh` were written from docs, not a live box.
Validate + iterate on the next launch, same as the RunPod API was shaken out.
