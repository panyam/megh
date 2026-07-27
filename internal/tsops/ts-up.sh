#!/usr/bin/env bash
# megh Tailscale bring-up — the SINGLE SOURCE OF TRUTH for getting a box onto the
# tailnet and serving its localhost surfaces there. Container providers (RunPod)
# have no TUN device, so tailscaled runs in userspace mode and `tailscale serve`
# bridges the surfaces: ttyd :7681, webterm :7682, noVNC :6080, code :8080.
#
# Run from the same bytes in two places (both via the megh binary that embeds it):
#   - at boot, the entrypoint runs `megh doctor ts start --local`
#   - from your machine, `megh doctor ts <action> [box]` pipes this over SSH
# so boot and repair can never drift.
#
# Reads from the environment:
#   TS_HOSTNAME  the tailnet name to come up as (default: megh-box)
#   TS_AUTHKEY   optional; when set, `up` (re)authenticates with it, otherwise it
#                reuses the box's existing auth state
#
# Actions (argument 1, default `up`): up | restart | down | status | logs
set -u

SOCK=/var/run/tailscale/tailscaled.sock
STATE=/var/lib/tailscale/tailscaled.state
UPLOG=/tmp/tailscale-up.log
DLOG=/tmp/tailscaled.log
HOST="${TS_HOSTNAME:-megh-box}"

ts() { tailscale --socket="$SOCK" "$@"; }

start_daemon() {
  mkdir -p /var/lib/tailscale /var/run/tailscale
  # setsid so the daemon survives the transient shell that launched it (at boot it
  # is a grandchild of PID 1; it reparents to PID 1 and keeps running).
  setsid tailscaled --tun=userspace-networking --state="$STATE" --socket="$SOCK" >"$DLOG" 2>&1 &
  for _ in $(seq 1 20); do [ -S "$SOCK" ] && return 0; sleep 0.5; done
  echo "[ts] tailscaled socket did not appear (see $DLOG)"
  return 1
}

# Prefer HTTPS: browsers only expose the clipboard API (copy/paste) in a secure
# context, i.e. HTTPS or localhost. A phone reaching the box by tailnet name over
# plain HTTP has no clipboard. Fall back to HTTP when the tailnet has no HTTPS
# certs enabled (admin console -> DNS -> HTTPS Certificates), so the box is still
# reachable either way. HTTPS also requires the full MagicDNS name in the URL.
SERVE_SCHEME=http
serve_port() { ts serve --bg --"$SERVE_SCHEME"="$1" "http://127.0.0.1:$1" >/dev/null 2>&1; }
do_serve() {
  # Probe once with :7681; if HTTPS certs exist the serve succeeds.
  if ts serve --bg --https=7681 http://127.0.0.1:7681 >/dev/null 2>&1; then SERVE_SCHEME=https; else SERVE_SCHEME=http; fi
  serve_port 7681 || echo "[ts] serve 7681 failed"
  serve_port 7682 || echo "[ts] serve 7682 failed"
  command -v Xvfb >/dev/null 2>&1 && { serve_port 6080 || echo "[ts] serve 6080 failed"; }
  serve_port 8080 || echo "[ts] serve 8080 failed"
}

do_up() {
  [ -S "$SOCK" ] || start_daemon || return 1
  up_args=(--hostname="$HOST" --ssh)
  [ -n "${TS_AUTHKEY:-}" ] && up_args=(--authkey="$TS_AUTHKEY" "${up_args[@]}")
  # timeout so an unauthenticated `up` (no key, expired state) can't hang on the
  # login prompt over a non-interactive SSH session.
  if timeout 45 tailscale --socket="$SOCK" up "${up_args[@]}" >"$UPLOG" 2>&1; then
    do_serve
    local fqdn; fqdn=$(ts status --json 2>/dev/null | jq -r '.Self.DNSName // ""' | sed 's/\.$//')
    if [ "$SERVE_SCHEME" = https ]; then
      echo "[ts] up as $HOST; surfaces served over HTTPS — e.g. https://${fqdn:-$HOST}:7682 (webterm)"
    else
      echo "[ts] up as $HOST; surfaces served over HTTP at http://$HOST:7682 (webterm)."
      echo "[ts] For mobile copy/paste, enable HTTPS certs on your tailnet (admin > DNS), then: megh doctor ts restart $HOST"
    fi
    ts status 2>/dev/null | head -1
  else
    echo "[ts] 'tailscale up' failed (expired/invalid key? try: megh doctor ts setkey):"
    cat "$UPLOG" 2>/dev/null
    return 1
  fi
}

case "${1:-up}" in
  up)      do_up ;;
  restart) echo "[ts] restarting tailscaled"; pkill -x tailscaled 2>/dev/null; sleep 1; do_up ;;
  down)    if [ -S "$SOCK" ]; then ts down && echo "[ts] disconnected (auth state kept)"; else echo "[ts] tailscaled not running"; fi ;;
  status)  if [ -S "$SOCK" ]; then ts status; else echo "[ts] tailscaled not running"; fi ;;
  logs)
    echo "== /tmp/tailscale-up.log =="; cat "$UPLOG" 2>/dev/null || echo "(none)"
    echo; echo "== /tmp/tailscaled.log (last 30) =="; tail -n 30 "$DLOG" 2>/dev/null || echo "(none)"
    echo; echo "== tailscale status =="; if [ -S "$SOCK" ]; then ts status 2>&1 | head -20; else echo "tailscaled not running"; fi
    ;;
  *) echo "usage: ts-up.sh {up|restart|down|status|logs}"; exit 2 ;;
esac
