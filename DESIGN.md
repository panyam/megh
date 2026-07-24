# megh design

`megh` (Hindi/Sanskrit for "cloud") moves project work and agentic coding off the
laptop onto cloud compute the user controls. This file records the decisions
that are settled so they do not get relitigated. Rationale lives next to each.

## Layers

Four layers, decoupled so the box is disposable and providers are swappable.

1. **Canonical state (portable, crosses everything).** Repos live in git
   (GitHub / GitLab / self-hosted Forgejo). Agent session *transcripts* are
   pushed to a private git repo (`megh-sessions`) so history is durable and
   searchable; auth tokens are re-mintable and deliberately not persisted. This
   is the only layer that follows the user across providers, dedicated boxes,
   and GPU hardware. (This supersedes the earlier restic-to-S3 plan:
   transcripts-in-git is greppable where a restic backup is not, and everything
   else is re-mintable, so restic is no longer needed.)
2. **Config (portable).** The dev environment is declared once in
   `env/<flavor>/provision.sh` and built into two artifacts (a container image
   and a VM image) from that same script. Tooling changes are commits here,
   rebuilt by CI and rolled to every provider. No box is ever mutated in place
   and snapshotted. Artifacts are versioned.
3. **Scratch (per-provider, never crosses).** A provider-native volume (RunPod
   network volume, Hetzner block volume) mounted into the dev containers as
   working copy, arch-tagged caches, and transient blobs. Disposable. A box
   hydrates it from the canonical layer on boot and flushes back on shutdown.
   Cross-provider transfer of scratch is deliberately out of scope; regenerate
   or seed from a snapshot if ever needed.
4. **Compute (per-provider).** Container-native providers run the container
   image directly; VM-native providers run the dev env directly on a VM from the
   VM image. A normalized `(vcpu, ram)` request maps to a real supported SKU per
   provider, never an arbitrary combination.

## Decisions

- **Provider abstraction is the `megh` CLI, not any one provider's tooling.**
  Each backend uses whatever is most reliable for that provider. RunPod: REST
  API (its Terraform provider is too flaky). Hetzner: OpenTofu. Provider
  switching is an early, first-class capability, additive per backend.
- **Two build artifacts from one source of truth. LOCKED.** The dev environment
  is declared once, in `env/<flavor>/provision.sh`, and emitted as two artifacts
  that both call that script at build time so they cannot drift:
  - **Container image** (`Dockerfile`) for container-native providers (RunPod).
    The platform runs it directly; there is no VM you manage. `INSTALL_DOCKER=0`
    (a container needs no daemon inside it).
  - **VM image** (`packer/`, later) for VM-native providers (Hetzner, and the
    AWS-AMI shape). The dev environment runs DIRECTLY on the VM, not in a
    container inside it, so the box has a real native Docker daemon for dev
    workflows (`docker build`, testcontainers). `INSTALL_DOCKER=1`. This is the
    user's stated preference and it dissolves the Docker-in-container (DinD)
    problem rather than working around it.
  Provider type picks the artifact.
- **On VM-native providers, run the VM directly; skip managed orchestration.**
  For a personal dev box a VM image (AMI / snapshot) is cheaper and more
  constraint-2-consistent than a managed container service (EKS / ECS / AKS):
  no control-plane fee (EKS alone is ~$73/mo before any nodes), no orchestrator
  in the path, nothing to bin-pack. Managed orchestration earns its keep at
  fleet scale, which does not apply to one dev box. (AWS is a good illustration
  of the principle but fights the predictable-cost constraint via egress /
  on-demand pricing, so it is unlikely to be a chosen provider.)
- **Capacity knob differs by artifact.** Container-native: multiple pods (billed
  per pod). VM-native: multiple worktrees / agent tasks on one VM at near-zero
  incremental cost, with native Docker available if a task wants container
  isolation.
- **Scratch volumes are per-provider and do not migrate.** Accepted explicitly.
  If work concentrates on a provider, create a volume there that all that
  provider's boxes mount.
- **Config changes regenerate images, they never snapshot a mutated box.** A new
  tool is a commit that rebuilds `go-dev-env` / `ts-dev-env` / combos and rolls
  to the providers that carry that flavor.
- **Registry consolidation (target state).** The always-on box runs Forgejo (git
  remote) + Headscale (mesh coordinator) + Forgejo's OCI registry (dev-env
  images). One small box, self-hosted control plane, constraint-2 clean.
- **Agent session history is persisted to git, not restic. LOCKED.** Claude/Codex
  store sessions as JSONL transcripts. A timer + shutdown hook (`flush-sessions.sh`)
  pushes only transcript/memory files (never credentials) to a private
  `megh-sessions` repo, so history is durable and `git grep`-searchable across
  every provider and laptop. The push uses a fine-grained PAT scoped to ONLY that
  repo (`contents:write`), passed as pod env and never written to git config. A
  compromised box can write your session history and nothing else. This is a
  deliberate, narrow exception to "no long-lived credentials on the box" that a
  background timer requires; agent forwarding only works in an interactive SSH
  session, not for a timer.
- **megh is stateless; the provider is the source of truth.** No local state file
  or dotdir. `megh list` / `megh storage list` query the provider live. RunPod
  has no pod tags/labels, so megh-managed resources are identified by a `megh-`
  **name prefix**: `megh up` enforces it; `list`/`ssh` filter to it (`--all` to
  see everything). If a provider adds real labels, switch to those.

## Access surfaces (baked into every box)

- **ttyd + tmux** on `:7681` — web shell, the "dev on the box" surface. No
  code-server; the user does not use VS Code (an off-by-default install is the
  only concession, not present in the base image yet).
- **noVNC + Xvfb + x11vnc** on `:6080` — headed browser (Playwright) viewable on
  laptop or phone. Chosen over X11 forwarding because a phone has no X server.
- **SSH** with agent forwarding — no long-lived git credentials on the box.

## Control surface (how you drive megh)

Goal: launch and reach boxes from a local terminal now, and from a phone later
(native app or a self-hosted web page). The provider backends are therefore kept
as a library the CLI calls, so a thin HTTP control API can call the same code
without duplicating logic. The megh CLI (`cmd/`) over `internal/providers/<name>`
is that seam; a phone control panel is another front end over the same packages.

Hosting the phone-facing control panel, two options:

- **Self-hosted control API on the always-on box, reached over the mesh
  (recommended).** The phone is already on the mesh to reach box web shells, so
  the control panel rides the same path. Constraint-2 pure: the RunPod/Hetzner
  API keys stay on your box, no third party, no public endpoint. Only reachable
  when the phone is on the mesh, which it will be anyway.
- **Google App Engine (or similar) with your credentials.** Public HTTPS, no
  mesh needed. But it must hold your provider API keys (credential exposure to
  Google) and it is a third-party managed service, a mild constraint-2 wrinkle.
  Only worth it if you need to launch boxes without the mesh, which the mesh
  itself removes the need for.

Leaning mesh-hosted. Not built yet; the CLI is the first surface.

## Open, not yet committed

- **Remote access. DECIDED.** Security comes from SSH (key auth) plus binding the
  web surfaces to localhost, not from any mesh. RunPod's public proxy is open and
  unauthenticated, so ttyd/noVNC bind to `127.0.0.1` and only `22/tcp` is public.
  The Mac uses `megh ssh` (auto ip:port + localhost tunnels of 7681/6080),
  nothing to install. Tailscale is an OPTIONAL convenience layer for
  phone/tablet browser access, enabled only when `TS_AUTHKEY` is set; on RunPod
  it runs userspace mode + `tailscale serve` because the container has no TUN
  device. Headscale (self-hosted coordinator on the always-on box) stays the
  constraint-2-pure upgrade if the hosted coordinator ever bothers us. Plain
  WireGuard rejected: clunky for a phone and for disposable boxes.
- **Default architecture** for VM providers. RunPod forces x86_64. For Hetzner,
  the tie-breaker is the user's Mac arch: match it for tightest local/remote
  parity, unless the deploy target's arch outweighs that.
- **GPU.** Deferred. A separate explicitly-invoked flow, not this box.
- **RunPod DinD (verify on box #1).** Does a RunPod CPU pod let the dev workflow
  run its own containers (`docker build`, testcontainers)? If not, RunPod is a
  taste-test box and real container-in-dev work belongs on a VM-native box (run
  directly, native Docker). This decides how far RunPod goes beyond "get a feel."

## New machine / portability

A fresh laptop needs almost nothing, because megh holds no local state and the
box holds nothing precious. Beyond cloning the repo and `make install`, you carry
(or re-mint) exactly two things:

1. **Secrets** (`~/personal/envvars`): `RUNPOD_API_KEY`, `GH_MEGH_TOKEN` (GHCR
   pull), `TS_AUTHKEY` (only for the phone path), and once the flush hook is on,
   `MEGH_SESSIONS_TOKEN`. All regenerate from their consoles in a minute.
2. **An SSH keypair** for box access + git push. Can be freshly generated and its
   pubkey re-enrolled (GitHub + `MEGH_PUBKEY`).

Everything else is remote or reconstructible: volumes/boxes/image live on the
provider (`megh list`, `megh storage list`); code is in git; agent history is in
`megh-sessions`; there is no local config or state file. In the strongest form
you carry nothing physical and just re-authenticate three services.

## Constraints carried from the handoff

- Low, predictable cost. Fine-grained lever on incremental spend.
- No third-party managed services in the control plane. Raw VMs and containers.
  IaaS hypervisor access is accepted. RunPod is a marketplace over third-party
  hardware and is a knowing, scoped exception for the "get a feel" phase.
- Coding agents sending source to Anthropic/OpenAI is expected and fine.
