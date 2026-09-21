#!/usr/bin/env bash
# Feature: playwright — install Playwright + Chromium (and system deps) on a box,
# plus `pw-ui`, which serves the UI/trace/report viewer on :9323. The browsers go
# on the scratch volume so a rebuild does not re-download them. For HEADED runs,
# enable the display first (`megh enable vnc`) and launch against DISPLAY=:99.
# Idempotent. MEGH_PLAYWRIGHT_EMIT_ONLY=1 writes the launcher and nothing else.
set -uo pipefail
log() { echo "[megh-enable] $*"; }

# The viewer launcher. Playwright's three web surfaces -- UI mode, the trace
# viewer and the HTML report -- are ordinary HTTP servers, so watching a run
# needs no X display at all; only a LIVE headed browser does. They default to
# :9323, which is the port megh's surface catalog names, so `megh browse <box>
# 9323` reaches whichever one is up.
log "installing the pw-ui launcher (/usr/local/bin/pw-ui)"
cat > /usr/local/bin/pw-ui <<'PWUI'
#!/usr/bin/env bash
# megh: written by `megh enable playwright`. Serves one of Playwright's web
# viewers on 127.0.0.1:9323, which `megh browse <box> 9323` forwards.
#
#   pw-ui ui [args...]     UI mode: pick, run, step and watch
#   pw-ui trace [file]     trace viewer: a finished run, action by action
#   pw-ui report [dir]     the HTML report of the last run
#
# The browser under test still runs headless; what is on the port is the viewer.
# For a live headed browser, that is `megh enable vnc` and DISPLAY=:99.
set -uo pipefail

port="${PW_UI_PORT:-9323}"
host=127.0.0.1            # C4: a surface binds loopback. The tunnel is the way in.

usage() {
  echo "usage: pw-ui [ui|trace <file>|report [dir]]   (serves on 127.0.0.1:${port})"
  echo "  ui      UI mode: pick, run, step and watch      (needs @playwright/test)"
  echo "  trace   the trace viewer for a recorded run     (playwright-core is enough)"
  echo "  report  the HTML report of the last run         (needs @playwright/test)"
  echo "reach it from the control machine with: megh browse <box> ${port}"
}

mode="${1:-ui}"
[ $# -gt 0 ] && shift
case "$mode" in
  -h|--help|help) usage; exit 0 ;;
esac

# Prefer the PROJECT's own CLI over the global one. UI mode and the report live
# in @playwright/test, which is a project dependency, and a trace written by one
# version opens empty in another version's viewer.
find_cli() {
  dir="$PWD"
  while : ; do
    for c in playwright playwright-core; do
      if [ -x "$dir/node_modules/.bin/$c" ]; then echo "$dir/node_modules/.bin/$c"; return 0; fi
    done
    [ "$dir" = "/" ] && break
    dir="$(dirname "$dir")"
  done
  command -v playwright 2>/dev/null
}

cli="$(find_cli)"
if [ -z "$cli" ]; then
  echo "pw-ui: no playwright CLI in this project or on PATH (run: megh enable playwright)" >&2
  exit 1
fi

# playwright-core ships the trace viewer alone. Its `test` and `show-report` are
# stubs that print "Available in @playwright/test package" and exit 0, which from
# the outside looks like a viewer that started and then vanished.
case "$mode:$cli" in
  ui:*playwright-core|report:*playwright-core)
    echo "pw-ui: '$mode' needs @playwright/test; this project has playwright-core only." >&2
    echo "       record a trace (use: --trace on) and open it with: pw-ui trace <trace.zip>" >&2
    exit 1 ;;
esac

# Serve it on the tailnet while it is up, and take it back DOWN on the way out.
# A `tailscale serve --bg` outlives the process that asked for it, and a proxy
# pointing at a port nothing answers on is what makes a healthy mesh look broken
# (the same finding as :6080 being served whenever Xvfb is merely installed).
served=0
if tailscale ip -4 >/dev/null 2>&1 && tailscale serve --bg --http="$port" "http://127.0.0.1:$port" >/dev/null 2>&1; then
  served=1
  echo "pw-ui: also on the tailnet at http://$(hostname):${port}"
fi
cleanup() {
  if [ "$served" = 1 ]; then tailscale serve --http="$port" off >/dev/null 2>&1; fi
}
trap cleanup EXIT INT TERM

echo "pw-ui: ${mode} on http://${host}:${port} — from the control machine: megh browse $(hostname) ${port}"
case "$mode" in
  ui)     "$cli" test --ui --ui-host="$host" --ui-port="$port" "$@" ;;
  trace)  "$cli" show-trace --host "$host" --port "$port" "$@" ;;
  report) "$cli" show-report --host "$host" --port "$port" "$@" ;;
  *)      echo "pw-ui: unknown mode '$mode'" >&2; usage >&2; exit 2 ;;
esac
PWUI
chmod 0755 /usr/local/bin/pw-ui

# Emit-only: used at IMAGE BUILD time to bake the launcher into the image, the
# same way the webterm page is. Writes pw-ui and stops here — no npm, no
# chromium, no volume. The full flavor already installs the browser stack in
# provision.sh; this is what makes :9323 a first-class surface there rather than
# something `megh enable playwright` has to retrofit onto every box.
if [ "${MEGH_PLAYWRIGHT_EMIT_ONLY:-0}" = "1" ]; then
  log "emit-only; launcher written, skipping the browser install"
  exit 0
fi

# Chromium and friends are ~650 MB, which is 3% of a 20 GB container disk and
# re-downloaded on every new box if it lands there. Park it on the volume
# instead. Measured on a live slim box: seeding the volume copy takes ~5s, and
# the only runtime cost is the FIRST launch after boot paying a cold NFS page
# cache (649ms against 175ms). Every launch after that matches local disk
# (182ms). A box with no volume keeps playwright's default local path.
browsers_path=""
if [ -d /mnt/work ] && [ -w /mnt/work ]; then
  browsers_path="/mnt/work/cache/${ARCH_TAG:-$(uname -m)}/ms-playwright"
  mkdir -p "${browsers_path}" || browsers_path=""
fi
if [ -n "${browsers_path}" ]; then
  export PLAYWRIGHT_BROWSERS_PATH="${browsers_path}"
  # Seed from a previous local download rather than re-fetching it.
  if [ -z "$(ls -A "${browsers_path}" 2>/dev/null)" ] && [ -d "${HOME}/.cache/ms-playwright" ]; then
    log "moving the existing browser cache onto the volume"
    cp -a "${HOME}/.cache/ms-playwright/." "${browsers_path}/" 2>/dev/null
  fi
  log "browsers on the volume: ${browsers_path}"
else
  log "no writable /mnt/work; browsers go to the local disk and are lost on rebuild"
fi

# Do NOT probe with `npx --yes playwright --version`. npx downloads a throwaway
# copy into its own cache and reports a version, so that check passes on a box
# where playwright was never installed. Measured on a live slim box: the guard
# succeeded, npm install -g never ran, chromium landed in ~/.cache/ms-playwright,
# and `require('playwright')` then failed with MODULE_NOT_FOUND while the script
# had already printed "ready". Ask npm what is actually installed instead.
if ! npm ls -g --depth=0 playwright >/dev/null 2>&1; then
  log "installing playwright (npm -g)"
  npm install -g playwright >/tmp/enable-playwright-npm.log 2>&1 \
    || { log "npm install failed (see /tmp/enable-playwright-npm.log)"; exit 1; }
fi

# Use the installed CLI rather than npx, for the same reason: npx would fetch a
# second copy whose version need not match the one scripts import.
log "installing chromium + system deps (this pulls ~a few hundred MB)"
if ! playwright install --with-deps chromium >/tmp/enable-playwright.log 2>&1; then
  log "playwright install failed (see /tmp/enable-playwright.log)"; exit 1
fi

# Node does not resolve globally installed modules by default, so a scratch
# script still could not `require('playwright')` after a clean global install.
# Set both this and the browser path for login shells, so a plain `node app.js`
# on the box finds the library AND the browsers without any per-script setup.
node_root="$(npm root -g)"
{
  echo "# megh: written by 'megh enable playwright'."
  echo "# Lets ad-hoc scripts require() globally installed modules."
  echo "export NODE_PATH=\"${node_root}\${NODE_PATH:+:\$NODE_PATH}\""
  [ -n "${browsers_path}" ] && echo "export PLAYWRIGHT_BROWSERS_PATH=\"${browsers_path}\""
} > /etc/profile.d/megh-playwright.sh
chmod 0644 /etc/profile.d/megh-playwright.sh

# Verify the thing we claim to have installed, rather than trusting exit codes.
if ! NODE_PATH="${node_root}" node -e "require('playwright')" >/dev/null 2>&1; then
  log "playwright installed but not importable from node (NODE_PATH=${node_root})"
  exit 1
fi

log "playwright + chromium ready ($(playwright --version 2>/dev/null || echo unknown))"
log "scripts: require('playwright') works in a login shell; open a new one, or"
log "         run with NODE_PATH=${node_root}"
log "viewer: 'pw-ui trace <file>' / 'pw-ui ui' serves on 127.0.0.1:9323; watch it"
log "        with 'megh browse <box> 9323'. No display needed."
log "headed: a LIVE browser still needs 'megh enable vnc', then DISPLAY=:99"
