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
  two artifacts, a container image and a VM image, never a hand-mutated snapshot.
- **Compute** is one backend per provider behind the `megh` CLI. Container-native
  providers run the image directly; VM-native providers run the dev env directly
  on the VM (native Docker, no nested containers). RunPod first (US); Hetzner next.
- **Scratch** is a per-provider volume mounted at `/mnt/work`. Per-provider by
  design; it does not migrate.
- **Canonical state** is git plus restic to object storage. The only layer that
  crosses providers.

Every box ships a web shell (ttyd + tmux), a headed-browser view for Playwright
(noVNC, works on a phone), and SSH with agent forwarding.

## Start here

- `SETUP.md` — stand up your first box on RunPod.
- `DESIGN.md` — the settled decisions and rationale.

## Layout

```
megh/
├── main.go, cmd/                # Go + Cobra CLI (the provider abstraction seam)
│   ├── up.go                    #   megh up <name> --provider ...
│   ├── registry.go              #   megh registry ls | tags
│   └── doctor.go                #   megh doctor (planned)
├── internal/
│   ├── config/                  # registries + provider config
│   ├── registry/                # OCI v2 tag inspection (stdlib only)
│   └── providers/runpod/        # RunPod backend (REST API)
├── env/base/                    # provision.sh (source of truth) + Dockerfile + entrypoint
├── .github/workflows/           # build + push env images to a registry
├── SETUP.md
└── DESIGN.md
```

## Status

First drop: RunPod backend, base image, web shell + noVNC + SSH. Not yet: the
mesh (Headscale/Tailscale), the Hetzner backend, `megh down/list/shell`, and the
shutdown flush hook.
