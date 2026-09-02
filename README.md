# megh

Self-hosted cloud dev boxes for agentic coding. `megh` is Hindi/Sanskrit for
"cloud."

Move project work and coding agents (Claude Code, Codex CLI) off the laptop and
onto cloud compute you control, with a fine lever on cost and no third party in
the control plane. Boxes are disposable cattle: destroy and recreate rather than
rescale. Everything precious lives in git and on a scratch volume, so losing a
box costs minutes, not work.

## Shape

- **Dev environment** is declared once (`env/base/provision.sh`) and built into
  two flavors, `base` (full, Playwright and code-server baked) and `slim` (lean
  and fast to pull), never a hand-mutated snapshot.
- **Compute** is one backend per provider behind the `megh` CLI. RunPod is the
  only backend today; Hetzner is next. megh keeps no local state, so the
  provider is the source of truth and boxes are found by a `megh-` name prefix.
- **Scratch** is a per-provider network volume mounted at `/mnt/work`, pinned to
  a datacenter and shareable by boxes in it. Per-provider by design; it does not
  migrate.
- **Canonical state** is git: your code in its own repos, agent transcripts
  pushed to a `megh-sessions` repo by a timer on the box. That is the only layer
  that crosses providers.
- **Reachability** is two paths, both load-bearing. Public SSH on 22/tcp with key
  auth is how the laptop drives a box (`ssh`, `browse`, `hydrate`, `doctor`).
  Tailscale, in userspace mode with `tailscale serve`, is how a phone or tablet
  reaches one. Web surfaces bind to loopback and are never published to RunPod's
  open proxy.

Boxes ship a web shell (ttyd plus tmux) and a browser-based terminal. A headed
browser display (noVNC), Playwright, code-server, an observability stack,
postgres, and redis are added per box with `megh enable`.

## Commands

`megh up` / `list` / `ssh` / `browse` / `down` are the daily loop. Beyond those:
`enable` adds a feature to a box, `doctor` probes health and repairs Tailscale,
`storage` manages volumes, `regions` finds a datacenter that will actually rent
the box you want, `hydrate` clones repos onto a volume, `profile` holds
per-context SSH and GitHub identities, and `portal` publishes a bookmarkable
index of boxes and URLs. `megh config` shows resolved settings and which secrets
are set, never their values.

Settings and pointers to secrets (env-var names) live in `megh.yaml`. Only
`megh.yaml.example` is tracked here: a real one names every repo you work on, so
it belongs in your own private config repo. Secret values live in the
environment and never in either.

## Start here

- `SETUP.md` — stand up your first box on RunPod, and (§6) run megh from a phone
  as the control device.
- `WORKFLOW.md` — the operational runbook.
- `DESIGN.md` — the settled decisions and rationale.
- `internal/features/NOTES.md` — implementation lore for the `enable` features.

## Layout

```
megh/
├── main.go, cmd/                # Go + Cobra CLI (the provider abstraction seam)
├── internal/
│   ├── config/                  # megh.yaml, registries, provider settings
│   ├── profile/                 # per-context box key + GitHub identity keys
│   ├── registry/                # OCI v2 tag inspection (stdlib only)
│   ├── features/                # `megh enable` scripts, embedded in the binary
│   ├── tsops/                   # Tailscale bring-up, shared by boot and repair
│   └── providers/runpod/        # RunPod backend (REST API)
├── env/base/                    # provision.sh (source of truth) + Dockerfile + entrypoint
├── .github/workflows/           # build + push env images to GHCR
├── SETUP.md, WORKFLOW.md, DESIGN.md, CONSTRAINTS.md
└── megh.yaml
```

## Status

Working end to end on RunPod: the box lifecycle, Tailscale, the web shell and
webterm, code-server, the observability stack, postgres and redis, profiles,
hydrate, the portal, and the `vnc` and `playwright` features.

Not built yet: the Hetzner backend and volume backup.
