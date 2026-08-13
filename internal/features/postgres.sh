#!/usr/bin/env bash
# Feature: postgres — a real PostgreSQL + pgvector on the box, no containers.
# Idempotent.
#
# Why native: RunPod pods cannot run containers at all (see DESIGN.md), so the
# compose files these repos ship cannot be used. Native is not a downgrade here:
# PGDG carries the same major the compose files pin, so this is version parity,
# not a compromise.
#
# ONE CLUSTER, ONE DATABASE PER PROJECT. Per-database overhead is negligible
# while per-cluster memory is not, so projects get real isolation without paying
# for N servers. `pg db add <name>` creates the role, the database and the vector
# extension in one step.
#
# Defaults deliberately match what the repos already expect, so a project needs
# NO config change: port 5433 (diffpp's compose maps 5433->5432 and its default
# DIFFPP_DB_URL follows), and `pg db add diffpp` yields role/password/database
# all named diffpp, exactly as its compose environment does.
#
# THE DATA DIRECTORY CANNOT LIVE ON THE VOLUME. Measured on a live box: the
# RunPod volume is nfs4 with sec=sys and root squashed, so chown is denied for
# EVERY uid including root, and a 0777 directory does not help because initdb
# also chmods to 0700. postgres requires ownership of its data directory, so it
# runs on the box's local disk and durability comes from logical dumps written
# to the volume instead (`pg dump`, auto-restored into a fresh cluster).
# Redis has no such problem and does keep its data on the volume: it needs write
# access, not ownership.
#
# Knobs: MEGH_PG_VERSION (default 18), MEGH_PG_PORT (5433),
#        MEGH_PG_DATA (default /var/lib/megh-pg, local disk),
#        MEGH_PG_DUMPS (default /mnt/work/state/postgres-dumps, on the volume).
set -uo pipefail
log() { echo "[megh-postgres] $*"; }

PGVER="${MEGH_PG_VERSION:-18}"
PGPORT="${MEGH_PG_PORT:-5433}"
PGROOT="${MEGH_PG_DATA:-/var/lib/megh-pg}"
PGDUMPS="${MEGH_PG_DUMPS:-/mnt/work/state/postgres-dumps}"
PGDATA="${PGROOT}/${PGVER}"
PGBIN="/usr/lib/postgresql/${PGVER}/bin"

# --- 1. install from PGDG (retrofit only) ------------------------------------
# Current images bake postgres + pgvector (provision.sh), so this is normally a
# no-op. It stays for boxes launched from an older image, the same way
# `megh enable webterm` retrofits the baked page. Ubuntu's own archive is a
# major behind (16); PGDG carries the pinned major.
if [ ! -x "${PGBIN}/postgres" ]; then
  export DEBIAN_FRONTEND=noninteractive
  log "installing postgresql-${PGVER} + pgvector from PGDG"
  apt-get install -y -qq --no-install-recommends curl ca-certificates gnupg >/dev/null 2>&1
  curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc \
    | gpg --dearmor -o /usr/share/keyrings/pgdg.gpg 2>/dev/null
  echo "deb [signed-by=/usr/share/keyrings/pgdg.gpg] https://apt.postgresql.org/pub/repos/apt $(. /etc/os-release && echo "${VERSION_CODENAME}")-pgdg main" \
    > /etc/apt/sources.list.d/pgdg.list
  apt-get update -qq >/tmp/pg-apt.log 2>&1
  if ! apt-get install -y -qq --no-install-recommends \
        "postgresql-${PGVER}" "postgresql-${PGVER}-pgvector" >>/tmp/pg-apt.log 2>&1; then
    log "apt install failed (see /tmp/pg-apt.log)"; exit 1
  fi
fi

# The package creates its own cluster on the CONTAINER disk, which does not
# survive a rebuild. Drop it; the cluster this feature manages lives on the
# volume and is started by the `pg` control script, not by systemd.
pg_dropcluster --stop "${PGVER}" main >/dev/null 2>&1 || true

# --- 2. initdb onto the data root -------------------------------------------
mkdir -p "${PGROOT}" "${PGDUMPS}"
FRESH=0
if [ ! -s "${PGDATA}/PG_VERSION" ]; then
  FRESH=1
  # postgres refuses to run as root, so the data directory must belong to the
  # postgres user. This is precisely why the default is local disk: the volume
  # denies chown to every uid. Anyone pointing MEGH_PG_DATA at it gets this
  # message rather than a confusing failure inside initdb.
  mkdir -p "${PGDATA}"
  if ! chown -R postgres:postgres "${PGROOT}" 2>/dev/null; then
    log "cannot chown ${PGROOT} to postgres."
    log "if this is on /mnt/work: the volume is NFS with root squashed, so postgres"
    log "cannot own its data dir there. Use local disk (the default) and rely on"
    log "'pg dump' to keep a copy on the volume."
    exit 1
  fi
  chmod 0700 "${PGDATA}"
  log "initdb -> ${PGDATA}"
  if ! su postgres -c "${PGBIN}/initdb -D '${PGDATA}' -E UTF8 --locale=C" >/tmp/pg-initdb.log 2>&1; then
    log "initdb FAILED (see /tmp/pg-initdb.log)"; tail -5 /tmp/pg-initdb.log; exit 1
  fi
fi

# Loopback only: RunPod's public proxy is unauthenticated, so a database on a
# wildcard address would be a database on the public internet. Reach it from the
# Mac with an SSH tunnel.
{
  echo "listen_addresses = 'localhost'"
  echo "port = ${PGPORT}"
  echo "unix_socket_directories = '/tmp'"
} > "${PGDATA}/conf.d-megh.conf"
grep -q "conf.d-megh.conf" "${PGDATA}/postgresql.conf" 2>/dev/null \
  || echo "include = '${PGDATA}/conf.d-megh.conf'" >> "${PGDATA}/postgresql.conf"
chown postgres:postgres "${PGDATA}/conf.d-megh.conf" 2>/dev/null

# --- 3. control script -------------------------------------------------------
# Only the values that must expand go in the UNQUOTED heredoc. Everything else
# lives in the quoted one below, because an unquoted heredoc also runs command
# substitution, so a backtick in a comment would execute (see the test).
cat > /usr/local/bin/pg <<CTRL
#!/usr/bin/env bash
PGVER="${PGVER}"; PGPORT="${PGPORT}"; PGDATA="${PGDATA}"; PGBIN="${PGBIN}"
PGDUMPS="${PGDUMPS}"
CTRL
cat >> /usr/local/bin/pg <<'CTRL'
# Control the megh postgres cluster.
#   pg start|stop|status|logs|psql [db]|db add|db drop|db list|dump [db]|restore [db]
#
# The cluster is on the box's LOCAL disk (the volume cannot own it; see the
# feature script). `dump` writes logical backups to the volume and `restore`
# reads them back, so a rebuilt box rehydrates. `stop` dumps automatically.
set -uo pipefail
LOG=/tmp/postgres.log
run() { su postgres -c "$1"; }
up() { "${PGBIN}/pg_isready" -h 127.0.0.1 -p "${PGPORT}" -q 2>/dev/null; }

case "${1:-status}" in
  start)
    up && { echo "  already running on 127.0.0.1:${PGPORT}"; exit 0; }
    run "${PGBIN}/pg_ctl -D '${PGDATA}' -l ${LOG} -w start" || { tail -5 "$LOG"; exit 1; }
    echo "  postgres up on 127.0.0.1:${PGPORT}"
    ;;
  stop)
    up || { echo "  not running"; exit 0; }
    # Dump before stopping: the cluster is on the ephemeral disk, so a stop that
    # is followed by a box teardown would otherwise lose everything.
    pg dump >/dev/null 2>&1 || echo "  warning: dump before stop failed"
    run "${PGBIN}/pg_ctl -D '${PGDATA}' -m fast -w stop" && echo "  stopped"
    ;;
  dump)
    up || { echo "  start it first: pg start"; exit 1; }
    mkdir -p "${PGDUMPS}"
    # Roles are CLUSTER-global, so pg_dump (which is per-database) does not carry
    # them. Without this the databases come back after a rebuild but nothing can
    # log in as their owner.
    if run "${PGBIN}/pg_dumpall -h 127.0.0.1 -p ${PGPORT} --roles-only" > "${PGDUMPS}/_roles.sql" 2>/dev/null; then
      echo "  dumped roles -> ${PGDUMPS}/_roles.sql"
    else
      echo "  FAILED to dump roles"; rm -f "${PGDUMPS}/_roles.sql"
    fi
    dbs="${2:-}"
    [ -z "$dbs" ] && dbs=$(run "${PGBIN}/psql -h 127.0.0.1 -p ${PGPORT} -tAc \"SELECT datname FROM pg_database WHERE datistemplate=false AND datname<>'postgres'\"")
    for d in $dbs; do
      # Plain SQL, not the custom format: a dump you can read and grep is worth
      # more on a dev box than one that needs pg_restore to inspect.
      if run "${PGBIN}/pg_dump -h 127.0.0.1 -p ${PGPORT} --create --clean --if-exists '$d'" > "${PGDUMPS}/$d.sql" 2>/dev/null; then
        echo "  dumped $d -> ${PGDUMPS}/$d.sql ($(wc -c < "${PGDUMPS}/$d.sql") bytes)"
      else
        echo "  FAILED to dump $d"; rm -f "${PGDUMPS}/$d.sql"
      fi
    done
    ;;
  restore)
    up || { echo "  start it first: pg start"; exit 1; }
    # Roles first: a database dump assigns ownership to a role that must already
    # exist. Errors are ignored because a fresh cluster already has the bootstrap
    # superuser and re-creating it is a harmless failure.
    if [ -s "${PGDUMPS}/_roles.sql" ]; then
      # "role already exists" is expected (the bootstrap superuser is in every
      # dump), so errors are tolerated. But do not claim success blindly: report
      # what is actually there afterwards, because a restore that silently did
      # nothing looks identical to one that worked.
      run "${PGBIN}/psql -h 127.0.0.1 -p ${PGPORT} -q -f '${PGDUMPS}/_roles.sql' postgres" >/tmp/pg-restore-roles.log 2>&1
      n=$(run "${PGBIN}/psql -h 127.0.0.1 -p ${PGPORT} -tAc \"SELECT count(*) FROM pg_roles WHERE rolcanlogin AND rolname<>'postgres'\"" 2>/dev/null)
      echo "  roles applied (${n:-0} login role(s) present; see /tmp/pg-restore-roles.log)"
    fi
    for f in "${PGDUMPS}"/${2:-*}.sql; do
      [ -e "$f" ] || { echo "  no dumps in ${PGDUMPS}"; break; }
      d=$(basename "$f" .sql)
      [ "$d" = "_roles" ] && continue
      run "${PGBIN}/psql -h 127.0.0.1 -p ${PGPORT} -q -f '$f' postgres" >"/tmp/pg-restore-$d.log" 2>&1
      if grep -q '^psql:.*ERROR' "/tmp/pg-restore-$d.log"; then
        echo "  restored $d WITH ERRORS (see /tmp/pg-restore-$d.log)"
      else
        echo "  restored $d"
      fi
    done
    ;;
  status)
    if up; then echo "  postgres up on 127.0.0.1:${PGPORT} (data ${PGDATA})"
    else echo "  postgres down (data ${PGDATA})"; fi
    ;;
  logs) tail -n 40 "$LOG" ;;
  psql) shift; run "${PGBIN}/psql -h 127.0.0.1 -p ${PGPORT} ${1:-postgres}" ;;
  db)
    case "${2:-}" in
      add)
        n="${3:-}"; [ -z "$n" ] && { echo "usage: pg db add <name>"; exit 2; }
        up || { echo "  start it first: pg start"; exit 1; }
        # Role, database and password all share the project name. That matches
        # what the projects' compose files already set, so their existing DSNs
        # work unchanged. Safe only because the cluster is loopback-only.
        run "${PGBIN}/psql -h 127.0.0.1 -p ${PGPORT} -tAc \"SELECT 1 FROM pg_roles WHERE rolname='$n'\"" \
          | grep -q 1 || run "${PGBIN}/psql -h 127.0.0.1 -p ${PGPORT} -c \"CREATE ROLE $n LOGIN PASSWORD '$n'\""
        run "${PGBIN}/psql -h 127.0.0.1 -p ${PGPORT} -tAc \"SELECT 1 FROM pg_database WHERE datname='$n'\"" \
          | grep -q 1 || run "${PGBIN}/psql -h 127.0.0.1 -p ${PGPORT} -c \"CREATE DATABASE $n OWNER $n\""
        run "${PGBIN}/psql -h 127.0.0.1 -p ${PGPORT} -d $n -c 'CREATE EXTENSION IF NOT EXISTS vector'"
        echo "  ready: postgres://$n:$n@127.0.0.1:${PGPORT}/$n"
        ;;
      drop)
        n="${3:-}"; [ -z "$n" ] && { echo "usage: pg db drop <name>"; exit 2; }
        read -r -p "drop database '$n' and its role? [y/N] " a; [ "$a" = y ] || exit 0
        run "${PGBIN}/psql -h 127.0.0.1 -p ${PGPORT} -c 'DROP DATABASE IF EXISTS $n'"
        run "${PGBIN}/psql -h 127.0.0.1 -p ${PGPORT} -c 'DROP ROLE IF EXISTS $n'"
        echo "  dropped $n"
        ;;
      list|"") run "${PGBIN}/psql -h 127.0.0.1 -p ${PGPORT} -c '\l'" ;;
    esac
    ;;
  *) echo "usage: pg start|stop|status|logs|psql [db]|db add <name>|db drop <name>|db list"; exit 2 ;;
esac
CTRL
chmod +x /usr/local/bin/pg

# --- 4. start, rehydrating a fresh cluster from the volume --------------------
/usr/local/bin/pg start || exit 1
# A fresh cluster on a rebuilt box is empty, but the dumps on the volume are not.
# Restoring here is what makes the local-disk data directory acceptable.
if [ "${FRESH}" = "1" ] && compgen -G "${PGDUMPS}/*.sql" >/dev/null 2>&1; then
  log "fresh cluster; restoring dumps from ${PGDUMPS}"
  /usr/local/bin/pg restore
fi
"${PGBIN}/psql" -V 2>/dev/null | sed 's/^/[megh-postgres] /'
log "control it with: pg start|stop|status|db add <name>|psql <db>"
log "a project connects with: postgres://<name>:<name>@127.0.0.1:${PGPORT}/<name>"
log "from your Mac: ssh -L ${PGPORT}:localhost:${PGPORT} ... (nothing is public)"
