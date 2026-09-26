#!/bin/bash
# Save/restore tmux sessions to the scratch volume via tmux-resurrect, so a
# local box can survive docker stop (Colima sleep, accidental stop) and come
# back with layout, scrollback, and (best-effort) restarted pane commands.
set -euo pipefail

TMUX_PLUGIN_DIR="${TMUX_PLUGIN_DIR:-/opt/megh/tmux-plugins}"
_resurrect="${TMUX_PLUGIN_DIR}/tmux-resurrect"
_save="${_resurrect}/scripts/save.sh"
_restore="${_resurrect}/scripts/restore.sh"

megh_resurrect_dir() {
  if [ -z "${WORK_MOUNT:-}" ]; then
    echo "/mnt/work/state/tmux-resurrect"
  else
    echo "${WORK_MOUNT}/state/tmux-resurrect"
  fi
}

# Warn every tmux pane (and wall every open login) before checkpoint. Grace period
# is MEGH_SHUTDOWN_GRACE seconds (default 15); keep docker --stop-timeout above
# grace + ~10s for save/logout.
megh_tmux_shutdown_warn() {
  local grace="${MEGH_SHUTDOWN_GRACE:-15}"
  case "${grace}" in
    '' | *[!0-9]*) grace=15 ;;
  esac
  if [ "${grace}" -gt 120 ]; then grace=120; fi

  local msg="[megh] Box stopping in ${grace}s — save your work (tmux restores on next start)"
  local ms=$((grace * 1000))

  if command -v wall >/dev/null 2>&1 && [ "${grace}" -gt 0 ]; then
    wall "${msg}" 2>/dev/null || true
  fi

  command -v tmux >/dev/null 2>&1 || return 0
  tmux list-sessions >/dev/null 2>&1 || return 0

  tmux display-message -a -d "${ms}" "${msg}" 2>/dev/null || true
  tmux list-panes -a -F '#{pane_id}' 2>/dev/null | while read -r pid; do
    [ -n "${pid}" ] || continue
    tmux display-message -p -t "${pid}" -d "${ms}" "${msg}" 2>/dev/null \
      || tmux display-message -t "${pid}" -d "${ms}" "${msg}" 2>/dev/null \
      || true
  done

  if [ "${grace}" -gt 0 ]; then
    sleep "${grace}"
  fi
}

megh_tmux_checkpoint_save() {
  [ -x "${_save}" ] || return 0
  command -v tmux >/dev/null 2>&1 || return 0
  tmux list-sessions >/dev/null 2>&1 || return 0
  local dir
  dir="$(megh_resurrect_dir)"
  mkdir -p "${dir}"
  export TMUX_PLUGIN_DIR
  # save.sh reads @resurrect-dir from tmux options; ensure server has loaded conf.
  tmux source-file /etc/tmux.conf 2>/dev/null || true
  "${_save}" >>/tmp/tmux-save.log 2>&1 || true
}

megh_tmux_checkpoint_restore() {
  [ -x "${_restore}" ] || return 0
  local dir
  dir="$(megh_resurrect_dir)"
  mkdir -p "${dir}"
  if [ ! -e "${dir}/last" ] && [ -z "$(ls -A "${dir}" 2>/dev/null | head -1 || true)" ]; then
    return 0
  fi
  export TMUX_PLUGIN_DIR
  tmux start-server 2>/dev/null || true
  tmux source-file /etc/tmux.conf 2>/dev/null || true
  "${_restore}" >>/tmp/tmux-restore.log 2>&1 || return 1
}

case "${1:-}" in
save) megh_tmux_checkpoint_save ;;
restore) megh_tmux_checkpoint_restore ;;
warn) megh_tmux_shutdown_warn ;;
*) echo "usage: $0 save|restore|warn" >&2; exit 2 ;;
esac
