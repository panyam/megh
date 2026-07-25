# megh lifecycle

The flows that make up a megh box, from image build to teardown, and exactly
where keys and secrets live. See `DESIGN.md` for rationale and `WORKFLOW.md` for
the operational runbook.

## 1. Image build and publish (config-as-code)

The dev environment is declared once and built by CI. Nothing is ever installed
by hand on a box and snapshotted.

```mermaid
flowchart LR
  edit["edit env/base/provision.sh<br>(or entrypoint / Dockerfile)"] --> push["git push main"]
  push -->|"only if env/** changed"| ci["GitHub Action: build-env<br>build-arg MEGH_BUILD_REF = git sha"]
  ci --> ghcr["ghcr.io/panyam/megh-base latest<br>plus a per-sha tag, private, amd64"]
  ghcr -.->|"RunPod pulls via<br>Container Registry Auth"| pod["RunPod pod"]
```

CLI-only changes (`cmd/`, `internal/`) do **not** trigger a build; they run on
your Mac, not in the image.

## 2. Profile setup (once per machine / identity)

```mermaid
flowchart TD
  create["megh profile create personal"] --> boxkey["box.key generated<br>(SSH into VMs)"]
  create --> secrets["secrets.env template"]
  ghadd["megh profile gh add personal<br>megh profile gh add inferbook"] --> ghkeys["gh identity keys generated"]
  ghkeys --> pub["pubkeys printed"]
  pub --> github["add each pubkey to its<br>GitHub account (once)"]
  create --> use["megh profile use personal"]
```

## 3. Box lifecycle: up -> hydrate -> work -> down

```mermaid
sequenceDiagram
  participant Mac
  participant RunPod
  participant VM as Box
  participant GitHub
  participant Sessions as megh-sessions repo

  Note over Mac: megh up
  Mac->>RunPod: create pod with image, pod env, and box pubkey
  RunPod->>VM: pull image and boot
  VM->>VM: entrypoint mounts volume, starts tailscale and web surfaces, arms flush timer

  Note over Mac: megh hydrate
  Mac->>VM: forward gh keys and write ssh config aliases
  VM->>GitHub: clone repos into workspace repos via gh alias
  GitHub-->>VM: repo contents

  Note over Mac: megh ssh
  Mac->>VM: connect with box key, forward gh keys, tunnel surfaces
  VM->>GitHub: git push and pull with forwarded key

  Note over VM: timer and shutdown
  VM->>Sessions: flush transcripts

  Note over Mac: megh down
  Mac->>RunPod: delete pod, volume survives
```

Repos are **not** cloned automatically by `megh up`; `megh hydrate` does it (it
needs your forwarded keys, which only exist during a client-driven SSH).

## Access surfaces (all private)

Every surface is bound to localhost (reached via an SSH tunnel or Tailscale) or
is key-auth only. Nothing but SSH is ever on the public proxy, and only when
`expose_ssh: true`.

| Surface | Port | Reach it via |
|---|---|---|
| SSH (shell, git, tunnels) | 22/tcp | `megh ssh`: public ip:port (key auth), or the tailnet (Tailscale SSH) |
| ttyd web shell (tmux) | 7681 | tunnel → `localhost:7681`, or `http://<box>:7681` on the tailnet |
| noVNC headed browser | 6080 | tunnel → `localhost:6080/vnc.html`, or `http://<box>:6080/vnc.html` |
| code-server (VS Code) | 8080 | tunnel → `localhost:8080`, or `http://<box>:8080` on the tailnet; Remote-SSH also works |

`megh ssh` forwards 7681/6080/8080 to your localhost automatically. With
Tailscale up, the same surfaces are served by name over the tailnet (phone /
tablet friendly). `expose_ssh: false` drops even public 22/tcp; the RunPod
console Web Terminal stays the break-glass.

## 4. Where keys and secrets live

The important one. Private SSH keys never land on a box; secrets only land on a
box if you explicitly declare them as `box_envs`.

```mermaid
flowchart TB
  subgraph Mac["Your Mac (control plane)"]
    prof["~/.megh/profile:<br>box.key + gh private keys"]
    env["environment:<br>RUNPOD_API_KEY, GH_MEGH_TOKEN,<br>box_envs (OPENAI_API_KEY, ...)"]
  end
  subgraph Box["RunPod VM (data plane)"]
    pub["~/.ssh/gh-*.pub + config aliases"]
    penv["pod env: box_envs,<br>TS_AUTHKEY, MEGH_SESSIONS_TOKEN"]
    repos["/workspace/repos"]
  end
  GitHub["GitHub"]

  prof -->|"box.key: connect (-i)"| Box
  prof -->|"gh PRIVATE keys: forwarded via scoped agent,<br>NEVER written to disk on the box"| Box
  prof -->|"gh PUBLIC keys: written"| pub
  env -->|"box_envs copied as pod env at up"| penv
  Box -->|"git via forwarded key + alias"| GitHub
```

| Thing | On the box? | How |
|---|---|---|
| Box SSH key (private) | no | on your Mac; used to connect |
| GitHub SSH keys (private) | **no** | forwarded via scoped ssh-agent |
| GitHub SSH keys (public) | yes | written to `~/.ssh/gh-*.pub` + config aliases |
| `RUNPOD_API_KEY`, `GH_MEGH_TOKEN` | no | used by megh on the Mac |
| Service API keys (`box_envs`) | yes | copied into pod env at `megh up` |
| `TS_AUTHKEY`, `MEGH_SESSIONS_TOKEN` | yes | pod env (tailscale / session flush) |

## 5. State and persistence

```mermaid
flowchart LR
  repos["/workspace/repos<br>(working copies)"] -->|"git push"| gh["git remotes"]
  state["/workspace/state claude+codex<br>(transcripts)"] -->|"flush-sessions.sh<br>(timer + shutdown)"| sess["megh-sessions repo"]
  gh -.->|"megh hydrate"| repos
  sess -.->|"git clone"| state
  vol[("network volume<br>/mnt/work")]
  vol -.->|"survives"| destroy["box destroyed"]
```

The volume is fast scratch, not the source of truth: code lives in git, agent
history in the sessions repo, and both rehydrate onto a fresh volume.

## Verifying the running image

Each image is stamped at build with the git SHA it came from. From a box:

```
cat /etc/megh/build-info
# ref=<git-sha>
# built=<timestamp>
```

Compare `ref` to the `latest` SHA tag from `megh registry ls`. If they match,
the box is running the current image. (Requires the build stamp, which lands in
the next image build after this change.)
```
