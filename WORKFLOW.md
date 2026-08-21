# megh workflow

The end-to-end operational flow for launching and reaching a dev box, plus the
provider realities learned the hard way. Read alongside `SETUP.md` (first-run
specifics) and `DESIGN.md` (why the architecture is shaped this way).

## Secrets (in `~/personal/envvars`, never in the repo)

The Makefile sources this file for every recipe that needs a secret.

| Var | What | Notes |
|-----|------|-------|
| `RUNPOD_API_KEY` | RunPod account key | launches, volumes, teardown |
| `GH_MEGH_TOKEN` | GitHub classic PAT, `read:packages` | lists/pulls the private image; also configured in RunPod as the ghcr.io registry credential |
| `TS_AUTHKEY` | Tailscale auth key | optional; only for phone/tablet access. reusable + ephemeral |
| `MEGH_SESSIONS_REPO` | `owner/repo` for session history | optional; e.g. `panyam/megh-sessions` (private) |
| `MEGH_SESSIONS_TOKEN` | fine-grained PAT, `contents:write` on that repo only | optional; pushes transcripts; narrow blast radius |

`make vars` shows which are set (no values printed).

## 1. Publish the dev-env image

The image is a build output, never a hand-mutated snapshot. Source of truth is
`env/base/provision.sh`; the `build-env` GitHub Action builds it for
`linux/amd64` and pushes to GHCR as `ghcr.io/panyam/megh-base:latest` (private).

```
make image        # git push origin HEAD -> triggers the build (~15 min first time)
make registry     # confirm the tag landed (needs GH_MEGH_TOKEN)
```

RunPod pulls the private image using a Container Registry Auth credential
(ghcr.io / panyam / a read:packages PAT), added once in the RunPod console. The
launcher attaches its id automatically (`registryAuthID` looks it up by host).

## 2. Placement: find storage + CPU in a close-enough region

The hard constraint: a network volume is pinned to one data center, and the pod
must run in that same DC. So you need a DC that has **both** volume support
**and** rentable CPU. Two realities make this fiddly:

- **There is no reliable availability API.** RunPod exposes flavors defined per
  DC, but "defined" is not "rentable." The only definitive signal is a real rent
  attempt: success, or `no longer any instances`. A failed attempt creates
  nothing and costs nothing, so iterating across DCs is safe.
- **CPU capacity flaps and varies by DC.** US-TX-3 was dry across every flavor;
  US-CA-2 had capacity.

`megh regions` automates the search. It reads the candidate DCs from the pods
schema in RunPod's published OpenAPI document, which is the closest thing to an
authoritative list (13 US regions as of 2026-08-20; the older hand-kept list
here named US-MO-2 and US-NE-1, which RunPod no longer accepts).

```
megh regions list                       # candidate DCs, marking where volumes already are
megh regions probe                      # rent-and-terminate in each; report who took it
megh regions probe --dc US-CA-2 --first # just check one, stop at the first that rents
megh regions place --name scratch --size 100   # probe, then create the volume where it rents
```

A probe is a real rent: it creates a pod with the shape you would launch, no
volume attached, then terminates it immediately. A refused create leaves nothing
behind, and an accepted one lives about a second and never pulls the image, so a
sweep costs a fraction of a cent. Probes run one region at a time, so at most one
probe pod exists at any moment. If one is ever left behind the command says so
loudly and prints the `megh down` line for it.

Placement by hand still works if you prefer: create the volume in a DC that
rents CPU (US-CA-2 worked), and launch there. If a launch returns `no
instances`, the DC went dry; move the volume (delete + recreate) to another and
retry.

Volume ops (one global view across providers):
```
megh storage list
megh storage create --dc US-CA-2 --size 100 --name megh-scratch-ca
megh storage rm <id>            # must be detached from all boxes first
```
A volume can be mounted by multiple boxes in the same DC at once (shared scratch
at /mnt/work). Keep each box/agent in its own worktrees/<project>/<task> dir to
avoid concurrent writes to the same file.

## 3. Launch

```
make up VOLUME=<vol-id> DC=<dc-id>          # 2 vCPU / 8 GB general, 20 GB container disk
make up VOLUME=<id> DC=<dc> VCPU=4 RAM=16   # bigger, if the DC has it
```

RunPod API realities baked into the backend (they differ from the docs):

- CPU pods use `computeType:"CPU"`, `vcpuCount`, and `cpuFlavorIds` (enum
  `cpu3c/g/m`, `cpu5c/g/m`; `c`=2 GB, `g`=4 GB, `m`=8 GB per vCPU). Not
  `cpuCount`/`memoryInGb`.
- `dataCenterIds` is an array; `ports` is an array; `env` is an object.
- **Container disk is capped by instance size** (~20 at 2 vCPU, up to ~60
  larger). This is the ephemeral OS disk, not your scratch. Real scratch is the
  100 GB network volume at `/mnt/work`. Default container disk is 20.

## 4. Access and security

Nothing sensitive is ever on RunPod's public proxy. The proxy is open and
unauthenticated, so exposing ttyd/noVNC there would put a root shell on the
public internet. The image binds those to `127.0.0.1` only.

- **Tailscale (primary).** RunPod containers have no TUN device, so tailscaled
  runs in userspace mode and `tailscale serve` bridges the tailnet to the
  localhost surfaces. Reach the box by name from any tailnet device including a
  phone: `http://<box-name>:7681` (shell), `http://<box-name>:6080/vnc.html`
  (headed browser). Brought up automatically when `TS_AUTHKEY` is set.
- **SSH by ip:port (break-glass).** Only `22/tcp` is public, key-auth only.
  From the RunPod console (Connect > TCP) get ip:port, then
  `ssh -A root@<ip> -p <port>`. Works even if Tailscale is down. Tunnel the web
  surfaces over it: `ssh -A -L 7681:localhost:7681 -L 6080:localhost:6080 ...`.

Tailscale prerequisites (one-time): a Tailscale account, the app installed and
signed in on your laptop/phone/tablet, MagicDNS enabled, and a reusable+ephemeral
auth key in `TS_AUTHKEY`.

## 5. Teardown

A powered-off pod still bills, so stop paying by deleting it.

```
curl -s -X DELETE -H "Authorization: Bearer $RUNPOD_API_KEY" \
  https://rest.runpod.io/v1/pods/<pod-id>
```

The network volume and its `/mnt/work` contents survive. An ephemeral Tailscale
node auto-leaves the tailnet. A rebuilt box hydrates from git + the volume.

## Lessons captured

- Trust the live API over the docs; the RunPod REST schema differed on nearly
  every CPU field.
- "Flavor defined in a DC" is not "instance rentable there." Only a real rent
  proves capacity.
- The public proxy is unauthenticated. Private surfaces bind to localhost and
  ride Tailscale or an SSH tunnel.
- Keep one always-secure path (SSH key auth by ip:port) independent of the
  convenience layer, so a mesh failure never locks you out.
