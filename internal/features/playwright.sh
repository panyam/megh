#!/usr/bin/env bash
# Feature: playwright — install Playwright + Chromium (and system deps) on the
# box's local disk. For HEADED runs, enable the display first (`megh enable vnc`)
# and launch against DISPLAY=:99. Idempotent.
set -uo pipefail
log() { echo "[megh-enable] $*"; }

if ! npx --yes playwright --version >/dev/null 2>&1; then
  log "installing playwright (npm -g)"
  npm install -g playwright >/tmp/enable-playwright-npm.log 2>&1 \
    || { log "npm install failed (see /tmp/enable-playwright-npm.log)"; exit 1; }
fi

log "installing chromium + system deps (this pulls ~a few hundred MB)"
if npx --yes playwright install --with-deps chromium >/tmp/enable-playwright.log 2>&1; then
  log "playwright + chromium ready"
  log "headed: run 'megh enable vnc', then launch with DISPLAY=:99"
else
  log "playwright install failed (see /tmp/enable-playwright.log)"; exit 1
fi
