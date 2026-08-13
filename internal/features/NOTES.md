# internal/features — implementation notes

Lore for the on-demand capability scripts (`megh enable <name>`). Each `*.sh`
here is embedded in the megh binary and piped to a box over SSH, so it must be
self-contained and idempotent. See the root `CLAUDE.md` for how to *use* them.

## Shared rules

- **Bind 127.0.0.1, always.** RunPod's public proxy is open and unauthenticated,
  so a feature that binds `0.0.0.0` puts a service on the public internet. Reach
  them via `tailscale serve` or `megh browse`.
- **Idempotent.** A feature is re-run on every fresh box (local disk is
  ephemeral), so "already installed / already running" must be a cheap no-op.
- **`megh enable` forwards only `MEGH_*` env.** The script is piped to a remote
  `bash -s`, which inherits nothing from the calling shell, so a feature's own
  knobs would otherwise be unreachable. `meghEnv()` in `cmd/enable.go` exports
  the `MEGH_`-prefixed vars ahead of the script and deliberately excludes
  provider credentials. See `CONSTRAINTS.md` C3.

## webterm

The on-screen key bar binds `touchstart` + `mousedown` with `preventDefault()`
(`webterm.sh:271`), deliberately, so a phone's soft keyboard cannot steal focus
from the terminal. It does **not** bind `click`. A test harness that fires a
synthetic `.click()` sees nothing happen and will look like a broken key bar.

xterm.js/css are vendored in `vendor/` and inlined at assembly time by
`features.Script` via `@@...@@` markers, so the page has no CDN dependency.
`vendor_test.go` fails if the embedded bytes drift from `SHA256SUMS`.

## postgres / redis

Native services, because RunPod pods cannot run containers at all (see
`DESIGN.md`). Not a downgrade: PGDG carries the same major the compose files
pin, so this is version parity.

**The binaries are baked into the image; these scripts own the state.** postgres
+ pgvector + redis are ~11 MB, about 1% of the slim image, so the size argument
that keeps Playwright out does not apply to them. The `initdb` onto the volume,
the config and the control scripts stay here because they are state, not tooling.
The apt blocks in both scripts are retrofit paths for boxes from an older image,
like `megh enable webterm`.

The observability stack deliberately went the other way: at ~700 MB it stays out
of the image, and `megh enable lgtm` caches it on the volume instead, which also
preserves the `MEGH_LGTM_*_VERSION` pinning that baking would freeze.

**Ports default to what the repos already expect**, so a project needs no config
change. `diffpp`'s compose maps `5433->5432` and its default `DIFFPP_DB_URL`
follows, so the cluster listens on **5433**. `cachewarden`'s maps `6399->6379`,
so redis listens on **6399**. Changing these defaults breaks that property, which
is the whole point of them.

`pg db add <name>` creates role, database and password all named for the project,
matching what those compose files set in their environment. That is only
acceptable because the cluster binds loopback and RunPod exposes nothing but
port 22.

One cluster, one database per project. Per-database overhead is negligible while
per-cluster memory is not, so projects get isolation without N servers. Redis is
the same idea via numbered databases or a key prefix.

### THE finding: postgres data cannot live on the volume

Measured on a live box. The volume is `nfs4 ... sec=sys` with root squashed:

- `chown` is denied for **every** uid, including root. Only a no-op chown to
  root succeeds. The same `chown` on the container disk works fine, so it is the
  export, not the container.
- A `0777` directory does not rescue it, because `initdb` also chmods to `0700`
  and gets `could not change permissions of directory`.

postgres requires ownership of its data directory, so this is not tunable. The
cluster therefore lives on the box's **local disk** (`/var/lib/megh-pg`) and
durability comes from logical dumps written to the volume, auto-restored into a
fresh cluster on the next box. `TestPostgresDataDefaultsOffTheVolume` exists so
the default cannot drift back, which would look like an improvement.

Redis has no such problem and does keep its data on the volume: it needs write
access, not ownership. Verified: the RDB lands on `/workspace/state/redis` and
survives a restart.

**Dump/restore carries roles separately.** `pg_dump` is per-database and roles
are cluster-global, so a database-only dump restores tables that nothing can log
in to own. `pg dump` writes `_roles.sql` via `pg_dumpall --roles-only` and
`pg restore` applies it first. Validated end to end: create role + db + a pgvector
row, dump, destroy the cluster, re-enable, and the row reads back through the
project's own DSN with the distance still correct.

### Gotchas

- **The Debian package creates its own cluster on the container disk**, which
  fights the managed one for the port. `provision.sh` and the feature both drop
  it (`pg_dropcluster`).
- **Backticks inside an UNQUOTED heredoc execute.** Both features generate their
  control script with `cat > file <<CTRL`, which expands `${VARS}` (the point)
  but also runs command substitution (not the point). A doc comment mentioning
  `` `stop` `` ran `stop` and silently deleted the word from the generated file.
  It is valid bash, so `bash -n` cannot see it;
  `TestNoCommandSubstitutionInUnquotedHeredocs` can. Put anything that is not a
  variable in the quoted heredoc.
- **Do not report a restore as successful without checking.** An early version
  printed "restored roles" unconditionally while the role did not exist, which
  cost real debugging time. Both paths now report what is actually present.
- **Redis uses RDB snapshots, not AOF.** The data dir defaults to the NFS volume,
  where AOF's per-write fsync is the pathological case, and a dev cache does not
  need per-operation durability.
- Neither is reachable from the Mac without an SSH tunnel, by design.

### Unverified

Everything above is reasoned from the package metadata and the repos' compose
files. It has NOT been run on a box yet: three consecutive RunPod launches stalled
with `portMappings: None`, so the live pass is still owed. Specifically unproven:
the NFS `chown`, `initdb` on the volume, postgres's fsync/locking behaviour on
this NFS mount, and the performance gap against local disk.

## lgtm

Grafana + Loki + Tempo + Mimir behind one OpenTelemetry Collector. Everything
(binaries, configs, data) lives on the volume under `/mnt/work/state/lgtm`, so a
box rebuild does not re-download ~1 GB and re-enabling is just re-linking.
Nothing starts at boot; `/usr/local/bin/lgtm` is the control script.

Versions float to each project's latest release rather than being pinned,
because the release asset names embed the version and a stale pin fails closed
on a 404. `MEGH_LGTM_*_VERSION` freezes any component for a demo.

### Four things that only surfaced by running it on a box

**The otelcol `batch` processor silently eats tenancy.** It merges across
requests and drops per-request client metadata, so `headers_setter`'s
`from_context` finds no tenant and every tenant collapses into `default_value`.
The trap is that ingestion returns HTTP 200 and logs nothing, so it looks like
it works until you query a tenant and get nothing. Needs:

```yaml
processors:
  batch:
    metadata_keys: [x-scope-orgid]
```

`from_context` also matches **lowercased** metadata keys (gRPC requires it and
the HTTP receiver matches), so `x-scope-orgid`, never `X-Scope-OrgID`.

**Mimir in a single process still talks to itself over the network.** Every
component advertises the box's eth0 address by default, while the servers bind
loopback, so the query-scheduler cannot reach the query-frontend and every read
hangs until it times out. Pin `instance_addr: 127.0.0.1` on *every* ring
(ingester, distributor, querier, query_scheduler, ruler, compactor,
store_gateway) plus `-query-frontend.instance-addr=127.0.0.1`, which has **no
YAML equivalent** and must be a command-line flag.

**Tempo 3.x removed the top-level `ingester` and `compactor` blocks** that 2.x
accepted. Leaving them in is a hard config parse error, not a warning.

**NFS refuses to delete files a running writer holds open** (`Device or resource
busy` on `.nfsXXXX` silly-renames), and the backend recreates the tenant
directory immediately anyway. `lgtm purge` therefore refuses unless the stack is
stopped, rather than reporting a delete that did not happen. It locates a
tenant's directories with `find -type d -name <tenant>` rather than hardcoding
three per-backend layouts, which is how it catches paths like
`loki/tsdb-shipper-cache/index_20677/<tenant>`.

### NFS install quirks (apply to any feature installing onto the volume)

- `mv` fails with "failed to preserve ownership" — use `install -m 0755`.
- `tar` fails to restore uid/gid — pass `--no-same-owner`.

### Validated on a live box (2026-08-12)

Per-tenant metrics, logs and traces all read back correctly; a cross-tenant
trace read returns nothing; `purge` removes one tenant across all three backends
and leaves the other intact; Grafana provisions a datasource set per tenant.
Versions at the time: Grafana 13.1.3, Loki 3.7.6, Tempo 3.0.2, Mimir 3.1.4,
otelcol-contrib 0.158.0.

Re-verified on image `0cf66ed`: dashboards render per-tenant data in a real
browser with no panel errors, and a query through `/api/ds/query` (the path a
panel actually uses) returns each tenant's own value while
`count(megh_demo_latency_ms)` on the proja datasource sees 1 series, not 2. So
the provisioned `httpHeaderValue1` tenant header survives Grafana's proxy.

Note that OTLP `service.name` arrives in Mimir as the `job` label
(`megh_demo_latency_ms{job="svc-a"}`), which is the standard OTLP-to-Prometheus
translation, not something this config sets.

Re-enabling on a second box took seconds rather than minutes: the binaries were
already on the volume, which is the point of installing there.
