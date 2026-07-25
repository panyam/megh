#!/usr/bin/env bash
# Feature: vnc — headed-browser display (Xvfb + x11vnc + noVNC on :6080) on
# demand. The full (base) flavor starts this at boot; slim defers it. Installs
# the display stack + a terminal to the box's LOCAL disk if missing, starts it,
# and serves it on the tailnet. Idempotent.
#
# The display is blank until a GUI app draws on it. Run one against DISPLAY=:99
# (e.g. Playwright headed after `megh enable playwright`), or use the auto-started
# xterm / the fluxbox right-click menu.
set -uo pipefail
log() { echo "[megh-vnc] $*"; }

# 1. Install the display stack + a terminal if anything is missing. Idempotent.
if ! command -v Xvfb >/dev/null 2>&1 || ! command -v xterm >/dev/null 2>&1; then
  log "installing display stack + xterm (xvfb, x11vnc, fluxbox, novnc, xterm)"
  export DEBIAN_FRONTEND=noninteractive
  if ! { apt-get update -qq && apt-get install -y --no-install-recommends \
           xvfb x11vnc fluxbox novnc websockify xterm x11-xserver-utils; } \
         >/tmp/enable-vnc-apt.log 2>&1; then
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

# 3. A visible default: a background colour and a terminal, so it is not blank.
DISPLAY=:99 xsetroot -solid '#2e3440' >/dev/null 2>&1 || true
if ! pgrep -x xterm >/dev/null 2>&1; then
  DISPLAY=:99 xterm -geometry 120x34+40+40 >/tmp/xterm.log 2>&1 &
fi
log "noVNC up on 127.0.0.1:6080 (DISPLAY=:99; a terminal is open, right-click for the menu)"

# 4. Serve on the tailnet if Tailscale is up; otherwise reach via an SSH tunnel.
if tailscale ip -4 >/dev/null 2>&1; then
  if tailscale serve --bg --http=6080 http://127.0.0.1:6080 >/tmp/ts-serve-vnc.log 2>&1; then
    log "served on the tailnet at :6080 (open http://<box-name>:6080/vnc.html)"
  else
    log "tailscale serve 6080 failed (see /tmp/ts-serve-vnc.log)"
  fi
else
  log "tailscale not up; reach via 'megh browse 6080' (SSH tunnel)"
fi
