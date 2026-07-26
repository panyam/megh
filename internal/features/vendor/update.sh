#!/usr/bin/env bash
# Refresh (or verify) the vendored webterm web assets from their pinned versions.
#
#   ./update.sh          re-fetch the pinned versions, copy the files, rewrite SHA256SUMS
#   ./update.sh --check  verify on-disk assets match SHA256SUMS; report upstream versions
#
# The assets (xterm.js/css + the fit addon) are vendored, not fetched at runtime,
# so the webterm page is fully offline and reproducible. Versions are pinned in
# versions.env; bumping = edit that file, run ./update.sh, and commit the result.
# A refresh (and the upstream part of --check) needs npm (registry.npmjs.org).
set -euo pipefail
cd "$(dirname "$0")"
# shellcheck source=/dev/null
source ./versions.env

# Parallel arrays (bash 3.2-safe): destination file <- pkg@version, tarball path.
DSTS=("xterm.js" "xterm.css" "addon-fit.js")
PKGS=("@xterm/xterm@${XTERM_VERSION}" "@xterm/xterm@${XTERM_VERSION}" "@xterm/addon-fit@${XTERM_ADDON_FIT_VERSION}")
GLOB=("*/package/lib/xterm.js" "*/package/css/xterm.css" "*/package/lib/addon-fit.js")

if [ "${1:-}" = "--check" ]; then
  echo "verifying vendored assets against SHA256SUMS..."
  sha256sum -c SHA256SUMS
  if command -v npm >/dev/null 2>&1; then
    echo "upstream versions (pinned -> latest):"
    printf "  @xterm/xterm      %-8s -> %s\n" "${XTERM_VERSION}" "$(npm view @xterm/xterm version 2>/dev/null || echo '?')"
    printf "  @xterm/addon-fit  %-8s -> %s\n" "${XTERM_ADDON_FIT_VERSION}" "$(npm view @xterm/addon-fit version 2>/dev/null || echo '?')"
    echo "(to update: bump versions.env, read the changelog, run ./update.sh)"
  else
    echo "npm not found; skipped the upstream-version check"
  fi
  exit 0
fi

command -v npm >/dev/null 2>&1 || { echo "update.sh: npm is required to refresh assets" >&2; exit 1; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
echo "fetching pinned versions via npm pack ..."
# Fetch unique specs, then extract each tarball into its own dir so nothing
# clobbers anything, and locate files by their in-package path.
( cd "$tmp" && npm pack "@xterm/xterm@${XTERM_VERSION}" "@xterm/addon-fit@${XTERM_ADDON_FIT_VERSION}" >/dev/null )
for t in "$tmp"/*.tgz; do
  d="${t%.tgz}"; mkdir -p "$d"; tar xzf "$t" -C "$d"
done

for i in "${!DSTS[@]}"; do
  src="$(find "$tmp" -path "${GLOB[$i]}" -print -quit)"
  [ -n "$src" ] && [ -f "$src" ] || { echo "update.sh: could not find ${GLOB[$i]} for ${PKGS[$i]}" >&2; exit 1; }
  cp "$src" "${DSTS[$i]}"
  echo "  vendored ${DSTS[$i]}  (from ${PKGS[$i]})"
done

sha256sum "${DSTS[@]}" > SHA256SUMS
echo "wrote SHA256SUMS:"
sed 's/^/  /' SHA256SUMS
echo "done — review the diff, then commit versions.env + the assets + SHA256SUMS."
