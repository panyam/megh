# megh: first box on RunPod

This gets you a working dev box to play with. It runs the `megh-full` image on a
RunPod CPU pod, backed by a network volume for scratch, reachable through a web
shell, a headed-browser view, and SSH.

The image is the reusable piece. The launch is scripted, but for the very first
box the RunPod console path is more reliable while we confirm the API against
your account. Both are below.

## Before any of this: a box on your own machine

If you only want to see what a box IS, the local backend gets you one in a couple
of minutes with no account, no key, and no bill:

```sh
make image-local-full                  # builds for this machine's arch
megh up   --provider docker local1
megh ssh  --provider docker local1     # tmux 'main', claude and codex already installed
megh down --provider docker local1
```

It runs the same image and the same entrypoint as a rented box, so everything you
learn about persist, symlinks, `megh enable` and `megh browse` transfers. Set
`providers.docker.image` to the tag the build prints, and declare what to
bind-mount under `providers.docker.mounts`; `megh.yaml.example` has the shape.
There are two local tags, one per flavor: `image-local-full` bakes Playwright, the
headed display and code-server, `image-local-slim` leaves them out and installs
code-server at boot.

The rest of this page is the rented path, which is what you want once you need
the box to outlive your laptop being closed.

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

The `build-env` workflow builds `megh-full` for `linux/amd64` and pushes it to
GHCR as:

```
ghcr.io/<you>/megh-full:latest
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

- Container image: `ghcr.io/<you>/megh-full:latest`
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
export MEGH_IMAGE=ghcr.io/<you>/megh-full:latest
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
constant and unprivileged, so they belong on different machines. No box is
elevated (`CONSTRAINTS.md` C3), so no box can spawn.

**Nothing is copied from your laptop.** Every credential here is re-mintable from
its own console in a browser on the phone, and re-minting beats copying because
you then revoke the old one. That revocation is what makes "only the phone can
spawn" true rather than "the phone can also spawn". Do not mail yourself an
envvars file: plaintext at rest, permanent, and synced to every device you own.

### 6.1 Termux packages

```sh
pkg install openssh gh git
```

`openssh` is not optional. megh shells out to `ssh`, `ssh-agent` and `ssh-add`
for every box operation and for the scoped agent that forwards your GitHub keys.
No Go toolchain is needed, which is the point of publishing binaries.

### 6.2 Authenticate GitHub

```sh
gh auth login                                        # device flow, approve in the browser
gh auth refresh -h github.com -s admin:public_key    # needed by --register below
```

This is the bootstrap credential and it is obtained fresh on the phone, so
nothing is transported. A default `gh` login does not carry `admin:public_key`,
and without it step 6.4 fails with a bare `HTTP 403: Resource not accessible`
that names neither the scope nor the fix.

### 6.3 Binary and config

One command, and it is the same one on every machine:

```sh
curl -fsSL https://raw.githubusercontent.com/panyam/megh/main/install.sh | sh
```

The repo and its releases are public, so the script and the binary need no auth.

It picks the artifact for the machine, verifies the checksum, installs to
`$PREFIX/bin` on Termux (`~/.local/bin` elsewhere), and writes
`~/.config/megh/megh.yaml` without overwriting one already there. Re-run it to
upgrade. `MEGH_TARGET` and `MEGH_INSTALL_DIR` override the guesses.

The **config** is the one part that still needs auth. A real `megh.yaml` names
every repo you work on, so it lives in the private dotfiles repo. The installer
fetches it with `gh` when you are logged in, and otherwise installs
`megh.yaml.example` and says so, which still leaves you a working binary.
`MEGH_CONFIG_REPO` and `MEGH_CONFIG_PATH` point it elsewhere.

So step 6.2 is only needed for the config and for `--register` later; the install
itself works without it.

Taking the **android** build rather than linux/arm64 matters and the script
handles it: the arch is the same, but Go's static linux binary is `ET_EXEC` and
Android's loader accepts only `ET_DYN`, so Termux refuses it with
`unexpected e_type: 2`.

`latest` is a rolling prerelease rebuilt on every push to main, so this is
always current. `~/.config/megh/megh.yaml` is the second place megh looks (after
walking up from the cwd), so it resolves from any directory. No repo clone is
needed: the config rides along as a release asset, and it holds only settings and
env-var names.

### 6.4 Mint the profile

```sh
megh profile create phone
megh profile gh add personal --profile phone --register
megh profile use phone
```

The profile mints its own box key and GitHub key locally. `--register` uploads
the pubkey to GitHub, so no base64 blob is ever pasted into a mobile browser.

### 6.5 Re-mint the secrets

They live in `~/.megh/profiles/phone/secrets.env`, mode 0600, outside any repo.

| variable | mint at |
|---|---|
| `RUNPOD_API_KEY` | RunPod console > Settings > API Keys |
| `MEGH_TAILSCALE_CLIENT_ID` / `_SECRET` | Tailscale > Settings > Trust credentials, scoped `tag:megh` |
| `GH_MEGH_TOKEN` | GitHub PAT, `read:packages`; only for `megh registry ls` |

`megh config` shows which are set without printing values. `GH_MEGH_TOKEN` is
needed even though `up` never reads it, because `requires.envs` gates the launch
on it.

**Keep the master copy in a Bitwarden secure note**, and make the note the whole
bootstrap: a script that writes itself to `~/megh-bootstrap.sh` via a heredoc and
then runs it, so §6.1–6.5 are one copy and one paste in Termux. Write-then-run
matters: pasted line by line, `gh auth login` reads the following pasted lines
as its answers. The script should `set +o history`, `trap` its own deletion
(it holds the values), write `secrets.env` under `umask 077`, and skip steps
already done so re-pasting is also the upgrade and rotation path.

### 6.6 Launch

```sh
megh config
megh regions probe --dc US-CA-2 --first -y   # capacity flaps; a probe costs a fraction of a cent
megh up devbox
megh ssh devbox                              # lands in tmux `main`
```

### 6.7 Reaching a phone-launched box from other machines

A box trusts the launcher's key plus every key in `extra_pubkeys:` in
`megh.yaml`. Keep one SSH key item in Bitwarden, put its public half there, and
enable Bitwarden desktop's SSH agent (Settings > Enable SSH agent; point
`SSH_AUTH_SOCK` at its socket) on any machine you want in from. That machine
needs no megh, profile, provider key or tailnet: open the portal, copy the box's
`ssh -p <port> root@<ip>` line (or the `-L 7682:...` one for webterm), and
Bitwarden asks to approve each use. Tailnet members can use `ssh root@<box>`
instead. `extra_pubkeys` is read at create, so a new key reaches only new boxes.

### The surprise worth knowing first

**The phone's profile has a new box key, so it can create boxes but cannot SSH
into any box launched with a different device's key.** `megh up` injects
whichever profile is active, and existing boxes only trust the key they were
born with. Either relaunch the box from the phone, or append the phone's pubkey
to the running box's `authorized_keys` by hand once.

For the same reason, keep the old control machine able to spawn until the phone
has taken a box from `up` to `ssh` successfully. Revoke afterwards, not before.

**Losing the phone does not lock you out.** The RunPod web console still
terminates pods and mints a fresh API key, and `megh up` injects whatever pubkey
you hand it, so a new control device is a re-mint rather than a recovery.

## 7. meghplane: the control plane as a web page

`cmd/meghplane` is the page `megh serve` runs locally, hosted on App Engine
standard behind IAP, so any device with a browser can list, launch and
terminate boxes. **By default it stores no keys** (§7.1 covers keeping them on
the server instead). You paste your control-plane note into
the page; the browser keeps it in session storage for that tab and sends the
keys as headers on each request, and the server drops them when the request
ends. Closing the tab forgets them.

It checks who you are twice. IAP signs you in at Google's edge, and the app
then verifies IAP's signed header itself (audience, issuer, signature, expiry,
via oneauth) and admits only the emails in `serve.allowed_emails`. With that
list empty, or IAP's keys unreachable, it refuses to start.

**One-time setup.** This is the sequence that worked on the live `meghplane`
project (2026-10-07), on a personal Google account with no Workspace org.

1. **App Engine app** created (any region; it cannot change later).
2. **Let the deploy build.** Cloud Build runs as the app's service account, and
   one you created starts with no build rights. Without them the deploy dies at
   `invalid bucket "staging.<project>.appspot.com"; service account ... does not
   have access to the bucket`:
   ```sh
   SA=<the-app-service-account>
   gcloud projects add-iam-policy-binding <project> --member=serviceAccount:$SA --role=roles/cloudbuild.builds.builder
   gcloud projects add-iam-policy-binding <project> --member=serviceAccount:$SA --role=roles/logging.logWriter
   gcloud storage buckets add-iam-policy-binding gs://staging.<project>.appspot.com --member=serviceAccount:$SA --role=roles/storage.objectAdmin
   ```
3. **OAuth consent screen** (Google Auth Platform > Audience): External, left in
   **Testing**, with each person under **Test users**. Testing mode is a free
   extra allowlist, so leave it there.
4. **An OAuth client for IAP.** Google only provides one automatically inside a
   Workspace org; a personal project gets `Empty Google Account OAuth client
   ID(s)/secret(s)` until you supply your own. Create a Web application client
   (Google Auth Platform > Clients), add the redirect URI
   `https://iap.googleapis.com/v1/oauth/clientIds/<CLIENT_ID>:handleRedirect`,
   keep the secret in Bitwarden, then:
   ```sh
   gcloud iap web enable --resource-type=app-engine --project=<project> \
     --oauth2-client-id=<CLIENT_ID> --oauth2-client-secret=<CLIENT_SECRET>
   ```
5. **IAP access**, per person (project ownership does not count):
   ```sh
   gcloud iap web add-iam-policy-binding --resource-type=app-engine --project=<project> \
     --member=user:<email> --role=roles/iap.httpsResourceAccessor
   ```
6. Billing > Budgets: a $1 alert. Nothing here should cost money (one F1
   instance, max), so the alert is how a mistake gets noticed.

**Adding a person means three lists and a redeploy**: an OAuth test user (3),
an IAP grant (5), and `serve.allowed_emails` in `megh.yaml`, which the app reads
only at startup and so needs a redeploy.

**Signing out**: the page's Sign out button forgets the keys and loads
`/?gcp-iap-mode=CLEAR_LOGIN_COOKIE`, IAP's own sign-out. Google itself stays
signed in, so IAP will sign the same account straight back in.

**Every refusal looks different, which is how you find it:**

| Symptom | Cause |
|---|---|
| `503 Service Unavailable` | The app exited at startup. `gcloud app logs read` names why: an empty allowlist, unreachable IAP keys, or the project number lookup |
| Log: `serve.allowed_emails is empty` with `config from /workspace/megh.yaml` | The file arrived but `serve:` is not at the top level. A key indented under another block is silently ignored |
| `Empty Google Account OAuth client ID(s)/secret(s)` | Step 4 |
| `Access blocked` / `Error 403: access_denied` at sign-in | Not an OAuth test user (step 3) |
| Google's "You don't have access" page | No IAP grant (step 5), or it has not propagated yet (minutes) |
| Plain `not authorized` | Past IAP, refused by the app: not in `serve.allowed_emails`. The log names the email |

**Deploy** from a checkout with your private `megh.yaml` at the repo root
(gitignored, but `.gcloudignore` uploads it) holding `serve.allowed_emails`:

```sh
cp ~/dotfiles/megh/megh.yaml .
gcloud app deploy --project <project-id>
```

The app reads its defaults (data center, volume, `extra_pubkeys`, tailnet)
from that file. Boxes it launches carry only `extra_pubkeys`, since the
server has no key of its own, so that list must hold a key you can actually
use (SETUP §6.7).

### 7.1 Keys on the server (optional)

Pasting keys into a page means trusting every extension that page runs
beside, and a work-managed browser can force-install extensions you cannot
remove. For those, keep the keys in Secret Manager instead and the page stops
asking for them.

1. Store the same note you keep in Bitwarden as **one** secret (one active
   version, inside the six that are free every month), in a single region:
   ```sh
   gcloud secrets create megh-control --project meghplane \
     --replication-policy=user-managed --locations=us-central1 --data-file=-
   # paste the note, then Ctrl-D
   ```
2. Let only the app's **runtime** service account read it, on that secret alone:
   ```sh
   gcloud secrets add-iam-policy-binding megh-control --project meghplane \
     --member=serviceAccount:<runtime-sa> --role=roles/secretmanager.secretAccessor
   ```
3. Add `secret: megh-control` under `serve:` in `megh.yaml` and redeploy. The
   startup log reports `secret megh-control holds runpod=true tailscale=true`,
   names only.

**Rotating** means `gcloud secrets versions add megh-control --data-file=-`,
then **destroying** the old version (`gcloud secrets versions destroy <n>`).
A disabled version still counts against the free six. The app re-reads the
secret every 10 minutes, so a rotation lands without a redeploy.

**Split the build account from the runtime account once a secret is in play.**
If one service account both runs Cloud Build and reads the secret, anything
that can trigger a build can reach your keys. Give the build roles
(`cloudbuild.builds.builder`, `logging.logWriter`, the staging bucket) to a
build-only account, and leave the runtime account with `secretAccessor` on
`megh-control` and nothing else.

What it costs you is the second factor. With the keys on the server, getting
past IAP and `serve.allowed_emails` is enough to launch and terminate boxes, so
your Google account's 2FA is now the whole lock.

### 7.2 Setting up a box from inside it

A box launched from meghplane and entered through Tailscale's browser console
(or webterm) has had nothing done to it: no control machine ran `megh ssh` or
`megh hydrate`, so `files:` never copied `megh.yaml` or `box-envvars` in, no
repo was cloned, and the dotfiles your shell expects are missing. Everything
below runs on the box, with no RunPod key and no phone.

```sh
gh auth login -h github.com -p https -w   # once per volume: ~/.config/gh is persisted
megh config pull                          # your private megh.yaml -> ~/.config/megh/megh.yaml (on the volume)
megh hydrate                              # on a box this runs locally; no agent here, so it clones over https via gh
exec zsh
```

Then paste your `box-envvars` note with its target set to
`/mnt/work/state/personal/envvars`, the path the box's `~/personal/envvars`
points at.

**Choose the GitHub credential deliberately.** It lives on the volume, so every
later box on that volume inherits it, and without a forwarded agent it is also
how you push. `gh auth login` grants your account's full repo access. A
fine-grained PAT limited to the repos you work in (contents read/write) is the
narrower choice: `gh auth login --with-token < pat.txt`. Boxes reached through
`megh ssh` never need either, since they clone and push with your forwarded key.

**Old deploy images pile up.** Each deploy stores a build image; add a cleanup
policy in Artifact Registry keeping the last few so storage stays free.

## What is not here yet

- The mesh (Headscale/Tailscale) so you reach the box by name without RunPod's
  proxy. For box #1 the proxy URLs are enough to get a feel.
- The Hetzner backend, the `megh down/list/shell` verbs, and the shutdown flush
  hook. Those come once the RunPod box feels right and we know what to tune.
