#!/usr/bin/env bash
# Feature: lgtm — a dev/demo observability stack: Grafana + Loki + Tempo + Mimir
# behind one OpenTelemetry Collector. OTLP in on :4317/:4318, Grafana out on
# :3000. Idempotent.
#
# Shape, and why:
#   - ONE stack, many projects. Loki/Mimir/Tempo are natively multi-tenant
#     (X-Scope-OrgID), so a project is a TENANT, not another instance. Per-tenant
#     data sits under its own directory, so `lgtm purge <tenant>` is a delete.
#   - Apps speak OTLP to the collector, which forwards the caller's X-Scope-OrgID
#     downstream (headers_setter). A project picks its tenant with one env var:
#       OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
#       OTEL_EXPORTER_OTLP_HEADERS=X-Scope-OrgID=<project>
#     No header means tenant "default".
#   - EVERYTHING (binaries, configs, data) lives on the volume under
#     /mnt/work/state/lgtm, so it survives a box rebuild and re-enabling is just
#     re-linking. Nothing here is on the ephemeral container disk.
#   - It does NOT run at boot. This is a demo stack you bring up for hours or
#     days: `lgtm start` / `lgtm stop` / `lgtm status` / `lgtm purge <tenant>`.
#
# All services bind 127.0.0.1 (RunPod's public proxy is unauthenticated). Reach
# Grafana over the tailnet or `megh browse 3000`.
set -uo pipefail
log() { echo "[megh-lgtm] $*"; }

ROOT="${MEGH_LGTM_ROOT:-/mnt/work/state/lgtm}"
BIN="${ROOT}/bin"
CFG="${ROOT}/config"
DATA="${ROOT}/data"
RUN=/tmp/lgtm
ARCH=amd64

mkdir -p "${BIN}" "${CFG}" "${RUN}" \
         "${DATA}"/{loki,tempo,grafana} \
         "${DATA}"/mimir/{blocks,ruler,alertmanager,tsdb,sync,compactor}

# --- 1. binaries (onto the VOLUME, so a rebuild does not re-download) ---------
# Versions float to each project's latest release unless pinned, because the
# asset names embed versions and a stale pin here fails closed. Pin with
# MEGH_LGTM_{LOKI,TEMPO,MIMIR,OTELCOL,GRAFANA}_VERSION to freeze a demo.
gh_latest() { # repo -> bare X.Y.Z
  # Tags are not uniform: grafana/loki uses "v3.7.6", grafana/mimir uses
  # "mimir-3.1.4". Strip everything before the first digit rather than assuming.
  curl -fsSL "https://api.github.com/repos/$1/releases/latest" 2>/dev/null \
    | grep -m1 '"tag_name"' \
    | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/; s/^[^0-9]*//'
}

# Unauthenticated GitHub API is 60/hr per IP; fail loudly rather than building a
# nonsense URL from an empty version.
ver() { # name repo override
  local v="$3"
  [ -z "$v" ] && v="$(gh_latest "$2")"
  [ -z "$v" ] && { log "could not resolve $1 version (GitHub API rate limit? pin MEGH_LGTM_$(echo "$1" | tr a-z A-Z)_VERSION)"; exit 1; }
  echo "$v"
}

# install(1), not mv(1): the volume is NFS and mv tries to preserve ownership,
# which it is not permitted to do there.
put() { install -m 0755 "$1" "$2" && rm -f "$1"; }

need() { [ -x "${BIN}/$1" ]; }

if ! need loki; then
  v="$(ver loki grafana/loki "${MEGH_LGTM_LOKI_VERSION:-}")"
  log "fetching loki ${v}"
  curl -fsSL "https://github.com/grafana/loki/releases/download/v${v}/loki-linux-${ARCH}.zip" \
    -o /tmp/loki.zip && unzip -oq /tmp/loki.zip -d /tmp \
    && put "/tmp/loki-linux-${ARCH}" "${BIN}/loki" \
    || { log "loki fetch FAILED"; exit 1; }
fi

if ! need tempo; then
  v="$(ver tempo grafana/tempo "${MEGH_LGTM_TEMPO_VERSION:-}")"
  log "fetching tempo ${v}"
  curl -fsSL "https://github.com/grafana/tempo/releases/download/v${v}/tempo_${v}_linux_${ARCH}.tar.gz" \
    -o /tmp/tempo.tgz && tar --no-same-owner -xzf /tmp/tempo.tgz -C /tmp tempo \
    && put /tmp/tempo "${BIN}/tempo" \
    || { log "tempo fetch FAILED"; exit 1; }
fi

if ! need mimir; then
  # NOTE: mimir's release TAG is "mimir-<ver>", not "v<ver>".
  v="$(ver mimir grafana/mimir "${MEGH_LGTM_MIMIR_VERSION:-}")"
  log "fetching mimir ${v}"
  curl -fsSL "https://github.com/grafana/mimir/releases/download/mimir-${v}/mimir-linux-${ARCH}" \
    -o /tmp/mimir && put /tmp/mimir "${BIN}/mimir" \
    || { log "mimir fetch FAILED"; exit 1; }
fi

if ! need otelcol-contrib; then
  v="$(ver otelcol open-telemetry/opentelemetry-collector-releases "${MEGH_LGTM_OTELCOL_VERSION:-}")"
  log "fetching otelcol-contrib ${v}"
  curl -fsSL "https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v${v}/otelcol-contrib_${v}_linux_${ARCH}.tar.gz" \
    -o /tmp/otelcol.tgz && tar --no-same-owner -xzf /tmp/otelcol.tgz -C /tmp otelcol-contrib \
    && put /tmp/otelcol-contrib "${BIN}/otelcol-contrib" \
    || { log "otelcol fetch FAILED"; exit 1; }
fi

if [ ! -x "${BIN}/grafana/bin/grafana" ]; then
  v="$(ver grafana grafana/grafana "${MEGH_LGTM_GRAFANA_VERSION:-}")"
  log "fetching grafana ${v}"
  # --no-same-owner: the volume is NFS and cannot restore the tarball's uid/gid.
  curl -fsSL "https://dl.grafana.com/oss/release/grafana-${v}.linux-${ARCH}.tar.gz" -o /tmp/grafana.tgz \
    && mkdir -p "${BIN}/grafana" \
    && tar --no-same-owner -xzf /tmp/grafana.tgz -C "${BIN}/grafana" --strip-components=1 \
    || { log "grafana fetch FAILED"; exit 1; }
fi
rm -f /tmp/loki.zip /tmp/tempo.tgz /tmp/otelcol.tgz /tmp/grafana.tgz

# --- 2. configs (written once; edit them freely, MEGH_LGTM_FORCE_CONFIG=1 resets) ---
w() { # path — write stdin unless it exists
  if [ -e "$1" ] && [ "${MEGH_LGTM_FORCE_CONFIG:-0}" != "1" ]; then cat >/dev/null; return; fi
  cat > "$1"
}

w "${CFG}/loki.yaml" <<EOF
auth_enabled: true            # multi-tenant: X-Scope-OrgID required
server:
  http_listen_address: 127.0.0.1
  http_listen_port: 3100
  grpc_listen_address: 127.0.0.1
  grpc_listen_port: 9096
  log_level: warn
common:
  instance_addr: 127.0.0.1
  path_prefix: ${DATA}/loki
  storage:
    filesystem:
      chunks_directory: ${DATA}/loki/chunks
      rules_directory: ${DATA}/loki/rules
  replication_factor: 1
  ring:
    kvstore:
      store: inmemory
schema_config:
  configs:
    - from: 2024-01-01
      store: tsdb
      object_store: filesystem
      schema: v13
      index:
        prefix: index_
        period: 24h
limits_config:
  allow_structured_metadata: true
  volume_enabled: true
  reject_old_samples: false
EOF

w "${CFG}/tempo.yaml" <<EOF
multitenancy_enabled: true
server:
  http_listen_address: 127.0.0.1
  http_listen_port: 3200
  grpc_listen_address: 127.0.0.1
  grpc_listen_port: 9097
  log_level: warn
distributor:
  receivers:
    otlp:
      protocols:
        # 14317, not 4317: the collector owns the public OTLP ports.
        grpc:
          endpoint: 127.0.0.1:14317
# NOTE: tempo 3.x removed the top-level ingester/compactor blocks that 2.x took;
# setting them is a hard config parse error. Defaults are fine for a demo stack.
storage:
  trace:
    backend: local
    local:
      path: ${DATA}/tempo/blocks
    wal:
      path: ${DATA}/tempo/wal
EOF

w "${CFG}/mimir.yaml" <<EOF
target: all                   # monolithic: every component in one process
multitenancy_enabled: true
server:
  http_listen_address: 127.0.0.1
  http_listen_port: 9009
  grpc_listen_address: 127.0.0.1
  grpc_listen_port: 9095
  log_level: warn
common:
  storage:
    backend: filesystem
    filesystem:
      dir: ${DATA}/mimir/blocks
blocks_storage:
  backend: filesystem
  filesystem:
    dir: ${DATA}/mimir/blocks
  tsdb:
    dir: ${DATA}/mimir/tsdb
  bucket_store:
    sync_dir: ${DATA}/mimir/sync
ruler_storage:
  backend: filesystem
  filesystem:
    dir: ${DATA}/mimir/ruler
alertmanager_storage:
  backend: filesystem
  filesystem:
    dir: ${DATA}/mimir/alertmanager
compactor:
  data_dir: ${DATA}/mimir/compactor
  sharding_ring:
    instance_addr: 127.0.0.1
# Every component advertises 127.0.0.1. Mimir's internal components find each
# other through rings, and by default each advertises the box's eth0 address —
# but the servers above listen on loopback only, so the query-scheduler cannot
# reach the query-frontend and every read hangs until it times out. In a
# single-process deployment all of these are the same process; pin them all.
ingester:
  ring:
    replication_factor: 1
    instance_addr: 127.0.0.1
    kvstore:
      store: memberlist
distributor:
  ring:
    instance_addr: 127.0.0.1
querier:
  ring:
    instance_addr: 127.0.0.1
# NOTE: the query-frontend's advertised address has no YAML field (it is not in
# frontend.CombinedFrontendConfig). It is passed as -query-frontend.instance-addr
# on the command line instead; see svc_cmd in /usr/local/bin/lgtm.
query_scheduler:
  ring:
    instance_addr: 127.0.0.1
ruler:
  ring:
    instance_addr: 127.0.0.1
store_gateway:
  sharding_ring:
    replication_factor: 1
    instance_addr: 127.0.0.1
memberlist:
  bind_addr: [127.0.0.1]
  join_members: []
limits:
  # Demos replay canned data, which is usually "old". Don't drop it.
  past_grace_period: 0s
  out_of_order_time_window: 12h
EOF

w "${CFG}/otelcol.yaml" <<EOF
# One OTLP front door. It forwards each caller's X-Scope-OrgID downstream, so
# tenancy is chosen by the APP, not by running more collectors.
extensions:
  headers_setter:
    headers:
      - action: upsert
        key: X-Scope-OrgID
        # from_context reads CLIENT METADATA, whose keys are lowercased (gRPC
        # requires it and the HTTP receiver matches). "X-Scope-OrgID" here never
        # matches and every tenant silently collapses into default_value.
        from_context: x-scope-orgid
        default_value: default
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 127.0.0.1:4317
        include_metadata: true    # required for from_context above
      http:
        endpoint: 127.0.0.1:4318
        include_metadata: true
processors:
  batch:
    # THE tenancy gotcha: the batch processor merges across requests and drops
    # per-request client metadata, so headers_setter finds no tenant and
    # everything lands in "default". Keying batches by the tenant header both
    # preserves it and stops one tenant's data being batched with another's.
    metadata_keys: [x-scope-orgid]
    metadata_cardinality_limit: 200
exporters:
  otlphttp/loki:
    logs_endpoint: http://127.0.0.1:3100/otlp/v1/logs
    auth:
      authenticator: headers_setter
  otlphttp/mimir:
    metrics_endpoint: http://127.0.0.1:9009/otlp/v1/metrics
    auth:
      authenticator: headers_setter
  otlp/tempo:
    endpoint: 127.0.0.1:14317
    tls:
      insecure: true
    auth:
      authenticator: headers_setter
service:
  telemetry:
    logs:
      level: warn
  extensions: [headers_setter]
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch]
      exporters: [otlp/tempo]
    metrics:
      receivers: [otlp]
      processors: [batch]
      exporters: [otlphttp/mimir]
    logs:
      receivers: [otlp]
      processors: [batch]
      exporters: [otlphttp/loki]
EOF

# Grafana: datasources are per TENANT, because the tenant is a request header.
# Regenerated on every run from the tenant list, so a new tenant shows up in the
# UI without hand-editing anything.
mkdir -p "${CFG}/grafana/provisioning/datasources" "${CFG}/grafana/provisioning/dashboards"
w "${CFG}/grafana/grafana.ini" <<EOF
[server]
http_addr = 127.0.0.1
http_port = 3000
[paths]
data = ${DATA}/grafana
logs = ${DATA}/grafana/log
plugins = ${DATA}/grafana/plugins
provisioning = ${CFG}/grafana/provisioning
[auth.anonymous]
enabled = true
org_role = Admin
[auth]
disable_login_form = true
[analytics]
reporting_enabled = false
check_for_updates = false
EOF

# --- 3. the control script: this stack is OFF unless you ask for it ----------
cat > /usr/local/bin/lgtm <<'CTRL'
#!/usr/bin/env bash
# Control the megh dev/demo observability stack.
#   lgtm start|stop|status|logs <svc>|tenants|purge <tenant>|datasources
set -uo pipefail
ROOT="${MEGH_LGTM_ROOT:-/mnt/work/state/lgtm}"
BIN="${ROOT}/bin"; CFG="${ROOT}/config"; DATA="${ROOT}/data"; RUN=/tmp/lgtm
mkdir -p "${RUN}"

svc_cmd() {
  case "$1" in
    # -query-frontend.instance-addr has no YAML equivalent, and without it the
    # scheduler advertises eth0 while the server listens on loopback, so every
    # read hangs. Keep it here, next to the binary it belongs to.
    mimir)   echo "${BIN}/mimir --config.file=${CFG}/mimir.yaml -query-frontend.instance-addr=127.0.0.1" ;;
    loki)    echo "${BIN}/loki -config.file=${CFG}/loki.yaml" ;;
    tempo)   echo "${BIN}/tempo -config.file=${CFG}/tempo.yaml" ;;
    otelcol) echo "${BIN}/otelcol-contrib --config=${CFG}/otelcol.yaml" ;;
    grafana) echo "${BIN}/grafana/bin/grafana server --config=${CFG}/grafana/grafana.ini --homepath=${BIN}/grafana" ;;
  esac
}
SVCS="mimir loki tempo otelcol grafana"

running() { [ -f "${RUN}/$1.pid" ] && kill -0 "$(cat "${RUN}/$1.pid")" 2>/dev/null; }

start_one() {
  running "$1" && { echo "  $1 already running"; return; }
  # An orphan from a previous run (pidfile lost, or a kill that did not land)
  # still holds the port, and the new process dies with "address already in
  # use". Clear it before starting rather than reporting a confusing bind error.
  if pgrep -f "${BIN}/$1" >/dev/null 2>&1; then
    echo "  $1: clearing orphaned process"
    pkill -f "${BIN}/$1" 2>/dev/null
    for _ in $(seq 1 10); do pgrep -f "${BIN}/$1" >/dev/null 2>&1 || break; sleep 1; done
    pkill -9 -f "${BIN}/$1" 2>/dev/null
  fi
  # shellcheck disable=SC2046
  nohup $(svc_cmd "$1") >"${RUN}/$1.log" 2>&1 &
  echo $! > "${RUN}/$1.pid"
  echo "  $1 started (pid $(cat "${RUN}/$1.pid"))"
}

stop_one() {
  local p
  if running "$1"; then
    p="$(cat "${RUN}/$1.pid")"
    kill "$p" 2>/dev/null
    # WAIT for it to actually exit. Removing the pidfile immediately and
    # returning lets an immediate restart race a process that still holds the
    # port, which reads as a mystery bind failure.
    for _ in $(seq 1 15); do kill -0 "$p" 2>/dev/null || break; sleep 1; done
    kill -0 "$p" 2>/dev/null && { kill -9 "$p" 2>/dev/null; sleep 1; }
    echo "  $1 stopped"
  fi
  pkill -f "${BIN}/$1" 2>/dev/null   # sweep orphans too
  rm -f "${RUN}/$1.pid"
}

case "${1:-status}" in
  start)
    # Backends first, collector last: it fails its first exports otherwise.
    for s in ${SVCS}; do start_one "$s"; sleep 2; done
    echo "waiting for readiness..."
    for i in $(seq 1 30); do
      ok=1
      curl -fsS http://127.0.0.1:9009/ready  >/dev/null 2>&1 || ok=0
      curl -fsS http://127.0.0.1:3100/ready  >/dev/null 2>&1 || ok=0
      curl -fsS http://127.0.0.1:3200/ready  >/dev/null 2>&1 || ok=0
      curl -fsS http://127.0.0.1:3000/api/health >/dev/null 2>&1 || ok=0
      [ "$ok" = 1 ] && { echo "all services ready"; break; }
      sleep 3
    done
    lgtm status
    ;;
  stop)
    for s in ${SVCS}; do stop_one "$s"; done
    ;;
  status)
    for s in ${SVCS}; do
      if running "$s"; then printf "  %-8s up   (pid %s)\n" "$s" "$(cat "${RUN}/$s.pid")"
      else printf "  %-8s down\n" "$s"; fi
    done
    echo "  OTLP  -> 127.0.0.1:4318 (http) / :4317 (grpc)"
    echo "  UI    -> 127.0.0.1:3000  (megh browse 3000, or http://<box>:3000 on the tailnet)"
    ;;
  logs)  tail -n 40 "${RUN}/${2:-otelcol}.log" ;;
  tenants)
    # Tenants are DIRECTORIES under each backend's per-tenant root. Skip the
    # backends' own bookkeeping (__mimir_cluster, *_cluster_seed.json, work.json).
    echo "tenants with data on disk:"
    { ls -1 "${DATA}/mimir/tsdb" 2>/dev/null; ls -1 "${DATA}/tempo/blocks" 2>/dev/null; } \
      | grep -vE '^__|\.json$' | sort -u | sed 's/^/  /'
    ;;
  purge)
    t="${2:-}"; [ -z "$t" ] && { echo "usage: lgtm purge <tenant>"; exit 2; }
    # A running backend holds its WAL open, and on NFS that makes deletion fail
    # ("Device or resource busy" on .nfsXXXX silly-renames) — and the tenant is
    # recreated the moment it is dropped. Refuse rather than half-delete.
    for s in ${SVCS}; do
      if running "$s"; then
        echo "stack is running ($s). Stop it first:  lgtm stop"
        exit 1
      fi
    done
    read -r -p "delete ALL telemetry for tenant '$t'? [y/N] " a
    [ "$a" = y ] || exit 0
    # Find the tenant's directory wherever each backend put it, rather than
    # hardcoding three layouts that shift between versions.
    found=$(find "${DATA}" -mindepth 2 -maxdepth 4 -type d -name "$t" 2>/dev/null)
    if [ -z "$found" ]; then echo "no data on disk for tenant '$t'"; exit 0; fi
    echo "$found" | sed 's|^|  removing |'
    find "${DATA}" -mindepth 2 -maxdepth 4 -type d -name "$t" -exec rm -rf {} + 2>/dev/null
    left=$(find "${DATA}" -mindepth 2 -maxdepth 4 -type d -name "$t" 2>/dev/null)
    if [ -n "$left" ]; then echo "WARNING: some paths survived:"; echo "$left" | sed 's/^/  /'; exit 1; fi
    echo "purged tenant '$t'"
    ;;
  datasources)
    echo "point an app at this stack with:"
    echo "  export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318"
    echo "  export OTEL_EXPORTER_OTLP_HEADERS=X-Scope-OrgID=<project>"
    ;;
  *) echo "usage: lgtm start|stop|status|logs <svc>|tenants|purge <tenant>|datasources"; exit 2 ;;
esac
CTRL
chmod +x /usr/local/bin/lgtm

# --- 4. Grafana datasources, one set per tenant ------------------------------
# MEGH_LGTM_TENANTS is a comma list; each gets Mimir/Loki/Tempo datasources
# pinned to its X-Scope-OrgID.
tenants="${MEGH_LGTM_TENANTS:-default}"
{
  echo "apiVersion: 1"
  echo "datasources:"
  IFS=',' read -ra ts <<< "${tenants}"
  for t in "${ts[@]}"; do
    t="${t//[[:space:]]/}"; [ -z "$t" ] && continue
    for d in "Mimir:prometheus:http://127.0.0.1:9009/prometheus" \
             "Loki:loki:http://127.0.0.1:3100" \
             "Tempo:tempo:http://127.0.0.1:3200"; do
      name="${d%%:*}"; rest="${d#*:}"; typ="${rest%%:*}"; url="${rest#*:}"
      echo "  - name: ${name} (${t})"
      echo "    type: ${typ}"
      echo "    access: proxy"
      echo "    url: ${url}"
      [ "$t" = "default" ] && [ "$name" = "Mimir" ] && echo "    isDefault: true"
      echo "    jsonData:"
      echo "      httpHeaderName1: X-Scope-OrgID"
      echo "    secureJsonData:"
      echo "      httpHeaderValue1: ${t}"
    done
  done
} > "${CFG}/grafana/provisioning/datasources/megh.yaml"

# --- 5. start + serve --------------------------------------------------------
log "starting stack (tenants: ${tenants})"
/usr/local/bin/lgtm start

if tailscale ip -4 >/dev/null 2>&1; then
  tailscale serve --bg --http=3000 http://127.0.0.1:3000 >/tmp/ts-serve-lgtm.log 2>&1 \
    && log "Grafana served on the tailnet at :3000 (http://<box-name>:3000)" \
    || log "tailscale serve 3000 failed (see /tmp/ts-serve-lgtm.log)"
else
  log "tailscale not up; reach Grafana via 'megh browse 3000'"
fi

log "control it with: lgtm start|stop|status|logs <svc>|tenants|purge <tenant>"
log "point an app at it: OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318"
log "                    OTEL_EXPORTER_OTLP_HEADERS=X-Scope-OrgID=<project>"
