#!/usr/bin/env bash
# Feature: redis — a real Redis on the box, no containers. Idempotent.
#
# Same reasoning as the postgres feature: RunPod pods cannot run containers, and
# Ubuntu's redis 7 matches the redis:7-alpine the compose files pin, so this is
# parity rather than a substitute.
#
# Default port 6399 matches cachewarden's compose (which maps 6399->6379 to dodge
# a local redis), so that project connects with no config change.
#
# Projects share one server and separate themselves with numbered databases or a
# key prefix. That is the redis-native equivalent of one postgres cluster with a
# database per project: isolation without paying for N servers.
#
# Data lives on the box's LOCAL disk, like postgres. Redis *can* keep it on the
# volume (verified: it needs write access, not ownership, unlike postgres), but
# these are dev boxes and there is no expectation that a database survives one.
# Sharing DB storage between boxes is a non-goal: seed with fixtures, or use a
# real cloud datastore if data genuinely has to outlive a box.
#
# Knobs: MEGH_REDIS_PORT (default 6399),
#        MEGH_REDIS_DATA (default /var/lib/megh-redis; the volume works too).
set -uo pipefail
log() { echo "[megh-redis] $*"; }

PORT="${MEGH_REDIS_PORT:-6399}"
DATA="${MEGH_REDIS_DATA:-/var/lib/megh-redis}"
CONF=/etc/redis/megh.conf

# Current images bake redis (provision.sh), so this is normally a no-op; it
# stays as the retrofit path for boxes from an older image.
if ! command -v redis-server >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  log "installing redis-server"
  apt-get update -qq >/tmp/redis-apt.log 2>&1
  apt-get install -y -qq --no-install-recommends redis-server >>/tmp/redis-apt.log 2>&1 \
    || { log "apt install failed (see /tmp/redis-apt.log)"; exit 1; }
fi
# The package ships an init-managed server on 6379; this feature runs its own on
# the volume instead, so stop the packaged one rather than fight it for the port.
service redis-server stop >/dev/null 2>&1 || true

mkdir -p "${DATA}" /etc/redis
cat > "${CONF}" <<EOF
# megh-managed redis. Loopback only: RunPod's public proxy is unauthenticated,
# so a wildcard bind would put an unauthenticated datastore on the internet.
bind 127.0.0.1
protected-mode yes
port ${PORT}
dir ${DATA}
dbfilename megh.rdb
logfile /tmp/redis.log
daemonize no
# RDB snapshots rather than appendonly: a dev cache does not need per-operation
# durability, and AOF's per-write fsync is the pathological case if anyone moves
# the data dir onto the NFS volume.
appendonly no
save 60 1000
EOF

cat > /usr/local/bin/redisctl <<CTRL
#!/usr/bin/env bash
# Control the megh redis server.  redisctl start|stop|status|logs|cli [args]
set -uo pipefail
PORT="${PORT}"; CONF="${CONF}"; DATA="${DATA}"
CTRL
cat >> /usr/local/bin/redisctl <<'CTRL'
up() { redis-cli -h 127.0.0.1 -p "${PORT}" ping >/dev/null 2>&1; }
case "${1:-status}" in
  start)
    up && { echo "  already running on 127.0.0.1:${PORT}"; exit 0; }
    nohup redis-server "${CONF}" >/tmp/redis-stdout.log 2>&1 &
    for _ in $(seq 1 15); do up && break; sleep 1; done
    up && echo "  redis up on 127.0.0.1:${PORT}" || { echo "  failed to start"; tail -5 /tmp/redis.log; exit 1; }
    ;;
  stop)
    up || { echo "  not running"; exit 0; }
    redis-cli -h 127.0.0.1 -p "${PORT}" shutdown nosave 2>/dev/null
    echo "  stopped"
    ;;
  status)
    if up; then echo "  redis up on 127.0.0.1:${PORT} (data ${DATA})"
    else echo "  redis down (data ${DATA})"; fi
    ;;
  logs) tail -n 40 /tmp/redis.log ;;
  cli) shift; redis-cli -h 127.0.0.1 -p "${PORT}" "$@" ;;
  reset)
    # Dev data is disposable by design; make throwing it away a one-liner rather
    # than something you improvise with rm at 2am.
    up && { redis-cli -h 127.0.0.1 -p "${PORT}" shutdown nosave 2>/dev/null; sleep 1; }
    rm -rf "${DATA}"/*; mkdir -p "${DATA}"
    echo "  wiped ${DATA}; start again with: redisctl start"
    ;;
  *) echo "usage: redisctl start|stop|status|logs|cli [args]|reset"; exit 2 ;;
esac
CTRL
chmod +x /usr/local/bin/redisctl

/usr/local/bin/redisctl start || exit 1
redis-server --version | sed 's/^/[megh-redis] /'
log "control it with: redisctl start|stop|status|logs|cli"
log "a project connects with: redis://127.0.0.1:${PORT}"
log "from your Mac: ssh -L ${PORT}:localhost:${PORT} ... (nothing is public)"
