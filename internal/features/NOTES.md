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

**Not yet verified:** dashboards actually rendering in the Grafana UI. Datasource
config can be valid and still query wrong, so load the browser on the next box.
