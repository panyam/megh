#!/bin/sh
# megh-tmux-attach: the command both web terminals (ttyd :7681 and :7682) run.
# ttyd -a appends each ?arg= in the page URL as an argument, so the URL picks the
# tmux session: http://<box>:7682/?arg=book attaches session "book", creating it
# if needed, and no ?arg attaches the default (main). The same sessions are what
# `megh ssh --session` and `megh tmux attach` reach.
#
# A URL value lands on this command line, so only a plain name gets through:
# letters, digits, _ and -, at most 32. tmux itself rejects "." and ":".
refuse() {
  printf 'megh: %s\r\n' "$1"
  sleep "${MEGH_ATTACH_REFUSE_WAIT:-5}"   # long enough to read in the browser
  exit 1
}
[ "$#" -le 1 ] || refuse "pass one session in the URL (?arg=<name>), not $#"
name="${1:-${MEGH_TMUX_SESSION:-main}}"
case "$name" in
  *[!A-Za-z0-9_-]*) refuse "'$name' is not a session name (letters, digits, _ and -)" ;;
esac
[ "${#name}" -le 32 ] || refuse "session names are at most 32 characters"
exec tmux new -A -s "$name"
