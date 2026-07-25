#!/usr/bin/env bash
# Enable the headed-browser display (Xvfb + x11vnc + noVNC on :6080) on demand.
#
# Baked into every megh image. The full (base) flavor starts this at boot; slim
# defers it. On a slim box run `megh enable-vnc` (this script) when you actually
# need a headed browser: it installs the display stack to the box's LOCAL disk if
# missing, starts it, and serves it on the tailnet. Idempotent.
set -uo pipefail
log() { echo "[megh-vnc] $*"; }

# 1. Install the display stack if missing (slim). No-op on base.
if ! command -v Xvfb >/dev/null 2>&1; then
  log "installing headed-browser display stack (xvfb, x11vnc, fluxbox, novnc)"
  export DEBIAN_FRONTEND=noninteractive
  if ! { apt-get update -qq && apt-get install -y --no-install-recommends \
           xvfb x11vnc fluxbox novnc websockify; } >/tmp/enable-vnc-apt.log 2>&1; then
    log "apt install failed (see /tmp/enable-vnc-apt.log)"
    exit 1
  fi
fi

# 2. Start the stack (only what is not already running).
if ! pgrep -x Xvfb >/dev/null 2>&1; then
  Xvfb :99 -screen 0 1920x1080x24 >/tmp/xvfb.log 2>&1 &
  sleep 1
fi
if ! pgrep -x fluxbox >/dev/null 2>&1; then
  DISPLAY=:99 fluxbox >/tmp/fluxbox.log 2>&1 &
fi
if ! pgrep -x x11vnc >/dev/null 2>&1; then
  x11vnc -display :99 -forever -shared -nopw -localhost -rfbport 5900 -bg -o /tmp/x11vnc.log
fi
if ! pgrep -f 'websockify.*6080' >/dev/null 2>&1; then
  websockify --web=/usr/share/novnc 127.0.0.1:6080 localhost:5900 >/tmp/novnc.log 2>&1 &
fi
log "noVNC up on 127.0.0.1:6080 (DISPLAY=:99)"

# 3. Serve on the tailnet if Tailscale is up; otherwise reach via an SSH tunnel.
if tailscale ip -4 >/dev/null 2>&1; then
  if tailscale serve --bg --http=6080 http://127.0.0.1:6080 >/tmp/ts-serve-vnc.log 2>&1; then
    log "served: http://$(hostname):6080/vnc.html (tailnet)"
  else
    log "tailscale serve 6080 failed (see /tmp/ts-serve-vnc.log)"
  fi
else
  log "tailscale not up; reach via SSH tunnel: ssh -L 6080:localhost:6080 ..."
fi

log "run a headed app against DISPLAY=:99 (Playwright: npx playwright install --with-deps chromium)"
