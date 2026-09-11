#!/usr/bin/env bash
# Feature: eda — the Linux-native EDA/CAD desktop apps (KiCad, lepton-eda,
# gerbv, xschem, ngspice, gtkwave, pcb-rnd, ddd) plus the software-GL, X and
# font packages a GUI app needs on a box with no GPU.
#
# It does NOT start a display: `megh enable vnc` owns that (Xvfb :99 + fluxbox +
# noVNC on :6080) and this owns what draws on it, so either can be re-run
# without disturbing the other. Order does not matter; nothing is visible until
# both have run. Idempotent.
#
# Knobs (MEGH_-prefixed, because `megh enable` forwards nothing else):
#   MEGH_EDA_PKGS       replace the app list entirely
#   MEGH_EDA_EXTRA      add to it (e.g. "librecad openscad")
#   MEGH_EDA_3D=1       also install kicad-packages3d: measured 424 MB to
#                       download and 5.7 GB on disk, against a container disk of
#                       20-40 GB that dies with the box. Off for that reason.
#   MEGH_EDA_KICAD_PPA  e.g. "9.0": take KiCad from upstream's PPA instead of
#                       Ubuntu 24.04's 7.0.11, which is a 2023 release.
set -uo pipefail
log() { echo "[megh-eda] $*"; }

ARCH="${ARCH_TAG:-$(uname -m)}"
APT_LOG=/tmp/enable-eda-apt.log

# The apps. kicad pulls its symbol/footprint libraries through Recommends, so
# this install must NOT use --no-install-recommends: a kicad with no libraries
# starts fine and then cannot place a single part.
APPS_DEFAULT="kicad kicad-libraries kicad-demos gerbv xschem lepton-eda pcb-rnd ngspice gtkwave ddd"

# What any X client needs here, separate from the apps because a replaced app
# list still wants it. There is no GPU and no DRI device, so GL resolves to
# mesa's llvmpipe software rasteriser, which is what libgl1-mesa-dri carries;
# without it KiCad's GL canvas has nothing to fall back to. glxinfo (mesa-utils)
# and xdpyinfo (x11-utils) are here to make "is the display actually usable"
# answerable on the box rather than by guessing from the Mac.
SUPPORT="libgl1-mesa-dri libglu1-mesa mesa-utils x11-utils fonts-liberation dbus-x11"

apps="${MEGH_EDA_PKGS:-${APPS_DEFAULT}}"
[ -n "${MEGH_EDA_EXTRA:-}" ] && apps="${apps} ${MEGH_EDA_EXTRA}"
[ "${MEGH_EDA_3D:-0}" = "1" ] && apps="${apps} kicad-packages3d"

# Ubuntu 24.04 dropped both names people still reach for, and each has a living
# successor under a different one. Translate instead of letting apt report "no
# installation candidate" for a tool that is right there.
wanted=""
for p in ${apps}; do
  case "${p}" in
    geda|geda-gaf|geda-gschem)
      log "geda left the archive before Ubuntu 24.04; installing its successor lepton-eda"
      p=lepton-eda ;;
    pcb|pcb-gtk)
      log "the gEDA pcb package left the archive before Ubuntu 24.04; installing pcb-rnd"
      p=pcb-rnd ;;
  esac
  case " ${wanted} " in *" ${p} "*) continue ;; esac
  wanted="${wanted} ${p}"
done
wanted="${wanted# }"

# ---------------------------------------------------------------------------
# 1. Keep the debs on the volume.
# ---------------------------------------------------------------------------
# The default set is ~200 MB of debs, and everything apt unpacks lands on the
# container disk, which is thrown away with the box. The packages have to be
# reinstalled on every new box either way; caching the archives on the volume at
# least makes that a local unpack instead of a re-download.
apt_opts=()
cache="/mnt/work/cache/apt/${ARCH}"
if [ -d /mnt/work ] && [ -w /mnt/work ] && mkdir -p "${cache}/partial" 2>/dev/null; then
  apt_opts+=(-o "Dir::Cache::archives=${cache}")
  # apt downloads as the unprivileged _apt user, which cannot write here: the
  # volume is NFS with root squashed, so the directory root just created is not
  # handable to uid 42 (chown is denied for every uid). apt detects that and
  # retries as root with a warning; ask for it up front so the warning does not
  # read like a fault. See the NFS note in CLAUDE.md.
  apt_opts+=(-o "APT::Sandbox::User=root")
  log "deb cache on the volume: ${cache} (next box reinstalls without re-downloading)"
else
  log "no writable /mnt/work; debs stay on the container disk and are re-fetched per box"
fi

free_gb="$(df -P -BG / 2>/dev/null | awk 'NR==2 {gsub(/G/,"",$4); print $4}')"
if [ -n "${free_gb}" ] && [ "${free_gb}" -lt 4 ] 2>/dev/null; then
  log "only ${free_gb}G free on / — the container disk is small and apt may refuse"
fi

# ---------------------------------------------------------------------------
# 2. Install.
# ---------------------------------------------------------------------------
export DEBIAN_FRONTEND=noninteractive
log "refreshing apt"
apt-get update -qq >"${APT_LOG}" 2>&1 || log "apt-get update reported errors (continuing; ${APT_LOG})"

if [ -n "${MEGH_EDA_KICAD_PPA:-}" ]; then
  ppa="ppa:kicad/kicad-${MEGH_EDA_KICAD_PPA}-releases"
  log "adding ${ppa} (the archive's KiCad is 7.0.11, a 2023 release)"
  if apt-get install -y --no-install-recommends software-properties-common >>"${APT_LOG}" 2>&1 \
     && add-apt-repository -y "${ppa}" >>"${APT_LOG}" 2>&1 \
     && apt-get update -qq >>"${APT_LOG}" 2>&1; then
    log "kicad comes from ${ppa}"
  else
    log "could not add ${ppa}; using the archive version instead (${APT_LOG})"
  fi
fi

log "installing X support: ${SUPPORT}"
apt-get install -y --no-install-recommends "${apt_opts[@]}" ${SUPPORT} >>"${APT_LOG}" 2>&1 \
  || log "some X support packages failed (${APT_LOG})"

log "installing: ${wanted}"
if ! apt-get install -y "${apt_opts[@]}" ${wanted} >>"${APT_LOG}" 2>&1; then
  # apt is all-or-nothing: one name that is no longer in the archive, or a typo
  # in MEGH_EDA_EXTRA, aborts the transaction and leaves you with none of the
  # rest. Retry individually so the working ones land, and say which did not.
  log "the group install failed; retrying one package at a time"
  failed=""
  for p in ${wanted}; do
    apt-get install -y "${apt_opts[@]}" "${p}" >>"${APT_LOG}" 2>&1 || failed="${failed} ${p}"
  done
  [ -n "${failed}" ] && log "not installed:${failed} (${APT_LOG})"
fi

# ---------------------------------------------------------------------------
# 3. Keep settings and user libraries across rebuilds.
# ---------------------------------------------------------------------------
# The same primitive as megh.yaml `persist:`, and deliberately the same volume
# slot names (path with / -> -, leading dot dropped), so adding these to
# persist: later links to the dir that is already there instead of starting a
# second, divergent copy.
persist() {
  local rel="$1" home slot
  home="${HOME}/${rel}"
  slot="$(printf '%s' "${rel}" | sed 's#/#-#g; s#^\.##')"
  [ -L "${home}" ] && return 0
  [ -d /mnt/work ] && [ -w /mnt/work ] || return 0
  mkdir -p "/mnt/work/state/${slot}" 2>/dev/null || return 0
  if [ -e "${home}" ]; then
    cp -a "${home}/." "/mnt/work/state/${slot}/" 2>/dev/null
    rm -rf "${home}"
  fi
  mkdir -p "$(dirname "${home}")"
  ln -sfn "/mnt/work/state/${slot}" "${home}"
  log "persisting ~/${rel} -> /mnt/work/state/${slot}"
}
for d in .config/kicad .local/share/kicad .config/lepton-eda .config/gerbv; do
  persist "${d}"
done

# ---------------------------------------------------------------------------
# 4. A fluxbox menu, built from what actually installed.
# ---------------------------------------------------------------------------
# Typing DISPLAY=:99 kicad & over ssh is fine from a laptop and miserable from a
# phone, which is the case noVNC exists for. Generate the entries from what the
# packages themselves declare, so anything added through MEGH_EDA_EXTRA gets a
# launcher too without a hand-kept list here.

# A metapackage owns no files: pcb-rnd's binary is in pcb-rnd-core, and asking
# dpkg what pcb-rnd installed answers "nothing". Fall back to its installed
# direct dependencies, and only as a fallback, so kicad (which ships its own
# .desktop files and pulls in python3) is never described by a dependency.
deps_of() {
  apt-cache depends --installed --no-recommends --no-suggests --no-conflicts \
    --no-breaks --no-replaces --no-enhances "$1" 2>/dev/null |
    awk '/Depends:/ { gsub(/[<>]/, "", $2); print $2 }'
}
owned() { dpkg -L "$1" 2>/dev/null | grep -E "$2"; }
family_files() {
  local p="$1" pat="$2" out
  out="$(owned "${p}" "${pat}")"
  if [ -z "${out}" ]; then
    for dep in $(deps_of "${p}"); do
      out="${out}${out:+
}$(owned "${dep}" "${pat}")"
    done
  fi
  printf '%s\n' "${out}" | grep -v '^$'
}
DESKTOP_RE='^/usr/share/applications/.*\.desktop$'
BIN_RE='^/usr/(bin|games)/[^/]+$'

# Which of a family's binaries IS the app. pcb-rnd-core ships four, and the
# alphabetically first is fp2preview, a footprint-to-image converter nobody
# asked for. Prefer the one named after the package, then one sharing its
# prefix, and only then give up and take the first.
pick_bin() {
  local p="$1" list b
  list="$(family_files "${p}" "${BIN_RE}")"
  [ -z "${list}" ] && return 1
  b="$(printf '%s\n' "${list}" | awk -F/ -v p="${p}" '$NF == p { print; exit }')"
  [ -z "${b}" ] && b="$(printf '%s\n' "${list}" | awk -F/ -v pre="${p%%-*}-" 'index($NF, pre) == 1 { print; exit }')"
  [ -z "${b}" ] && b="$(printf '%s\n' "${list}" | head -1)"
  printf '%s\n' "${b}"
}

entries="$(mktemp)"
for p in ${wanted}; do
  d="$(family_files "${p}" "${DESKTOP_RE}")"
  if [ -n "${d}" ]; then
    printf '%s\n' "${d}" | while read -r f; do
      # Only the [Desktop Entry] group: an Action group carries its own Name and
      # Exec and would otherwise win by appearing first. Field codes (%U, %F) are
      # for a file manager passing arguments and are not valid shell.
      awk '
        /^\[/       { entry = ($0 == "[Desktop Entry]"); next }
        !entry      { next }
        /^Name=/    { if (n == "") n = substr($0, 6) }
        /^Exec=/    { if (x == "") x = substr($0, 6) }
        /^NoDisplay=true/ { hide = 1 }
        /^Terminal=true/  { term = 1 }
        END {
          if (hide || n == "" || x == "") exit
          gsub(/ *%[a-zA-Z]/, "", x)
          gsub(/[()]/, "", n)
          if (term) x = "xterm -e " x
          printf "[exec] (%s) {%s}\n", n, x
        }
      ' "${f}" >> "${entries}"
    done
    continue
  fi
  # No .desktop anywhere in the family (pcb-rnd ships none), so fall back to its
  # binary. Run it under xterm: that way a GUI app still opens its window
  # and its stderr is visible, and a CLI-only package (ngspice) gives a usable
  # terminal instead of a menu entry that silently does nothing.
  b="$(pick_bin "${p}")"
  [ -n "${b}" ] && printf '[exec] (%s) {xterm -e %s}\n' "${p}" "${b}" >> "${entries}"
done

fb="${HOME}/.fluxbox"
frag="${fb}/megh-eda"
mkdir -p "${fb}"
{
  echo "# Generated by 'megh enable eda'. Rewritten on every run; edits are lost."
  echo "[submenu] (EDA)"
  sort -u "${entries}"
  echo "[end]"
} > "${frag}"
rm -f "${entries}"

menu="${fb}/menu"
if [ ! -f "${menu}" ]; then
  # With no user menu fluxbox falls back to /etc/X11/fluxbox/fluxbox-menu, which
  # cannot be included from inside ours (it is a complete [begin]...[end] of its
  # own). Write a small menu that keeps the entries anyone actually uses.
  {
    echo '[begin] (megh)'
    echo "[include] (${frag})"
    echo '[exec] (Terminal) {xterm -fa "DejaVu Sans Mono" -fs 11}'
    echo '[submenu] (System)'
    echo '[workspaces] (Workspaces)'
    echo '[config] (Configure)'
    echo '[restart] (Restart)'
    echo '[end]'
    echo '[exit] (Exit)'
    echo '[end]'
  } > "${menu}"
  log "wrote a fluxbox menu with an EDA submenu"
elif ! grep -q 'megh-eda' "${menu}"; then
  last="$(grep -n '^\[end\]' "${menu}" | tail -1 | cut -d: -f1)"
  if [ -n "${last}" ]; then
    awk -v n="${last}" -v inc="[include] (${frag})" 'NR==n { print inc } { print }' \
      "${menu}" > "${menu}.megh" && mv "${menu}.megh" "${menu}"
    log "added the EDA submenu to your existing fluxbox menu"
  fi
fi

# ---------------------------------------------------------------------------
# 5. Verify what is really there, and report the display.
# ---------------------------------------------------------------------------
# apt's exit code says the transaction committed, not that a command exists:
# the lesson from playwright, where a passing install left nothing importable.
# Ask dpkg what landed in a bin directory and resolve it through PATH.
ok=0
for p in ${wanted}; do
  if ! dpkg -s "${p}" 2>/dev/null | grep -q '^Status: install ok installed'; then
    printf '  %-18s NOT INSTALLED\n' "${p}"
    continue
  fi
  ok=$((ok + 1))
  bin="$(pick_bin "${p}")"
  if [ -n "${bin}" ]; then
    printf '  %-18s %s\n' "${p}" "$(command -v "$(basename "${bin}")" || echo "${bin}")"
  else
    printf '  %-18s installed (data only)\n' "${p}"
  fi
done
[ "${ok}" -eq 0 ] && { log "nothing installed (${APT_LOG})"; exit 1; }

if DISPLAY=:99 xdpyinfo >/dev/null 2>&1; then
  geom="$(DISPLAY=:99 xdpyinfo 2>/dev/null | awk '/dimensions:/ { print $2; exit }')"
  log "display :99 is up (${geom})"
  gl="$(DISPLAY=:99 glxinfo -B 2>/dev/null | sed -n 's/^OpenGL renderer string: //p')"
  [ -n "${gl}" ] && log "GL renderer: ${gl}"
  log "watch it: 'megh browse 6080' -> http://localhost:6080/vnc.html (or http://<box>:6080/vnc.html)"
else
  log "no display on :99 — run 'megh enable vnc' on this box first, then reconnect"
fi

log "launch: DISPLAY=:99 kicad &   or right-click the noVNC desktop -> EDA"
log "        fluxbox reads a new menu on Restart (right-click -> System -> Restart)"
log "no GPU here, so pcbnew's canvas and the 3D viewer run on llvmpipe: usable, not fast."
log "        if the board view crawls, Preferences -> Graphics -> Fallback."
