#!/usr/bin/env bash
# megh dev-box entrypoint.
#
# Runs as PID 1 inside the dev container. Its whole job is to make a fresh,
# disposable box usable: accept an SSH key, bring up the web shell and the
# headed-browser display, wire the scratch volume into a stable layout, and
# hydrate persistent state from that volume. Nothing here is precious. Destroy
# the box and this replays.
set -euo pipefail

log() { echo "[megh] $*"; }

# ---------------------------------------------------------------------------
# 1. Scratch volume layout.
#
# WORK_MOUNT is where the provider mounts the network/block volume. RunPod
# network volumes default to /workspace. Everything the box "keeps" lives on
# the volume so a rebuild of the box loses nothing but a few minutes of state.
# ---------------------------------------------------------------------------
WORK_MOUNT="${WORK_MOUNT:-/workspace}"
ARCH_TAG="${ARCH_TAG:-x86_64}"

mkdir -p \
  "${WORK_MOUNT}/repos" \
  "${WORK_MOUNT}/worktrees" \
  "${WORK_MOUNT}/state" \
  "${WORK_MOUNT}/cache/${ARCH_TAG}"

# Stable path used everywhere else, independent of the provider's mount point.
ln -sfn "${WORK_MOUNT}" /mnt/work

# Persist tool state on the volume by pointing home dirs at it. Only link if the
# real dir is empty, so we never clobber existing config.
link_state() {
  local home_dir="$1" vol="$2" kind="$3"
  [ -L "${home_dir}" ] && return 0                 # already linked
  mkdir -p "$(dirname "${home_dir}")"
  if [ "${kind}" = file ]; then
    # A FILE (e.g. ~/.claude.json — claude's login/onboarding state, which lives
    # next to ~/.claude, so the dir symlink alone misses it).
    mkdir -p "$(dirname "${vol}")"
    # Heal a slot an earlier boot got wrong. A file entry that was once guessed
    # to be a directory leaves an empty dir on the VOLUME, and linking to it
    # reproduces "Is a directory" on every later box however the kind is decided
    # now. rmdir, not rm -rf: it refuses on a non-empty directory, so real data
    # is never at risk.
    [ -d "${vol}" ] && [ ! -L "${vol}" ] && rmdir "${vol}" 2>/dev/null || true
    if [ -f "${home_dir}" ]; then
      [ -e "${vol}" ] || cp -a "${home_dir}" "${vol}" 2>/dev/null || true
      rm -f "${home_dir}"
    fi
    ln -sfn "${vol}" "${home_dir}"
  else
    mkdir -p "${vol}"
    if [ -e "${home_dir}" ]; then                  # real dir (image default): move once
      cp -a "${home_dir}/." "${vol}/" 2>/dev/null || true
      rm -rf "${home_dir}"
    fi
    ln -sfn "${vol}" "${home_dir}"
  fi
}

# Which home paths to persist. Configurable via megh.yaml `persist:` (passed as
# MEGH_PERSIST, comma-separated), so a one-time `claude login` / `codex login` /
# etc. survives box rebuilds on the same volume. Add tools by editing that list.
# Default keeps the agent auth/config state even on older megh binaries.
PERSIST_DIRS="${MEGH_PERSIST:-~/.claude,~/.claude.json,~/.codex}"
IFS=',' read -ra _persist <<< "${PERSIST_DIRS}"
for _p in "${_persist[@]}"; do
  _p="${_p//[[:space:]]/}"          # trim whitespace
  [ -z "${_p}" ] && continue
  # An entry may declare its kind explicitly: "file:~/.gitconfig". Guessing is
  # only a fallback, because guessing wrong CREATES A DIRECTORY where a file
  # belongs and every tool then fails with "Is a directory".
  _forced=""
  case "${_p}" in
    file:*) _forced=file; _p="${_p#file:}" ;;
    dir:*)  _forced=dir;  _p="${_p#dir:}"  ;;
  esac
  _p="${_p#\~/}"; _p="${_p#/}"      # home-relative: strip a leading ~/ or /
  _home="${HOME}/${_p}"
  # Volume slot name: path with / -> - and the leading dot dropped (.claude -> claude).
  _name="$(printf '%s' "${_p}" | sed 's#/#-#g; s#^\.##')"
  # Kind: an explicit declaration wins, then what is actually on disk, then the
  # volume slot if a previous box already made one, and only then a guess.
  _b="$(basename "${_p}")"; _b="${_b#.}"
  if [ -n "${_forced}" ]; then _kind="${_forced}"
  elif [ -f "${_home}" ] && [ ! -d "${_home}" ]; then _kind=file
  elif [ -d "${_home}" ]; then _kind=dir
  elif [ -f "${WORK_MOUNT}/state/${_name}" ]; then _kind=file
  elif [ -d "${WORK_MOUNT}/state/${_name}" ]; then _kind=dir
  else
    # A second dot means a file (.claude.json). Plain dotfiles are ambiguous:
    # .claude and .codex are dirs, .gitconfig and .npmrc are files, and the name
    # alone cannot tell you. These are the common rc-style FILES; anything else
    # unknown stays a dir, which is the safer default for tool state.
    case "${_b}" in
      *.*) _kind=file ;;
      gitconfig|gitignore|gitattributes|bashrc|bash_profile|zshrc|zprofile|profile|inputrc|netrc|npmrc|curlrc|vimrc|gemrc|editorconfig|tmux.conf)
        _kind=file ;;
      *) _kind=dir ;;
    esac
  fi
  link_state "${_home}" "${WORK_MOUNT}/state/${_name}" "${_kind}"
done

# Map home paths onto volume locations (e.g. ~/newstack -> repos/newstack) so the
# paths your local scripts/tools expect resolve on the box. Configurable via
# megh.yaml `symlinks:` (passed as MEGH_SYMLINKS, "link:target" pairs). Targets are
# relative to /mnt/work unless absolute. Unlike `persist` this does not migrate
# anything: the target is authoritative (populated by `megh hydrate`). Skips a link
# that already exists as real content.
if [ -n "${MEGH_SYMLINKS:-}" ]; then
  IFS=',' read -ra _links <<< "${MEGH_SYMLINKS}"
  for _l in "${_links[@]}"; do
    link="${_l%%:*}"; target="${_l#*:}"
    [ -z "${link}" ] || [ -z "${target}" ] && continue
    link="${link/#\~/$HOME}"                              # ~ -> /root
    case "${target}" in /*) ;; *) target="/mnt/work/${target}" ;; esac   # relative -> /mnt/work
    if [ -e "${link}" ] && [ ! -L "${link}" ]; then
      log "symlink: ${link} exists as real content; leaving it"
      continue
    fi
    # Make the parents, not the target itself: the target may be a FILE (a dotfile
    # like repos/dotfiles/.vimrc) or a dir (repos/newstack), and it may not exist
    # until `megh hydrate` runs. mkdir-ing the target would wrongly create a dir.
    mkdir -p "$(dirname "${target}")" "$(dirname "${link}")"
    ln -sfn "${target}" "${link}"
    log "symlink: ${link} -> ${target}"
  done
fi

# ---------------------------------------------------------------------------
# 2. SSH. RunPod injects the user's key as PUBLIC_KEY. Agent forwarding means
#    no long-lived git credentials ever land on the box.
# ---------------------------------------------------------------------------
mkdir -p /root/.ssh /run/sshd
chmod 700 /root/.ssh
if [ -n "${PUBLIC_KEY:-}" ]; then
  echo "${PUBLIC_KEY}" >> /root/.ssh/authorized_keys
  chmod 600 /root/.ssh/authorized_keys
  log "installed injected PUBLIC_KEY"
fi
ssh-keygen -A >/dev/null 2>&1 || true

# Key auth ONLY, stated explicitly. Today this changes nothing: root's password
# is locked and no account on the box has a usable hash, so stock sshd already
# refuses passwords for root via PermitRootLogin prohibit-password. But that is
# a distro default, not a decision — the moment anything creates a user with a
# password, `PasswordAuthentication yes` becomes a live door on a port that is
# public whenever expose_ssh is true (and it must be, for any control machine
# that is not on the tailnet). sshd_config Includes this directory near the top
# and takes the first value it sees, so this wins.
cat > /etc/ssh/sshd_config.d/megh.conf <<'SSHD'
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin prohibit-password
PermitEmptyPasswords no
SSHD
# Never boot a box with no sshd at all: if the drop-in is somehow invalid, throw
# it away rather than leave the only door shut.
if ! /usr/sbin/sshd -t 2>/tmp/sshd-config-test.log; then
  log "sshd config invalid, dropping the megh hardening (see /tmp/sshd-config-test.log)"
  rm -f /etc/ssh/sshd_config.d/megh.conf
fi
/usr/sbin/sshd
log "sshd up on :22 (key auth only)"

# ---------------------------------------------------------------------------
# 3. Headed-browser display: Xvfb -> fluxbox -> x11vnc -> noVNC on :6080.
#    Playwright launches headed against DISPLAY=:99 and you watch it in a
#    browser tab (laptop or phone).
# ---------------------------------------------------------------------------
# Present only on the full flavor; slim skips the whole frontend display stack.
if command -v Xvfb >/dev/null 2>&1; then
  Xvfb :99 -screen 0 1920x1080x24 >/tmp/xvfb.log 2>&1 &
  sleep 1
  fluxbox >/tmp/fluxbox.log 2>&1 &
  # -localhost + a localhost websockify bind: these listen only on 127.0.0.1 and
  # are reached via Tailscale (or an SSH tunnel), never the public proxy.
  x11vnc -display :99 -forever -shared -nopw -localhost -rfbport 5900 -bg -o /tmp/x11vnc.log
  websockify --web=/usr/share/novnc 127.0.0.1:6080 localhost:5900 >/tmp/novnc.log 2>&1 &
  log "noVNC up on 127.0.0.1:6080 (headed display :99)"
else
  log "no headed-browser display (slim); run 'megh enable vnc' (or 'megh enable' to list) to add it"
fi

# ---------------------------------------------------------------------------
# 4. Optional hydrate: clone repos listed in REPOS (comma-separated git URLs)
#    into the scratch volume if absent. Canonical source is always git; this is
#    just seeding the fast working copy.
# ---------------------------------------------------------------------------
if [ -n "${REPOS:-}" ]; then
  IFS=',' read -ra _repos <<< "${REPOS}"
  for url in "${_repos[@]}"; do
    name="$(basename "${url%.git}")"
    dest="${WORK_MOUNT}/repos/${name}"
    if [ ! -d "${dest}/.git" ]; then
      log "cloning ${url} -> ${dest}"
      git clone "${url}" "${dest}" || log "clone failed: ${url}"
    fi
  done
fi

# ---------------------------------------------------------------------------
# 5. Web shell: ttyd serving a persistent tmux session on :7681.
# ---------------------------------------------------------------------------
cd /mnt/work
# Bind to 127.0.0.1: no public listener. Reached via Tailscale or SSH tunnel.
ttyd -i 127.0.0.1 -p 7681 -W -t titleFixed=megh tmux new -A -s main >/tmp/ttyd.log 2>&1 &
log "ttyd up on 127.0.0.1:7681 (tmux session 'main')"

# Second page: a mobile/tablet-optimized web terminal on :7682 (on-screen key
# bar for Esc/Ctrl/arrows/symbols/tmux + voice). Same tmux session as :7681, so
# it is another view of the same shell. The page is baked into the image at build
# time (see Dockerfile) and served here directly — a first-class peer of :7681,
# no `enable` needed. Older images may lack the baked page; regenerate it once
# from the baked megh binary as a fallback.
webterm_html=/opt/megh/webterm/term.html
if [ ! -f "${webterm_html}" ] && command -v megh >/dev/null 2>&1; then
  MEGH_WEBTERM_EMIT_ONLY=1 megh enable webterm --local >/tmp/webterm-emit.log 2>&1 || true
fi
if [ -f "${webterm_html}" ]; then
  ttyd -i 127.0.0.1 -p 7682 -W -t titleFixed=megh-webterm \
    --index "${webterm_html}" tmux new -A -s main >/tmp/ttyd-webterm.log 2>&1 &
  log "webterm up on 127.0.0.1:7682 (mobile key bar; same tmux session 'main')"
else
  log "webterm page unavailable; run 'megh enable webterm' to add the mobile web terminal"
fi

# code-server (VS Code in browser) on localhost, reached over tailscale/tunnel.
# Baked on the full flavor. On slim it is background-installed to the box's local
# disk on first boot, so you can shell in immediately and it comes online shortly.
if command -v code-server >/dev/null 2>&1; then
  code-server --bind-addr 127.0.0.1:8080 --auth none /mnt/work >/tmp/code-server.log 2>&1 &
  log "code-server up on 127.0.0.1:8080"
else
  log "code-server not baked (slim); background-installing to local disk"
  ( curl -fsSL https://code-server.dev/install.sh | sh >/tmp/code-server-install.log 2>&1 \
      && code-server --bind-addr 127.0.0.1:8080 --auth none /mnt/work >/tmp/code-server.log 2>&1 \
      || log "code-server background install failed (see /tmp/code-server-install.log)" ) &
fi

# ---------------------------------------------------------------------------
# 6. Tailscale (private mesh access). Container providers (RunPod) have no TUN
#    device, so tailscaled runs in userspace mode and `tailscale serve` bridges
#    the tailnet to the localhost surfaces above. Reach the box by name from any
#    device on your tailnet (laptop, phone) with nothing exposed publicly.
#    Skipped when TS_AUTHKEY is unset; then use SSH by ip:port + tunnels.
# ---------------------------------------------------------------------------
if [ -n "${TS_AUTHKEY:-}" ]; then
  # Bring Tailscale up via the shared helper baked into the megh binary
  # (internal/tsops/ts-up.sh), so boot and `megh doctor ts` run identical logic
  # and can never drift. TS_HOSTNAME/TS_AUTHKEY are read from this env.
  if megh doctor ts start --local; then
    log "tailscale up as '${TS_HOSTNAME:-megh-box}'; surfaces served on the tailnet"
  else
    log "tailscale up failed (see /tmp/tailscale-up.log); 'megh doctor ts logs' shows why, 'megh doctor ts setkey' re-keys; SSH by ip:port still works"
  fi
else
  log "TS_AUTHKEY unset; skipping tailscale (use SSH by ip:port + tunnels)"
fi

# ---------------------------------------------------------------------------
# 7. Shutdown. Agent transcripts are NOT pushed from here: doing so needed a
#    long-lived GitHub credential on the box, because a background timer cannot
#    use SSH agent forwarding. They live on the volume and are collected by the
#    control machine instead (see DESIGN.md, "Agent session history").
# ---------------------------------------------------------------------------
# Leave the tailnet on shutdown so an ephemeral node is removed immediately (the
# node-side opposite of `tailscale up`). Best-effort; complements `megh down` and
# covers terminations megh can't SSH for (tailnet-only boxes, console kills). Only
# fires if RunPod delivers SIGTERM with grace; a hard SIGKILL relies on ephemeral GC.
ts_logout() { tailscale --socket=/var/run/tailscale/tailscaled.sock logout >/dev/null 2>&1 || true; }
on_term() { log "shutdown signal: leaving tailnet"; ts_logout; exit 0; }
trap on_term TERM INT

cat <<EOF
[megh] box is up.
       tailnet   : http://${TS_HOSTNAME:-<box>}:7681            (web shell)
                   http://${TS_HOSTNAME:-<box>}:7682            (mobile web shell + key bar)
                   http://${TS_HOSTNAME:-<box>}:6080/vnc.html   (headed browser)
       ssh       : ssh -A root@<ip> -p <port>  (key auth; ip:port from launch output)
       tunnel    : ssh -A -L 7681:localhost:7681 -L 6080:localhost:6080 root@<ip> -p <port>
       work dir  : /mnt/work  ->  ${WORK_MOUNT}
EOF

# PID 1 stays alive but signal-responsive, so the shutdown flush trap can run.
tail -f /dev/null &
wait $!
