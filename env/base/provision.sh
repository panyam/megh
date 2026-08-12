#!/usr/bin/env bash
# Single source of truth for the megh base dev-environment tooling.
#
# This script is the ONE place tools are declared. It is invoked as root at
# BUILD time by both artifact pipelines so they can never drift:
#   - env/base/Dockerfile          -> container image (container-native providers, e.g. RunPod)
#   - env/base/packer/*.pkr.hcl    -> VM image        (VM-native providers, e.g. Hetzner)  [later]
#
# It installs tools only. It never starts services. Runtime init is the
# artifact's job: the container uses entrypoint.sh as PID 1; the VM uses
# systemd units + cloud-init.
#
# Knobs (env vars):
#   TARGET_ARCH     amd64 | arm64          (default amd64)
#   GO_VERSION      go toolchain version   (default 1.22.5)
#   TTYD_VERSION    ttyd release           (default 1.7.7)
#   INSTALL_DOCKER  1 to install the Docker engine (VM: yes; container: no)
#   MEGH_SLIM       1 for the slim flavor: skip the frontend stack (Playwright +
#                   headed-browser display + code-server). The entrypoint
#                   background-installs code-server to the box's local disk;
#                   Playwright/headed browser is on-demand (use the base flavor
#                   for frontend work). Most work is not frontend, so slim is the
#                   fast-booting default for backend/CLI dev.
set -euo pipefail

TARGET_ARCH="${TARGET_ARCH:-amd64}"
GO_VERSION="${GO_VERSION:-1.26.4}"
TTYD_VERSION="${TTYD_VERSION:-1.7.7}"
INSTALL_DOCKER="${INSTALL_DOCKER:-0}"
MEGH_SLIM="${MEGH_SLIM:-0}"

export DEBIAN_FRONTEND=noninteractive

# --- base OS tooling -------------------------------------------------------
apt-get update
apt-get install -y --no-install-recommends \
  ca-certificates curl wget gnupg git git-lfs openssh-server rsync \
  tmux ripgrep fd-find fzf jq unzip zip build-essential pkg-config \
  python3 python3-pip python3-venv zsh \
  sudo locales tzdata less nano vim htop procps net-tools iproute2

# --- GitHub CLI ------------------------------------------------------------
# Not optional: the PR/issue workflow (start_pr, address_pr_feedback,
# reviewer_guide, ghissue) is built on `gh`, so a box without it cannot do the
# thing the box exists for. From the official repo rather than Ubuntu's, which
# lags. Auth persists across rebuilds via megh.yaml `persist: ~/.config/gh`.
curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
  -o /usr/share/keyrings/githubcli-archive-keyring.gpg
chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg
echo "deb [arch=${TARGET_ARCH} signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
  > /etc/apt/sources.list.d/github-cli.list
apt-get update
apt-get install -y --no-install-recommends gh

# Make zsh root's login shell so a box's shell matches a zsh setup (ttyd/tmux and
# `megh ssh` both use the login shell). Your zsh config arrives via megh.yaml
# `files:` (~/.zshenv, ~/.zshrc); with none, zsh just starts bare. Harmless if you
# stay on bash.
chsh -s "$(command -v zsh)" root || true

# --- headed-browser display stack (full flavor only) -----------------------
# Only useful with Playwright's headed browser; slim skips it.
if [ "${MEGH_SLIM}" != "1" ]; then
  apt-get install -y --no-install-recommends xvfb x11vnc fluxbox novnc websockify
fi

# --- Docker engine (VM artifact only) --------------------------------------
# On a VM the dev environment runs directly on the box, so this is the real,
# native daemon that dev workflows (docker build, testcontainers) use. The
# container artifact skips this (INSTALL_DOCKER=0): it has no daemon and does
# not need one.
if [ "${INSTALL_DOCKER}" = "1" ]; then
  apt-get install -y --no-install-recommends docker.io docker-compose-v2
  systemctl enable docker 2>/dev/null || true
fi

# --- Node 22 LTS (Claude Code, Codex CLI, Playwright) ----------------------
curl -fsSL https://deb.nodesource.com/setup_22.x | bash -
apt-get install -y --no-install-recommends nodejs
rm -rf /var/lib/apt/lists/*

# --- Go --------------------------------------------------------------------
curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGET_ARCH}.tar.gz" -o /tmp/go.tgz
rm -rf /usr/local/go
tar -C /usr/local -xzf /tmp/go.tgz
rm /tmp/go.tgz

# --- ttyd (arch-specific static binary) ------------------------------------
case "${TARGET_ARCH}" in
  amd64) ttyd_asset="ttyd.x86_64" ;;
  arm64) ttyd_asset="ttyd.aarch64" ;;
  *) echo "provision: unsupported TARGET_ARCH=${TARGET_ARCH}" >&2; exit 1 ;;
esac
curl -fsSL "https://github.com/tsl0922/ttyd/releases/download/${TTYD_VERSION}/${ttyd_asset}" \
  -o /usr/local/bin/ttyd
chmod +x /usr/local/bin/ttyd

# --- Tailscale (private access to the box) ---------------------------------
# On container providers (RunPod) there is no TUN device, so tailscaled runs in
# userspace-networking mode and `tailscale serve` bridges the tailnet to the
# box's localhost web surfaces. The entrypoint brings this up when TS_AUTHKEY is
# set. On VM providers a normal tailscaled/systemd unit is used instead.
curl -fsSL https://tailscale.com/install.sh | sh

# --- code-server (full flavor only; slim background-installs it at boot) -----
# Bound to localhost by the entrypoint and reached over Tailscale (or a tunnel),
# same as ttyd/noVNC. VS Code Remote-SSH also works over the box's SSH for free.
if [ "${MEGH_SLIM}" != "1" ]; then
  curl -fsSL https://code-server.dev/install.sh | sh
fi

# --- PATH for login shells (VM) and a sane default -------------------------
cat > /etc/profile.d/megh-path.sh <<'EOF'
export PATH="/usr/local/go/bin:/root/go/bin:$PATH"
EOF

# --- coding agents + pnpm (both flavors) -----------------------------------
# pnpm is not interchangeable with npm here: repos carrying a pnpm-lock.yaml
# resolve differently under npm, so an npm-only box installs the wrong tree.
npm install -g @anthropic-ai/claude-code @openai/codex pnpm

# --- Playwright + headed browser (full flavor only) ------------------------
# Deferred on slim: most work is not frontend. Install on demand with
#   npm i -g playwright && npx playwright install --with-deps chromium
if [ "${MEGH_SLIM}" != "1" ]; then
  npm install -g playwright
  npx --yes playwright install --with-deps chromium
fi

echo "provision: dev environment ready (arch=${TARGET_ARCH}, docker=${INSTALL_DOCKER}, slim=${MEGH_SLIM})"
