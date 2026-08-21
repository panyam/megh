#!/usr/bin/env bash
# Feature: playwright — install Playwright + Chromium (and system deps) on a box.
# The browsers go on the scratch volume so a rebuild does not re-download them.
# For HEADED runs, enable the display first (`megh enable vnc`) and launch
# against DISPLAY=:99. Idempotent.
set -uo pipefail
log() { echo "[megh-enable] $*"; }

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
log "headed: run 'megh enable vnc', then launch with DISPLAY=:99"
