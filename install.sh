#!/usr/bin/env sh
# megh installer. One command on any machine that will be a control device:
#
#   curl -fsSL https://raw.githubusercontent.com/panyam/megh/main/install.sh | sh
#
# The repo and its releases are public, so the script and the binary need no
# auth. The CONFIG is a separate matter: a real megh.yaml names every repo you
# work on, so it lives in a private repo and is fetched with gh when available.
# Without gh you still get a working install and the template to edit.
#
# Idempotent. Re-run it to upgrade; it never overwrites an existing megh.yaml.
# MEGH_CONFIG_REPO / MEGH_CONFIG_PATH point the config fetch somewhere else.
set -eu

REPO="${MEGH_REPO:-panyam/megh}"
RELEASE="${MEGH_RELEASE:-latest}"

say()  { printf '  %s\n' "$*"; }
die()  { printf 'megh install: %s\n' "$*" >&2; exit 1; }

# --- 1. Which artifact does this machine need? -------------------------------
# Termux is the case worth getting right. Its arch is arm64, but a linux/arm64
# Go binary is ET_EXEC and Android's loader takes only ET_DYN, so it is rejected
# with "unexpected e_type: 2". The android build is PIE with an interpreter of
# /system/bin/linker64, which is what Android actually provides.
# MEGH_TARGET forces the artifact, for a machine this script guesses wrong about
# and for testing the Termux path from somewhere that is not Termux.
os="$(uname -s)"
arch="$(uname -m)"
case "$arch" in
  arm64|aarch64) arch=arm64 ;;
  x86_64|amd64)  arch=amd64 ;;
  *) die "unsupported architecture: $arch" ;;
esac
if [ -n "${MEGH_TARGET:-}" ]; then
  target="$MEGH_TARGET"
else
  case "$os" in
    Darwin) target="darwin-$arch" ;;
    Linux)
      if [ -n "${TERMUX_VERSION:-}" ] || [ "${PREFIX:-}" != "${PREFIX#*com.termux}" ] || [ "$(uname -o 2>/dev/null || true)" = "Android" ]; then
        target="android-$arch"
      else
        target="linux-$arch"
      fi
      ;;
    *) die "unsupported OS: $os" ;;
  esac
fi

# --- 2. Where does it go? ----------------------------------------------------
if [ -n "${MEGH_INSTALL_DIR:-}" ]; then bindir="$MEGH_INSTALL_DIR"
elif [ -n "${PREFIX:-}" ] && [ -d "${PREFIX}/bin" ]; then bindir="${PREFIX}/bin"   # Termux
elif [ -d "$HOME/.local/bin" ]; then bindir="$HOME/.local/bin"
elif [ -d "$HOME/bin" ]; then bindir="$HOME/bin"
else bindir="$HOME/.local/bin"
fi
mkdir -p "$bindir"

say "target:  $target"
say "install: $bindir/megh"

# --- 3. Prerequisites --------------------------------------------------------
command -v curl >/dev/null 2>&1 || die "needs curl"
command -v tar  >/dev/null 2>&1 || die "needs tar"
# gh is optional now that the release is public. It is only used to fetch the
# private config, and its absence costs you that, not the install.
have_gh=no
if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then have_gh=yes; fi
# megh shells out to these for every box operation; better to say so now than to
# fail later inside a command.
for c in ssh ssh-agent ssh-add; do
  command -v "$c" >/dev/null 2>&1 || say "WARNING: $c not found; megh needs it (Termux: pkg install openssh)"
done

# --- 4. Fetch ----------------------------------------------------------------
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM
say "downloading $RELEASE ..."
base="https://github.com/$REPO/releases/download/$RELEASE"
for f in "megh-$target" "megh.yaml.example" "SHA256SUMS"; do
  # Only the binary is required; the other two are conveniences.
  curl -fsSL -o "$tmp/$f" "$base/$f" 2>/dev/null || true
done
[ -f "$tmp/megh-$target" ] || die "no asset megh-$target in the $RELEASE release"

# --- 5. Verify ---------------------------------------------------------------
# A tampered or truncated download should not become an executable on PATH.
if [ -f "$tmp/SHA256SUMS" ]; then
  if command -v sha256sum >/dev/null 2>&1; then sum="sha256sum"
  elif command -v shasum >/dev/null 2>&1; then sum="shasum -a 256"
  else sum=""; fi
  if [ -n "$sum" ]; then
    want="$(awk -v f="megh-$target" '$2 == f || $2 == "*"f {print $1}' "$tmp/SHA256SUMS")"
    got="$(cd "$tmp" && $sum "megh-$target" | awk '{print $1}')"
    [ -n "$want" ] && [ "$want" != "$got" ] && die "checksum mismatch for megh-$target"
    [ -n "$want" ] && say "checksum ok"
  fi
fi

# --- 6. Install --------------------------------------------------------------
chmod +x "$tmp/megh-$target"
mv "$tmp/megh-$target" "$bindir/megh"

# Two sources on purpose. The binary comes from this repo's release; the real
# megh.yaml comes from a private config repo, because that file names every repo
# you work on and does not belong in a public one. If it is unreachable, the
# template is installed instead, which is enough to edit into shape.
cfg="$HOME/.config/megh/megh.yaml"
CONFIG_REPO="${MEGH_CONFIG_REPO:-panyam/dotfiles}"
CONFIG_PATH="${MEGH_CONFIG_PATH:-megh/megh.yaml}"
if [ -f "$cfg" ]; then
  say "config:  $cfg already exists, left alone"
else
  mkdir -p "$(dirname "$cfg")"
  if [ "$have_gh" = yes ] && gh api "repos/$CONFIG_REPO/contents/$CONFIG_PATH" \
       -H "Accept: application/vnd.github.raw" > "$tmp/cfg" 2>/dev/null && [ -s "$tmp/cfg" ]; then
    cp "$tmp/cfg" "$cfg"
    say "config:  $cfg (from $CONFIG_REPO)"
  elif [ -f "$tmp/megh.yaml.example" ]; then
    cp "$tmp/megh.yaml.example" "$cfg"
    say "config:  $cfg (TEMPLATE; $CONFIG_REPO/$CONFIG_PATH not reachable)"
    [ "$have_gh" = no ] && say "         gh is not logged in, so the private config was skipped"
    say "         edit it, or drop your own over the top"
  fi
fi

# --- 7. What now -------------------------------------------------------------
printf '\n'
say "installed: $bindir/megh"
case ":$PATH:" in
  *":$bindir:"*) ;;
  *) say "NOTE: $bindir is not on your PATH. Add it:"; say "  export PATH=\"$bindir:\$PATH\"" ;;
esac
printf '\n'
say "Next, once per device:"
say "  gh auth refresh -h github.com -s admin:public_key"
say "  megh profile create \$(hostname -s 2>/dev/null || echo device)"
say "  megh profile gh add personal --profile <name> --register"
say "  megh profile use <name>"
say "Then fill ~/.megh/profiles/<name>/secrets.env and run: megh config"
say "Full walkthrough: SETUP.md section 6"
