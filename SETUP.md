# megh: first box on RunPod

This gets you a working dev box to play with. It runs the `megh-base` image on a
RunPod CPU pod, backed by a network volume for scratch, reachable through a web
shell, a headed-browser view, and SSH.

The image is the reusable piece. The launch is scripted, but for the very first
box the RunPod console path is more reliable while we confirm the API against
your account. Both are below.

## 0. One-time prerequisites

1. A RunPod account and an API key (Settings > API Keys). Export it:
   ```
   export RUNPOD_API_KEY=...   # or type: ! export RUNPOD_API_KEY=... in this session
   ```
2. Your SSH public key handy (`cat ~/.ssh/id_ed25519.pub`). We put only the
   public key on the box. Git auth uses agent forwarding, so no long-lived
   credentials live on the box.
3. Decide a US data center. Pick one that offers CPU pods and network volumes.

## 1. Publish the image (private)

Create the repo private so your setup stays yours:

```
gh repo create <you>/megh --private --source=. --remote=origin --push
```

The `build-env` workflow builds `megh-base` for `linux/amd64` and pushes it to
GHCR as:

```
ghcr.io/<you>/megh-base:latest
```

A package built from a private repo is **private by default**, which is what you
want. RunPod then needs credentials to pull it:

1. Create a GitHub personal access token (classic) with only `read:packages`.
2. In the RunPod console: Settings > Container Registry Auth > Add. Registry
   `ghcr.io`, username your GitHub handle, password the token.
3. Select that credential when you deploy the pod (or on the template).

The image holds tools only, never source or keys, so even the registry sees
nothing sensitive. The end state drops GitHub entirely and serves images from
the self-hosted Forgejo registry over the mesh, so no third party sees them at
all. GHCR-private is the interim.

## 2. Create a network volume (once per data center)

In the RunPod console: Storage > Network Volumes > New. Pick your US data
center and a size (100 GB is plenty for scratch to start). Note its **volume
id** and the **data center id**. Every megh box in that data center mounts this
same volume at `/workspace`.

## 3a. Launch via the console (recommended for box #1)

Deploy a Pod > CPU. Then:

- Container image: `ghcr.io/<you>/megh-base:latest`
- Attach the network volume from step 2 (mounts at `/workspace`)
- Container disk: 100 GB
- Expose ports: `22/tcp`, `7681/http`, `6080/http`
- Environment variables:
  - `PUBLIC_KEY` = your SSH public key
  - `WORK_MOUNT` = `/workspace`
  - `ARCH_TAG` = `x86_64`

Deploy. Give it a minute to pull and start.

## 3b. Launch via the CLI (once box #1 confirms the flow)

Build the CLI once (Go 1.22+):

```
go build -o bin/megh .
```

Check the image published (needs the PAT to have `read:packages`):

```
./bin/megh registry ls
```

Then launch:

```
export MEGH_IMAGE=ghcr.io/<you>/megh-base:latest
export MEGH_PUBKEY="$(cat ~/.ssh/id_ed25519.pub)"
export MEGH_VOLUME_ID=<volume-id>
export MEGH_DC=<data-center-id>

./bin/megh up --provider runpod --vcpu 4 --ram 16
```

It prints the pod id and the URLs below.

## 4. Connect

- **Web shell**: `https://<pod-id>-7681.proxy.runpod.net` — a tmux session named
  `main`. Run `claude`, `codex`, vim, whatever. This is your "dev on the box"
  surface.
- **Headed browser**: `https://<pod-id>-6080.proxy.runpod.net/vnc.html` — watch
  Playwright headed runs from a laptop or phone. Test it:
  ```
  DISPLAY=:99 npx playwright open https://example.com
  ```
- **SSH**: from the console's Connect > TCP, note the ip and mapped port, then
  `ssh -A root@<ip> -p <port>`. The `-A` forwards your agent so git pushes work
  without keys on the box.

## 5. Tear down

Stop or terminate the pod from the console. The network volume and its contents
survive. Anything you cared about is either in git or on that volume. A rebuilt
box hydrates from both.

## 6. Using a phone as the control device

The machine that spawns boxes needs a provider credential, and whatever holds it
can terminate every box on the account. That is a good reason to keep it on
hardware you physically hold rather than on rented compute. A phone is enough:
megh is a static Go binary and Termux runs it fine.

The split it buys you is that spawning is rare and privileged while working is
constant and unprivileged, so they live on different machines. `megh up` refuses
to run on a box for exactly this reason (see `CONSTRAINTS.md` C3).

**Nothing is copied from your laptop.** Every credential here is re-mintable from
its own console in a browser on the phone, and re-minting is better than copying
because you then revoke the old one. That revocation is what makes "only the
phone can spawn" true rather than "the phone can also spawn". Do not mail
yourself an envvars file: plaintext at rest, permanent, and synced to every
device you own.

```sh
pkg install openssh gh                 # Termux; no Go toolchain needed
gh auth login                          # device flow, approve in the phone browser

# Binary + config, straight from the rolling release. No repo clone needed.
gh release download latest --repo panyam/megh -p 'megh-linux-arm64' -p 'megh.yaml'
mv megh-linux-arm64 megh && chmod +x megh
mkdir -p ~/.config/megh && mv megh.yaml ~/.config/megh/megh.yaml

./megh profile create phone            # mints its own box key + GitHub key here

# Mint the GitHub identity AND enrol it, so no base64 blob is ever pasted into a
# mobile browser. Keys are never copied between devices, so a new device always
# mints its own; --register is what stops that being the annoying part.
gh auth refresh -h github.com -s admin:public_key
./megh profile gh add personal --profile phone --register
```

Then fill in the secrets, which live
at `~/.megh/profiles/phone/secrets.env` at mode 0600, outside any repo:

| variable | where to mint it |
|---|---|
| `RUNPOD_API_KEY` | RunPod console > Settings > API Keys |
| `MEGH_TAILSCALE_CLIENT_ID` / `_SECRET` | Tailscale console > Settings > Trust credentials, scoped to `tag:megh` |
| `GH_MEGH_TOKEN` | GitHub PAT, `read:packages` (only needed for `megh registry ls`) |

Then revoke the old ones wherever they used to live. `megh config` shows what is
set without printing values.

**Losing the phone does not lock you out.** The RunPod web console still
terminates pods and mints a fresh API key, and `megh up` injects whatever pubkey
you hand it, so a new control device is a re-mint rather than a recovery.

## What is not here yet

- The mesh (Headscale/Tailscale) so you reach the box by name without RunPod's
  proxy. For box #1 the proxy URLs are enough to get a feel.
- The Hetzner backend, the `megh down/list/shell` verbs, and the shutdown flush
  hook. Those come once the RunPod box feels right and we know what to tune.
