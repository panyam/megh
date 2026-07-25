#!/usr/bin/env bash
# Feature: code — code-server (VS Code in the browser) on :8080. Installs to the
# box's local disk if missing, starts it, and serves it on the tailnet. Idempotent.
# (The slim flavor already background-installs this on boot; this is the explicit
# on-demand path and a no-op if it is already up.)
set -uo pipefail
log() { echo "[megh-enable] $*"; }

if ! command -v code-server >/dev/null 2>&1; then
  log "installing code-server"
  curl -fsSL https://code-server.dev/install.sh | sh >/tmp/enable-code.log 2>&1 \
    || { log "install failed (see /tmp/enable-code.log)"; exit 1; }
fi

if ! pgrep -f 'code-server' >/dev/null 2>&1; then
  code-server --bind-addr 127.0.0.1:8080 --auth none /mnt/work >/tmp/code-server.log 2>&1 &
fi
log "code-server up on 127.0.0.1:8080"

if tailscale ip -4 >/dev/null 2>&1; then
  tailscale serve --bg --http=8080 http://127.0.0.1:8080 >/tmp/ts-serve-code.log 2>&1 \
    && log "served on the tailnet at :8080 (open http://<box-name>:8080)" \
    || log "tailscale serve 8080 failed (see /tmp/ts-serve-code.log)"
else
  log "tailscale not up; reach via SSH tunnel: ssh -L 8080:localhost:8080 ..."
fi
