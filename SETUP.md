# megh: first box on RunPod

This gets you a working dev box to play with. It runs the `megh-full` image on a
RunPod CPU pod, backed by a network volume for scratch, reachable through a web
shell, a headed-browser view, and SSH.

The image is the reusable piece. The launch is scripted, but for the very first
box the RunPod console path is the simplest way to see everything work. Both are
below.

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

or, through the `gh` login from step 6.2, which also works if the repo is ever
made private:

```sh
gh api repos/panyam/megh/contents/install.sh -H 'Accept: application/vnd.github.raw' | sh
```

With `gh` logged in the installer downloads the release through it; otherwise
it uses plain `curl`, which only works while the repo is public.

It picks the artifact for the machine, verifies the checksum, installs to
`$PREFIX/bin` on Termux (`~/.local/bin` elsewhere), and writes
`~/.config/megh/megh.yaml` without overwriting one already there. Re-run it to
upgrade. `MEGH_TARGET` and `MEGH_INSTALL_DIR` override the guesses.

The **config** is the one part that still needs auth. A real `megh.yaml` names
every repo you work on, so it lives in a private repo of yours: by default
`<your GitHub login>/dotfiles`, file `megh/megh.yaml`. The installer fetches it
with `gh` when you are logged in, and otherwise installs `megh.yaml.example` and
says so, which still leaves you a working binary. `MEGH_CONFIG_REPO` and
`MEGH_CONFIG_PATH` point it elsewhere, for both the installer and
`megh config pull`.

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

**Keep the master copy in a password manager's secure note** (e.g. Bitwarden), and make the note the whole
bootstrap: a script that writes itself to `~/megh-bootstrap.sh` via a heredoc and
then runs it, so §6.1–6.5 are one copy and one paste in Termux. Write-then-run
matters: pasted line by line, `gh auth login` reads the following pasted lines
as its answers. The script should `set +o history`, `trap` its own deletion
(it holds the values), write `secrets.env` under `umask 077`, and skip steps
already done so re-pasting is also the upgrade and rotation path.

### 6.6 Launch

```sh
megh config
megh regions probe --dc <dc> --first -y      # capacity flaps; a probe costs a fraction of a cent
megh up devbox
megh ssh devbox                              # lands in tmux `main`
```

### 6.7 Reaching a phone-launched box from other machines

A box trusts the launcher's key plus every key in `extra_pubkeys:` in
`megh.yaml`. Keep one SSH key in a password manager that can act as an SSH
agent (e.g. Bitwarden desktop: Settings > Enable SSH agent, then point
`SSH_AUTH_SOCK` at its socket), put its public half there, and enable that agent
on any machine you want in from. That machine needs no megh, profile, provider
key or tailnet: open the portal, copy the box's `ssh -p <port> root@<ip>` line
(or the `-L 7682:...` one for webterm), and the agent asks to approve each use. Tailnet members can use `ssh root@<box>`
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

The note needs at least one provider key, `RUNPOD_API_KEY`, `HCLOUD_TOKEN` or
`VULTR_API_KEY`, and the page works with whichever backends it holds a key for.
The provider menu still lists all three, marking the ones with no key. The
Keys panel at the top says where each key comes from (the server, this tab, or
missing), and pasting a line there adds that key beside the rest, so a Vultr
key can sit in the tab while RunPod's stays in Secret Manager. Boxes and
volumes from every keyed provider share one list, and a launch goes to the
provider of the volume you pick. The Regions section probes RunPod's data
centers, and for Hetzner and Vultr lists the locations selling the chosen size
from their price lists, since those have nothing to probe.

A Hetzner or Vultr box pulls the image itself on first boot, so for a private
image the note also needs the registry token, under the same name your secrets
file uses (`registries[0].token_env`, `GH_MEGH_TOKEN` by default). Without it
the VM boots and the pull fails. RunPod never needs it, since its pull
credential lives in the RunPod console.

**A Vultr key used by meghplane must allow all IPs.** App Engine has no fixed
outbound address, so the key's IP allowlist (section 9) cannot name it.

It checks who you are twice. IAP signs you in at Google's edge, and the app
then verifies IAP's signed header itself (audience, issuer, signature, expiry,
via oneauth) and admits only the emails in `serve.allowed_emails`. With that
list empty, or IAP's keys unreachable, it refuses to start.

**One-time setup.** Tested on a personal (non-Workspace) Google account.

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
   keep the secret in your password manager, then:
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
megh config pull                       # refresh ~/.config/megh/megh.yaml from your config repo
cp ~/.config/megh/megh.yaml .
gcloud app deploy --project <project>
```

The app reads its defaults (data center, volume, `extra_pubkeys`, tailnet)
from that file. Boxes it launches carry only `extra_pubkeys`, since the
server has no key of its own, so that list must hold a key you can actually
use (SETUP §6.7).

### 7.1 Keys on the server (optional)

Pasting keys into a page means trusting every extension that page runs
beside, and a managed browser can force-install extensions you cannot
remove. For those, keep the keys in Secret Manager instead and the page stops
asking for them.

1. Store your control-plane note as **one** secret (one active
   version, inside the six that are free every month), in a single region:
   ```sh
   gcloud secrets create megh-control --project <project> \
     --replication-policy=user-managed --locations=us-central1 --data-file=-
   # paste the note, then Ctrl-D
   ```
2. Let only the app's **runtime** service account read it, on that secret alone:
   ```sh
   gcloud secrets add-iam-policy-binding megh-control --project <project> \
     --member=serviceAccount:<runtime-sa> --role=roles/secretmanager.secretAccessor
   ```
3. Add `secret: megh-control` under `serve:` in `megh.yaml` and redeploy. The
   startup log reports `secret megh-control holds runpod=true hetzner=false
   vultr=false tailscale=true registry=true`, names only.

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
`megh hydrate`, so `files:` never copied `megh.yaml` or your box env file in, no
repo was cloned, and the dotfiles your shell expects are missing. Everything
below runs on the box, with no RunPod key and no phone.

```sh
gh auth login -h github.com -p https -w   # once per volume: ~/.config/gh is persisted
megh config pull                          # your private megh.yaml -> ~/.config/megh/megh.yaml (on the volume)
megh hydrate                              # on a box this runs locally; no agent here, so it clones over https via gh
exec zsh
```

Then paste your box env file (the service tokens a box needs) with its target
set to the volume path your `files:` entry maps it to, the path your shell rc
expects through `symlinks:`.

**Choose the GitHub credential deliberately.** It lives on the volume, so every
later box on that volume inherits it, and without a forwarded agent it is also
how you push. `gh auth login` grants your account's full repo access. A
fine-grained PAT limited to the repos you work in (contents read/write) is the
narrower choice: `gh auth login --with-token < pat.txt`. Boxes reached through
`megh ssh` never need either, since they clone and push with your forwarded key.

**Old deploy images pile up.** Each deploy stores a build image; add a cleanup
policy in Artifact Registry keeping the last few so storage stays free.

## 8. Hetzner Cloud: a second provider

A Hetzner box is a VM that runs the same megh image under Docker, with its
volume at `/workspace`, so everything inside the box behaves as on RunPod. What
differs is outside: you pick any size per launch, and when RunPod's CPU pool is
dry Hetzner is a separate pool.

1. Create a Hetzner Cloud project and an API token with read/write access
   (Security > API tokens). Put it in your secrets file as `HCLOUD_TOKEN`.
2. Create a volume in a location. US locations are `ash` (Ashburn) and `hil`
   (Hillsboro). Hetzner formats it at creation:
   ```sh
   megh storage create --provider hetzner --name megh-work --size 50 --dc ash
   ```
3. Launch, at whatever size this session needs:
   ```sh
   megh up dev --provider hetzner --volume <id> --vcpu 4 --ram 8
   ```
   megh picks the cheapest current x86 server type in the volume's location
   with at least that many cores, that much memory, and the `--disk` you ask
   for. First boot installs Docker and pulls the image, so allow a few minutes
   before `ssh -p 2222 root@<vm-ip>` (printed by `up`) or the tailnet answers.
4. Set `providers.hetzner.default_volume`/`default_dc` (and `default_provider:
   hetzner` if it becomes your main one) so `up` needs no flags.

How it is put together:

- **The VM is only a Docker host.** Its own sshd is switched off; you log into
  the box, whose sshd is published on port 2222.
- **The image pull uses your registry token** (`registries[0].token_env`, e.g.
  `GH_MEGH_TOKEN`) in the VM's first-boot script, which logs in, pulls, and
  logs out. The box is cut off from the metadata service, where that script
  could otherwise be read. Public images skip the login entirely.
- **Terminate deletes the VM; the volume survives** and attaches to the next
  box in its location, so `megh hydrate` and your logins carry over, exactly as
  with a RunPod network volume.
- **meghplane** launches Hetzner boxes too, with `HCLOUD_TOKEN` in its note
  (section 7). `megh regions offers --provider hetzner --vcpu 4 --ram 8` lists
  every server type a location sells, with prices; launch one with
  `megh up --type <type>`.

## 9. Vultr: another provider, with more US regions

A Vultr box has the same shape as a Hetzner one (section 8): a VM running the
megh image under Docker, sized per launch from Vultr's plans. Vultr has 9 US
regions (`ewr` New Jersey, `ord` Chicago, `atl`, `sea`, `mia`, `dfw`, `sjc`,
`lax`, `hnl`).

1. Create an API key (Account > API). **Vultr API keys have an IP
   allowlist**: add the addresses of every machine that runs megh with it, or
   allow all, or every call fails with an authorization error.
2. Put it in your secrets file as `VULTR_API_KEY`.
3. Create a volume. It is NVMe (`high_perf`) unless `providers.vultr.block_type`
   says `storage_opt` (HDD, cheaper, not in every region):
   ```sh
   megh storage create --provider vultr --name megh-work --size 50 --dc ewr
   ```
4. Launch at the size you need:
   ```sh
   megh up dev --provider vultr --volume <id> --vcpu 4 --ram 8
   ```
   megh creates the instance, then attaches the volume, retrying for up to
   about three minutes while Vultr finishes creating it. If it never attaches,
   megh terminates the instance so nothing bills with no volume to boot onto.

**A Vultr volume arrives blank, so the first box formats it, once.** The boot
script picks the one unpartitioned, unmounted disk whose size matches the
volume exactly, asks `blkid -p` whether it holds a filesystem, and formats it
only when the answer is a definite "no" (exit 2). A filesystem means mount it
as is; any other answer stops the boot without formatting, because a failed
launch is recoverable and a wiped volume is not. Later boxes on the same volume
only mount it, and `/workspace/.megh-volume` records when it was first
formatted. If the first boot ever stops at this step, `/var/log/megh-boot.log`
(through Vultr's web console) says which check refused.

## 10. A gateway: workers from a machine that is not on the tailnet

`megh gw up` runs a docker container on this machine that joins the tailnet
in its place, so the machine itself never runs Tailscale (DESIGN.md "Roles"). It
needs docker, `tailnet:` in megh.yaml, the Tailscale client id and secret (to
mint the gateway's key), and the gateway image.

That image is its own small one (`env/gw/Dockerfile`): the official tailscale
image plus the megh binary, nothing from the dev image. CI publishes it as
`megh-gw` for amd64 and arm64 on every merge that touches megh's code, so a Mac
pulls it rather than building anything. It is private like the dev images, so
log in once with `echo $GH_MEGH_TOKEN | docker login ghcr.io -u <you>
--password-stdin`. To run one built from a local checkout instead, use `make
image-local-gw` and set `tailscale.gateway_image` to the tag it prints.

Three ACL edits first, since the gateway joins under its own tag:

```jsonc
"tagOwners": {
  "tag:megh":    ["autogroup:admin"],
  "tag:megh-gw": ["autogroup:admin", "tag:megh"],  // tag:megh lets the OAuth client mint it
},
"grants": [
  // the gateway reaches the workers' web surfaces and ssh, and nothing else
  {"src": ["tag:megh-gw"], "dst": ["tag:megh"], "ip": ["tcp:22", "tcp:7681", "tcp:7682", "tcp:8080", "tcp:6080"]},
],
"ssh": [
  // workers run Tailscale SSH, which answers :22 itself and authorizes the NODE
  {"action": "accept", "src": ["tag:megh-gw"], "dst": ["tag:megh"], "users": ["root"]},
],
```

If the credential was created with `tag:megh` only, edit it to add
`tag:megh-gw` as well; otherwise minting fails and `up` says so. Set
`tailscale.gateway_tag` to use a different tag.

```
megh gw up            # start (or restart) the container, join, print the URLs
megh gw status        # container state, whether it is on the tailnet, the URLs
megh gw down          # leave the tailnet and remove it (--purge drops its state volume)
megh gw shell         # a shell inside the container (tailscale status, logs)
```

Then open `http://dev.localhost:7682/` for webterm on `dev` (`?arg=book` for the
`book` tmux session), `:7681` for ttyd, `:8080` for code-server, `:6080` for
noVNC. These are plain HTTP between the browser and the container, which is all
on this machine; the hop to the worker is TLS when the tailnet has certificates
on, and WireGuard either way. A phone clipboard still needs HTTPS, so on a phone
use the tailnet directly rather than a gateway.

**SSH goes through it too, with nothing to configure.** While the gateway is
running, `megh ssh dev` to any cloud box (and `tmux attach`, `browse`, `hydrate`,
`enable`, `doctor`, `mesh`) goes by the box's tailnet name and adds
`ProxyCommand docker exec -i megh-gw megh gw nc %h %p`, so ssh runs here, with
this machine's profile and its scoped GitHub agent, and only the bytes cross the
container. megh says so on stderr when it does. It takes this route even for a
box with public SSH, because a box launched from meghplane trusts none of this
machine's keys, and the tailnet route works whoever launched it. Because workers run Tailscale
SSH, the worker authorizes the gateway node by the ssh rule above rather than by
a box key, which means anyone who can `docker exec` on this machine can be root
on a worker. That's the same reach the published terminal ports already give.
`megh gw shell` opens a shell in the container for `tailscale status` and
friends.

The gateway rejoins on its own after a `docker restart` (its state is in the
`megh-gw-tailscale` volume). Its node is ephemeral, so after a long time stopped
the tailnet drops it; `megh gw up` notices and mints a new key.

## What is not here yet

- The mesh (Headscale/Tailscale) so you reach the box by name without RunPod's
  proxy. For box #1 the proxy URLs are enough to get a feel.
- The Hetzner backend, the `megh down/list/shell` verbs, and the shutdown flush
  hook. Those come once the RunPod box feels right and we know what to tune.
