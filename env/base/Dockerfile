# megh base dev environment.
#
# This is the "env layer": tools and libraries live here, not on the host and
# not baked into a hand-mutated VM snapshot. A tooling change is a commit to
# this file, rebuilt by CI and rolled to every provider. The box that runs it
# is disposable; this image plus a git remote plus a scratch volume is the
# whole dev environment.
#
# RunPod CPU pods are x86_64, so this image targets linux/amd64. When a second
# provider needs a different arch, add a build matrix entry rather than editing
# assumptions in here.

FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    DISPLAY=:99

# Base OS tooling. Kept deliberately broad so the first "get a feel" box has
# everything an agent loop needs without a second install pass.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl wget gnupg git git-lfs openssh-server \
      tmux ripgrep fd-find fzf jq unzip zip build-essential pkg-config \
      python3 python3-pip python3-venv \
      xvfb x11vnc fluxbox novnc websockify \
      sudo locales tzdata less nano vim htop procps net-tools iproute2 \
    && rm -rf /var/lib/apt/lists/*

# Node 22 LTS (drives Claude Code, Codex CLI, and Playwright).
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

# Go (amd64). Bump GO_VERSION here; this is the config-as-code seam for Go.
ARG GO_VERSION=1.22.5
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" \
      -o /tmp/go.tgz \
    && tar -C /usr/local -xzf /tmp/go.tgz \
    && rm /tmp/go.tgz
ENV PATH="/usr/local/go/bin:/root/go/bin:${PATH}"

# ttyd static binary (web terminal). Serves the tmux session over HTTP.
ARG TTYD_VERSION=1.7.7
RUN curl -fsSL "https://github.com/tsl0922/ttyd/releases/download/${TTYD_VERSION}/ttyd.x86_64" \
      -o /usr/local/bin/ttyd \
    && chmod +x /usr/local/bin/ttyd

# Coding agents.
RUN npm install -g @anthropic-ai/claude-code @openai/codex

# Playwright + headed Chromium and its system deps, so noVNC has something to
# show on day one. This is the heavy layer; drop chromium here if you want a
# slimmer image.
RUN npm install -g playwright \
    && npx --yes playwright install --with-deps chromium

EXPOSE 22 7681 6080

COPY entrypoint.sh /usr/local/bin/megh-entrypoint
RUN chmod +x /usr/local/bin/megh-entrypoint

ENTRYPOINT ["/usr/local/bin/megh-entrypoint"]
