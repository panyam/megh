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
  see everything). If a provider adds real labels, switch to those. The prefix is
  internal only (a marker megh filters on), never a name the user types or sees:
  lookups accept the bare name, `list`/messages print it, and the box's Tailscale
  hostname is the bare name. See `CONSTRAINTS.md` C1.

## Access surfaces (baked into every box)

- **ttyd + tmux** on `:7681` — web shell, the "dev on the box" surface. No
  code-server; the user does not use VS Code (an off-by-default install is the
  only concession, not present in the base image yet).
- **webterm** on `:7682` — a second ttyd whose page is a custom xterm.js client
  built for phones/tablets: an on-screen key bar (Esc/Ctrl/Alt/Tab, arrows, the
  symbols buried in soft keyboards, one-tap Ctrl-combos, a tmux row, paste, and a
  Web-Speech voice mic) plus autocorrect/autocapitalize disabled on the input.
  It attaches the SAME tmux session as `:7681`, so the two ports are two views of
  one shell; the page and its WebSocket are same-origin (one port, no reverse
  proxy) which is what keeps it robust over `tailscale serve` and SSH tunnels.
  The page (`internal/features/webterm.sh`) is the single source of truth. It is
  baked into the image at build time (`MEGH_WEBTERM_EMIT_ONLY=1 megh enable
  webterm --local` in the Dockerfile) and the entrypoint serves it directly, so
  it is a first-class surface, not an on-demand add-on — no `enable` step on a
  fresh box. `megh enable webterm` remains the retrofit path for boxes launched
  from older images. Baking at build time also fails the build early if the
  baked-`megh` path is broken. xterm.js/css + the fit addon are vendored
  (`internal/features/vendor/`) and inlined into the page at assembly time
  (`@@...@@` markers, replaced by `features.Script`), so it has zero CDN/network
  dependency — neither the box nor the client needs internet. Versions are pinned
  in `vendor/versions.env`; `vendor/update.sh` (`make vendor`) re-fetches those
  versions and rewrites `vendor/SHA256SUMS`, and `update.sh --check` (`make
  vendor-check`) verifies integrity, reports pinned->latest, and prints a
  bump-readiness verdict — for a new xterm MAJOR the gate is a stable
  `@xterm/addon-fit` whose peer includes that major (READY / NOT READY /
  verify-manually if npm metadata is unavailable). A Go test
  (`vendor_test.go`, run in CI) fails if the embedded bytes ever drift from
  `SHA256SUMS`, so a stale or hand-edited bundle cannot slip through. Bumping xterm
  is: edit `versions.env`, `make vendor`, verify, commit — not a URL edit.
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
- **RunPod DinD. SETTLED: NO, and not fixable by configuration** (measured on a
  live CPU pod, 2026-08-12). The pod holds 13 capabilities and `cap_sys_admin` is
  not among them (`CapEff=00000000a80405fb`); `mount` and `iptables` are denied
  and `/dev/fuse`, `/dev/net/tun`, `/dev/kmsg`, `/dev/loop0` are all absent.
  Default `dockerd` fails creating the DOCKER NAT chain. The near-miss to be wary
  of: `dockerd --storage-driver=vfs --iptables=false --bridge=none` *does* start,
  but `docker run` then dies at `failed to register layer: unshare: operation not
  permitted`. It cannot unpack an image, so no container runs at all.
  **Consequences.** Testcontainers and `docker build` are impossible on RunPod;
  that is what the VM artifact (native Docker) is for, and it is the concrete
  reason to build the Hetzner backend. But this does NOT block using RunPod as
  the main box, because the actual need was storage services, and those run
  natively: apt postgres 16 with `postgresql-16-pgvector` (extension 0.6.0, real
  `<->` distance queries verified) and redis 7. Prefer one postgres CLUSTER with
  one DATABASE per project: per-database overhead is negligible while per-cluster
  memory is not, so isolation stays clean without N instances. Caveat: apt gives
  pg16 where the repos' compose files pin `pgvector/pgvector:pg18`, so anything
  depending on pg18 behaviour is a genuine gap.
- **Vertical over horizontal scaling. SETTLED** (measured 2026-08-12). RunPod CPU
  pricing is exactly linear: $0.04/vCPU-hr and $0.01/GB-hr, with 2/8, 4/16 and
  8/32 billing 0.080, 0.160 and 0.320. Two small boxes cost precisely what one
  double-sized box costs, so there is no cost argument for splitting. One box
  wins on every other axis: two boxes cannot share a postgres data dir on the
  same volume without corrupting it, per-box services duplicate memory, and a
  services box the others depend on stops being disposable. Linear pricing plus
  disposable boxes also means sizing is a PER-SESSION choice, not a standing one:
  run 4/16 for ordinary work and launch 8/32 for a demo day. Duty cycle dominates
  the cost model (8h/day at 4/16 is ~$28/mo against ~$117 always-on). Shared
  services migrate to the always-on box if and when that exists.

## Profiles (self-contained key + secrets per context)

A profile (`~/.megh/profiles/<name>/`, override `MEGH_HOME`) makes megh depend on
nothing at the system level, no reliance on `~/.ssh` or a shared agent that also
holds a corporate key. LOCKED decisions:

- **One box key, N GitHub keys.** The box key (single) SSHes into VMs; its pubkey
  is injected. GitHub identity keys (`gh/<name>`) are separate and plural: you
  work on repos from different accounts in the SAME box.
- **Scoped agent forwards ONLY the profile's keys.** megh spins up a throwaway
  ssh-agent holding exactly the profile's GitHub keys and forwards that, so a
  corporate key sitting in your normal agent never reaches a third-party VM. The
  box is connected to with `-i box.key -o IdentitiesOnly=yes`.
- **Multi-identity in one box via Host aliases.** On connect, megh writes the GH
  *public* keys plus a `~/.ssh/config` Host alias per identity (`gh-<name>`,
  `IdentityFile <pubkey>`, `IdentitiesOnly`). Repos clone via the alias
  (`git@gh-<name>:...`), so each repo signs with its own forwarded key. Private
  keys never touch the box.
- **Repo -> identity is explicit with a default.** `megh.yaml` repo `key:`, else
  `default_gh_key`. No inference.
- **Secrets: values in the profile, pointers in the repo.** `megh.yaml` names env
  vars (pointers); the profile's `secrets.env` provides values, applied over the
  ambient env (blanks fall back), so migration is gradual and nothing secret is
  committed.
- **Keygen reuses `oneauth/sshkeys` (in-process ed25519, container-safe).**
  Storage is plain 0600 files now; `oneauth/keys.EncryptedKeyStorage` is the
  intended drop-in for encryption at rest in a later secure-storage phase (kept
  out for now to avoid its OTel/JWKS deps in a lean CLI).
- **Blast radius is bounded.** Lose `~/.megh` and you lose only that profile's VM
  access and secret values, both re-mintable; code is in git.

Known extension (not built): a single repo needing two GitHub accounts at once is
already covered by per-repo `key:`; a single *clone* spanning accounts is not a
case megh handles.

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
