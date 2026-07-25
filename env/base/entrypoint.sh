#!/usr/bin/env bash
# megh dev-box entrypoint.
#
# Runs as PID 1 inside the dev container. Its whole job is to make a fresh,
# disposable box usable: accept an SSH key, bring up the web shell and the
# headed-browser display, wire the scratch volume into a stable layout, and
# hydrate persistent state from that volume. Nothing here is precious. Destroy
# the box and this replays.
set -euo pipefail

log() { echo "[megh] $*"; }

# ---------------------------------------------------------------------------
# 1. Scratch volume layout.
#
# WORK_MOUNT is where the provider mounts the network/block volume. RunPod
# network volumes default to /workspace. Everything the box "keeps" lives on
# the volume so a rebuild of the box loses nothing but a few minutes of state.
# ---------------------------------------------------------------------------
WORK_MOUNT="${WORK_MOUNT:-/workspace}"
ARCH_TAG="${ARCH_TAG:-x86_64}"

mkdir -p \
  "${WORK_MOUNT}/repos" \
  "${WORK_MOUNT}/worktrees" \
  "${WORK_MOUNT}/state/claude" \
  "${WORK_MOUNT}/state/codex" \
  "${WORK_MOUNT}/cache/${ARCH_TAG}"

# Stable path used everywhere else, independent of the provider's mount point.
ln -sfn "${WORK_MOUNT}" /mnt/work

# Persist agent state on the volume by pointing the agents' home dirs at it.
# Only link if the real dir is empty, so we never clobber existing config.
link_state() {
  local home_dir="$1" vol_dir="$2"
  if [ -e "${home_dir}" ] && [ ! -L "${home_dir}" ]; then
    # A real dir already exists (image default). Move its contents once.
    cp -a "${home_dir}/." "${vol_dir}/" 2>/dev/null || true
    rm -rf "${home_dir}"
  fi
  ln -sfn "${vol_dir}" "${home_dir}"
}
link_state /root/.claude "${WORK_MOUNT}/state/claude"
link_state /root/.codex  "${WORK_MOUNT}/state/codex"

# ---------------------------------------------------------------------------
# 2. SSH. RunPod injects the user's key as PUBLIC_KEY. Agent forwarding means
#    no long-lived git credentials ever land on the box.
# ---------------------------------------------------------------------------
mkdir -p /root/.ssh /run/sshd
chmod 700 /root/.ssh
if [ -n "${PUBLIC_KEY:-}" ]; then
  echo "${PUBLIC_KEY}" >> /root/.ssh/authorized_keys
  chmod 600 /root/.ssh/authorized_keys
  log "installed injected PUBLIC_KEY"
fi
ssh-keygen -A >/dev/null 2>&1 || true
/usr/sbin/sshd
log "sshd up on :22"

# ---------------------------------------------------------------------------
# 3. Headed-browser display: Xvfb -> fluxbox -> x11vnc -> noVNC on :6080.
#    Playwright launches headed against DISPLAY=:99 and you watch it in a
#    browser tab (laptop or phone).
# ---------------------------------------------------------------------------
Xvfb :99 -screen 0 1920x1080x24 >/tmp/xvfb.log 2>&1 &
sleep 1
fluxbox >/tmp/fluxbox.log 2>&1 &
# -localhost + a localhost websockify bind: these listen only on 127.0.0.1 and
# are reached via Tailscale (or an SSH tunnel), never the public proxy.
x11vnc -display :99 -forever -shared -nopw -localhost -rfbport 5900 -bg -o /tmp/x11vnc.log
websockify --web=/usr/share/novnc 127.0.0.1:6080 localhost:5900 >/tmp/novnc.log 2>&1 &
log "noVNC up on 127.0.0.1:6080 (headed display :99)"

# ---------------------------------------------------------------------------
# 4. Optional hydrate: clone repos listed in REPOS (comma-separated git URLs)
#    into the scratch volume if absent. Canonical source is always git; this is
#    just seeding the fast working copy.
# ---------------------------------------------------------------------------
if [ -n "${REPOS:-}" ]; then
  IFS=',' read -ra _repos <<< "${REPOS}"
  for url in "${_repos[@]}"; do
    name="$(basename "${url%.git}")"
    dest="${WORK_MOUNT}/repos/${name}"
    if [ ! -d "${dest}/.git" ]; then
      log "cloning ${url} -> ${dest}"
      git clone "${url}" "${dest}" || log "clone failed: ${url}"
    fi
  done
fi

# ---------------------------------------------------------------------------
# 5. Web shell: ttyd serving a persistent tmux session on :7681.
# ---------------------------------------------------------------------------
cd /mnt/work
# Bind to 127.0.0.1: no public listener. Reached via Tailscale or SSH tunnel.
ttyd -i 127.0.0.1 -p 7681 -W -t titleFixed=megh tmux new -A -s main >/tmp/ttyd.log 2>&1 &
log "ttyd up on 127.0.0.1:7681 (tmux session 'main')"

# code-server (VS Code in browser) on localhost, reached over tailscale/tunnel.
if command -v code-server >/dev/null 2>&1; then
  code-server --bind-addr 127.0.0.1:8080 --auth none /mnt/work >/tmp/code-server.log 2>&1 &
  log "code-server up on 127.0.0.1:8080"
fi

# ---------------------------------------------------------------------------
# 6. Tailscale (private mesh access). Container providers (RunPod) have no TUN
#    device, so tailscaled runs in userspace mode and `tailscale serve` bridges
#    the tailnet to the localhost surfaces above. Reach the box by name from any
#    device on your tailnet (laptop, phone) with nothing exposed publicly.
#    Skipped when TS_AUTHKEY is unset; then use SSH by ip:port + tunnels.
# ---------------------------------------------------------------------------
if [ -n "${TS_AUTHKEY:-}" ]; then
  mkdir -p /var/lib/tailscale /var/run/tailscale
  tailscaled --tun=userspace-networking \
    --state=/var/lib/tailscale/tailscaled.state \
    --socket=/var/run/tailscale/tailscaled.sock >/tmp/tailscaled.log 2>&1 &
  for _ in $(seq 1 20); do [ -S /var/run/tailscale/tailscaled.sock ] && break; sleep 0.5; done
  if tailscale up --authkey="${TS_AUTHKEY}" --hostname="${TS_HOSTNAME:-megh-box}" --ssh \
       >/tmp/tailscale-up.log 2>&1; then
    tailscale serve --bg --http=7681 http://127.0.0.1:7681 >/tmp/ts-serve-ttyd.log 2>&1 \
      || log "tailscale serve 7681 failed (see /tmp/ts-serve-ttyd.log)"
    tailscale serve --bg --http=6080 http://127.0.0.1:6080 >/tmp/ts-serve-vnc.log 2>&1 \
      || log "tailscale serve 6080 failed (see /tmp/ts-serve-vnc.log)"
    tailscale serve --bg --http=8080 http://127.0.0.1:8080 >/tmp/ts-serve-code.log 2>&1 \
      || log "tailscale serve 8080 failed (see /tmp/ts-serve-code.log)"
    log "tailscale up as '${TS_HOSTNAME:-megh-box}'; surfaces served on the tailnet"
  else
    log "tailscale up failed (see /tmp/tailscale-up.log); SSH by ip:port still works"
  fi
else
  log "TS_AUTHKEY unset; skipping tailscale (use SSH by ip:port + tunnels)"
fi

# ---------------------------------------------------------------------------
# 7. Session persistence. Flush agent transcripts to a durable, searchable git
#    repo on a timer and at shutdown, so history survives this disposable box.
#    Off unless MEGH_SESSIONS_REPO is set. See flush-sessions.sh.
# ---------------------------------------------------------------------------
flush() { /usr/local/bin/megh-flush-sessions || true; }
on_term() { log "shutdown signal: flushing sessions"; flush; exit 0; }
trap on_term TERM INT

if [ -n "${MEGH_SESSIONS_REPO:-}" ]; then
  interval="${MEGH_FLUSH_INTERVAL:-300}"
  ( while true; do sleep "${interval}"; flush; done ) &
  log "session flush enabled -> ${MEGH_SESSIONS_REPO} (every ${interval}s + on shutdown)"
else
  log "MEGH_SESSIONS_REPO unset; session history stays on the volume only"
fi

cat <<EOF
[megh] box is up.
       tailnet   : http://${TS_HOSTNAME:-<box>}:7681            (web shell)
                   http://${TS_HOSTNAME:-<box>}:6080/vnc.html   (headed browser)
       ssh       : ssh -A root@<ip> -p <port>  (key auth; ip:port from launch output)
       tunnel    : ssh -A -L 7681:localhost:7681 -L 6080:localhost:6080 root@<ip> -p <port>
       work dir  : /mnt/work  ->  ${WORK_MOUNT}
EOF

# PID 1 stays alive but signal-responsive, so the shutdown flush trap can run.
tail -f /dev/null &
wait $!
