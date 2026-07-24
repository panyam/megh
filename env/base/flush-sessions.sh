#!/usr/bin/env bash
# Flush agent session TRANSCRIPTS to a durable, searchable git repo.
#
# Runs on a timer and at box shutdown (see entrypoint.sh). It pushes only
# transcript/memory files, never credentials, so your Claude/Codex history
# survives the disposable box and is greppable forever, independent of provider
# or volume.
#
# Enabled when both are set (passed as pod env):
#   MEGH_SESSIONS_REPO   owner/repo, e.g. panyam/megh-sessions (private)
#   MEGH_SESSIONS_TOKEN  fine-grained PAT scoped to ONLY that repo,
#                        contents:write. Deliberately narrow: a compromised box
#                        can write your session history and nothing else.
#
# The token is never persisted in git config; it is used only inline at push.
set -uo pipefail

log() { echo "[megh-flush] $*"; }

repo="${MEGH_SESSIONS_REPO:-}"
token="${MEGH_SESSIONS_TOKEN:-}"
[ -z "${repo}" ] && exit 0
[ -z "${token}" ] && { log "MEGH_SESSIONS_TOKEN unset; cannot push"; exit 0; }

work="${WORK_MOUNT:-/workspace}"
repo_dir="${work}/state/sessions-repo"
auth_url="https://x-access-token:${token}@github.com/${repo}.git"
plain_url="https://github.com/${repo}.git"

# Clone once (or init if the remote is still empty). Store the PLAIN url as
# origin so the token never lands in .git/config; push with the auth url inline.
if [ ! -d "${repo_dir}/.git" ]; then
  if ! git clone --depth 1 "${auth_url}" "${repo_dir}" >/tmp/flush-clone.log 2>&1; then
    mkdir -p "${repo_dir}"
    git -C "${repo_dir}" init -q
  fi
  git -C "${repo_dir}" remote set-url origin "${plain_url}" 2>/dev/null \
    || git -C "${repo_dir}" remote add origin "${plain_url}"
  git -C "${repo_dir}" config user.email "megh@localhost"
  git -C "${repo_dir}" config user.name "megh"
fi

host="$(hostname)"
dest="${repo_dir}/${host}"
mkdir -p "${dest}"

# Allowlist: only known transcript locations, which live UNDER these subdirs and
# so never include the top-level credential files (.credentials.json, auth.json).
# Belt-and-suspenders excludes in case a transcript dir ever holds a secret.
excludes=(--exclude '*.credentials.json' --exclude 'auth.json'
          --exclude '*token*' --exclude '*.pem' --exclude '*.key')
copy() {
  local src="$1" name="$2"
  [ -d "${src}" ] || return 0
  mkdir -p "${dest}/${name}"
  rsync -a --delete "${excludes[@]}" "${src}/" "${dest}/${name}/" 2>/dev/null || true
}
copy "${work}/state/claude/projects" claude-projects
copy "${work}/state/codex/sessions"  codex-sessions

git -C "${repo_dir}" add -A
if git -C "${repo_dir}" diff --cached --quiet; then
  exit 0   # nothing new
fi
git -C "${repo_dir}" commit -q -m "sessions: ${host} $(date -u +%FT%TZ)" || exit 0
if git -C "${repo_dir}" push -q "${auth_url}" HEAD:refs/heads/main >/tmp/flush-push.log 2>&1; then
  log "pushed sessions for ${host}"
else
  log "push failed (see /tmp/flush-push.log)"
fi
