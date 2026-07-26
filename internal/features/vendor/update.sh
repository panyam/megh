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
  if ! command -v npm >/dev/null 2>&1; then
    echo "npm not found; skipped the upstream-version + readiness check"
    exit 0
  fi

  xt_latest="$(npm view @xterm/xterm version 2>/dev/null || echo '?')"
  fit_latest="$(npm view @xterm/addon-fit version 2>/dev/null || echo '?')"
  echo "upstream versions (pinned -> latest stable):"
  printf "  @xterm/xterm      %-8s -> %s\n" "${XTERM_VERSION}" "${xt_latest}"
  printf "  @xterm/addon-fit  %-8s -> %s\n" "${XTERM_ADDON_FIT_VERSION}" "${fit_latest}"

  # Readiness. The real gate for taking a new xterm MAJOR is a STABLE
  # @xterm/addon-fit whose peer @xterm/xterm includes that major (today only a
  # prerelease fit pairs with xterm 6). The published tarball omits
  # peerDependencies and `npm view` returns it inconsistently, so read it
  # defensively and fall back to "verify manually" rather than guessing.
  pin_major="${XTERM_VERSION%%.*}"
  lat_major="${xt_latest%%.*}"
  echo "bump readiness:"
  if [ "${xt_latest}" = "?" ]; then
    echo "  could not reach npm; re-run when online."
  elif [ "${xt_latest}" = "${XTERM_VERSION}" ]; then
    echo "  on the latest xterm (${XTERM_VERSION}); nothing to do."
  elif [ "${lat_major}" = "${pin_major}" ]; then
    echo "  minor/patch available (${XTERM_VERSION} -> ${xt_latest}); low-risk — bump versions.env + ./update.sh."
  else
    peer="$(npm view "@xterm/addon-fit@${fit_latest}" peerDependencies.@xterm/xterm 2>/dev/null || true)"
    [ -n "${peer}" ] || peer="$(npm view "@xterm/addon-fit@${fit_latest}" peerDependencies --json 2>/dev/null \
        | grep -oE '"@xterm/xterm"[[:space:]]*:[[:space:]]*"[^"]*"' | grep -oE '"[^"]*"$' | tr -d '"' || true)"
    case " ${peer} " in
      *"^${lat_major}."*|*">=${lat_major}"*|*"${lat_major}.x"*)
        echo "  READY: stable @xterm/addon-fit ${fit_latest} supports xterm ${lat_major}.x (peer ${peer})."
        echo "         next: bump versions.env, ./update.sh, then verify the page in a mobile browser (fit + /ws)." ;;
      "  ")
        echo "  xterm ${lat_major}.x is out, but the fit addon's peer range could not be read from npm here."
        echo "         confirm @xterm/addon-fit's stable peer includes ^${lat_major} (npmjs.com) before bumping." ;;
      *)
        echo "  NOT READY: stable @xterm/addon-fit ${fit_latest} still targets xterm ${peer}, not ${lat_major}.x —"
        echo "         only a prerelease fit addon pairs with xterm ${lat_major}. Hold until a stable one ships." ;;
    esac
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
