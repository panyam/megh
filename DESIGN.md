# megh design

`megh` (Hindi/Sanskrit for "cloud") moves project work and agentic coding off the
laptop onto cloud compute the user controls. This file records the decisions
that are settled so they do not get relitigated. Rationale lives next to each.

## Layers

Four layers, decoupled so the box is disposable and providers are swappable.

1. **Canonical state (portable, crosses everything).** Repos live in git
   (GitHub / GitLab / self-hosted Forgejo). Session state (agent config, tokens)
   is snapshotted with restic to S3-compatible object storage. This is the only
   layer that follows the user across providers, dedicated boxes, and GPU
   hardware.
2. **Config (portable).** The dev environment is a container image built from
   this repo (`env/<flavor>/`). Tooling changes are commits here, rebuilt by CI
   and rolled to every provider. No box is ever mutated in place and
   snapshotted. Images are versioned artifacts.
3. **Scratch (per-provider, never crosses).** A provider-native volume (RunPod
   network volume, Hetzner block volume) mounted into the dev containers as
   working copy, arch-tagged caches, and transient blobs. Disposable. A box
   hydrates it from the canonical layer on boot and flushes back on shutdown.
   Cross-provider transfer of scratch is deliberately out of scope; regenerate
   or seed from a snapshot if ever needed.
4. **Compute (per-provider).** A thin host runs N dev containers. A normalized
   `(vcpu, ram)` request maps to a real supported SKU per provider, never an
   arbitrary combination. Parallel containers on one box are the low-incremental-
   cost capacity knob.

## Decisions

- **Provider abstraction is the `megh` CLI, not any one provider's tooling.**
  Each backend uses whatever is most reliable for that provider. RunPod: REST
  API (its Terraform provider is too flaky). Hetzner: OpenTofu. Provider
  switching is an early, first-class capability, additive per backend.
- **The dev environment is ALWAYS a container. LOCKED.** This is settled, not an
  either/or with VMs. The container is the single build artifact that runs
  everywhere. What differs per provider is only what *hosts* the container:
  - Container-native provider (RunPod): the platform runs the container
    directly. There is no VM you manage.
  - VM-native provider (Hetzner): you provision a thin, disposable VM (OpenTofu)
    whose only job is to be a Docker host, and the same container runs on it.
  "Parallel containers for capacity" maps to multiple pods on RunPod (billed per
  pod) and multiple containers on one VM on Hetzner (near-zero incremental cost
  on a box you already pay for). The VM-host model is therefore the better fit
  for the parallel-capacity story; RunPod is better for a quick single box.
- **The one thing NOT locked by the above: whether a given box needs a real VM
  host.** A dev workflow that itself runs containers (testcontainers, `docker
  build`, nested devcontainers) needs a real Docker daemon. That is native on a
  VM host (Hetzner) and constrained-to-unavailable inside a container-native
  pod (RunPod, where Docker-in-container needs privileged/DinD that shared hosts
  often deny). So the substrate is locked (container), but provider choice for a
  *given* box depends on whether that box must run containers itself. See the
  RunPod DinD verify item below.
- **Scratch volumes are per-provider and do not migrate.** Accepted explicitly.
  If work concentrates on a provider, create a volume there that all that
  provider's boxes mount.
- **Config changes regenerate images, they never snapshot a mutated box.** A new
  tool is a commit that rebuilds `go-dev-env` / `ts-dev-env` / combos and rolls
  to the providers that carry that flavor.
- **Registry consolidation (target state).** The always-on box runs Forgejo (git
  remote) + Headscale (mesh coordinator) + Forgejo's OCI registry (dev-env
  images). One small box, self-hosted control plane, constraint-2 clean.

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
without duplicating logic. `bin/megh` -> `providers/<name>/*` is that seam.

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

- **Mesh control plane.** Leaning Headscale on the always-on box (self-hosted,
  constraint-2 pure, at no extra machine cost). Tailscale hosted is the
  low-effort fallback. Plain WireGuard is clunky for phone + service-by-name.
  Cost is a wash across all three; the axis is ops effort vs third-party control
  plane. Not needed for the first RunPod box (its proxy URLs suffice).
- **Default architecture** for VM providers. RunPod forces x86_64. For Hetzner,
  the tie-breaker is the user's Mac arch: match it for tightest local/remote
  parity, unless the deploy target's arch outweighs that.
- **GPU.** Deferred. A separate explicitly-invoked flow, not this box.
- **RunPod DinD (verify on box #1).** Does a RunPod CPU pod let the dev workflow
  run its own containers (`docker build`, testcontainers)? If not, RunPod is a
  taste-test box and real container-in-dev work belongs on a VM host. This
  decides how far RunPod goes beyond "get a feel."

## Constraints carried from the handoff

- Low, predictable cost. Fine-grained lever on incremental spend.
- No third-party managed services in the control plane. Raw VMs and containers.
  IaaS hypervisor access is accepted. RunPod is a marketplace over third-party
  hardware and is a knowing, scoped exception for the "get a feel" phase.
- Coding agents sending source to Anthropic/OpenAI is expected and fine.
