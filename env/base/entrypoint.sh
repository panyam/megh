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
x11vnc -display :99 -forever -shared -nopw -rfbport 5900 -bg -o /tmp/x11vnc.log
websockify --web=/usr/share/novnc 6080 localhost:5900 >/tmp/novnc.log 2>&1 &
log "noVNC up on :6080 (headed display :99)"

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
ttyd -p 7681 -W -t titleFixed=megh tmux new -A -s main >/tmp/ttyd.log 2>&1 &
log "ttyd up on :7681 (tmux session 'main')"

cat <<EOF
[megh] box is up.
       web shell : http://<host>:7681
       headed vnc: http://<host>:6080/vnc.html
       ssh       : ssh root@<host> (key auth, agent forwarding)
       work dir  : /mnt/work  ->  ${WORK_MOUNT}
EOF

# PID 1 stays alive; the services above are backgrounded.
exec tail -f /dev/null
