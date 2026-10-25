# Running SoroVault in production

This guide is for operators. It assumes you can build or pull the `sorovault`
binary and have a PostgreSQL database; it covers topology, backups, upgrades,
refresh tuning and monitoring. The environment-variable reference is in the
[README](../README.md#configuration); every variable named here is read by
`internal/config/config.go`.

SoroVault is a single process. It serves the JSON API and browse UI, and (when
enabled) runs the refresh sweeper in the same process. There is no separate
worker, queue or cache to operate, and the templates and migrations are embedded
in the binary.

## Deployment topology

```
                 TLS
clients ──────────────► reverse proxy ──────► sorovault serve ──────► PostgreSQL
                                                   │
                                                   └────────────────► Stellar RPC
```

A minimal production deployment is:

- one `sorovault serve` process, or a small number of identical replicas behind
  a load balancer;
- one PostgreSQL database the replicas share;
- a TLS-terminating proxy or ingress in front, because the server itself speaks
  plain HTTP on `HTTP_ADDR`;
- an RPC endpoint in `RPC_URL` that serves the network named by
  `NETWORK_PASSPHRASE`.

The process keeps no durable state of its own. Everything the registry knows is
in PostgreSQL. In-memory state — the resolved network label, the interface cache
and the last sweep summary — is rebuilt on restart; the sweep summary is per
replica.

### One network per process

Writes and refresh sweeps use the network the process resolves from `RPC_URL`
on first use. Records are namespaced by that network, so the same contract ID on
testnet and mainnet are separate rows rather than a conflict. `NETWORK_PASSPHRASE`
is a guard, not a label: if the endpoint reports a different passphrase, work
fails with `registry.ErrNetworkMismatch` and the API returns **503** instead of
silently filing records under the wrong network.

To serve two networks, run one deployment per network. They may share a
database because every key includes the network, but each process only writes
and sweeps its own network. Reads that take `?network=` can select another
network from a shared database. Note that `/api/contracts` and `/api/upgrades`
span all networks when `network` is omitted, `/api/stats` always totals all
networks, and single-contract endpoints default to the instance's network; pass
`network` explicitly if a shared database is in use.

Do not point two processes with different `NETWORK_PASSPHRASE` values at the
same network expecting them to be interchangeable: one of them will refuse to
work, which is the intended behaviour.

### Replicas and the refresher

Every `serve` process with `REFRESH_INTERVAL` greater than zero runs its own
sweep. There is no leader election and no cross-replica lock around
refreshing. With more than one replica:

- set `REFRESH_INTERVAL=0` on all but one, or run a single dedicated refresher
  deployment; otherwise each replica makes the same RPC calls and posts the
  same webhook messages;
- expect `GET /api/stats` to report a different `last_refresh` on each replica
  if more than one is sweeping.

The Helm chart defaults to `replicaCount: 1`. Scaling readers is safe for the
API, but the refresh setting is process-wide and has no per-replica override
beyond the environment, so arrange the split deliberately.

### TLS and the proxy

The HTTP server does not terminate TLS. Put it behind the proxy or ingress you
already operate. Two details matter:

- **Preserve `Origin` and `Referer`.** The browse UI accepts its registration
  form only when the browser names the same origin. A proxy that strips those
  headers breaks the form with a 403; the JSON API does not use them.
- **Allow for the 60-second request timeout.** Registration can spend two RPC
  round trips. Set the proxy's read/upstream timeout above 60 seconds (for
  example 70s) so the server's JSON timeout response reaches the client instead
  of the proxy cutting the connection first.

There is no authentication in SoroVault. Anyone who can reach the service can
register contracts, catalogue uploaded WASM, attach source URLs and trigger
refreshes. If the deployment is public, enforce authentication and rate limits
at the proxy, and consider allowing only `GET`/`HEAD` to the origin. At minimum,
protect the write routes:

```
POST /api/contracts
POST /api/contracts:batch
POST /api/wasm/{hash}
PUT  /api/contracts/{id}/source
POST /api/contracts/{id}/refresh
```

`POST /api/contracts/{id}/refresh` is a write route in this sense even though it
changes only SoroVault's copy: it causes RPC traffic.

SoroVault sets no cache headers. If the proxy caches, keep TTLs short: a
refresh can change the interface served for the same contract ID.

### PostgreSQL connections

`DATABASE_URL` is the connection string. If your platform mounts secrets as
files, use `DATABASE_URL_FILE` instead; setting both is a startup error. The
file's trailing newline is trimmed, so a Kubernetes Secret written with `echo`
works.

The store uses `pgxpool`, whose `pool_max_conns` parameter can be set in the
connection URL. The default is the larger of 4 and the number of CPUs, per
process, so size it explicitly once you run several replicas:

```
postgres://sorovault:...@db:5432/sorovault?sslmode=verify-full&pool_max_conns=10
```

Keep `replicas × pool_max_conns`, plus the migration job, below PostgreSQL's
`max_connections` with headroom for admin sessions. A single instance with the
default limit is usually fine; the pool is not the registry's concurrency
limit (that is `MAX_CONCURRENT_REGISTRATIONS`).

Use `sslmode=verify-full` (or your platform's equivalent) for a database that
is not on a private loopback network. Do not expose PostgreSQL publicly.

## Database sizing

The registry has three tables:

- `contracts` — one row per `(network, contract_id)`, with the current WASM
  hash, source metadata and refresh timestamps;
- `specs` — one row per stored interface version, keyed by
  `(network, contract_id, wasm_hash)`; this is the table that grows with every
  upgrade and is never pruned automatically;
- `wasm_specs` — catalogued uploaded code, keyed by `(network, wasm_hash)`,
  which may exist before a contract uses it.

Size from the number of stored versions, not the number of contracts. An
interface is JSONB and commonly a few kilobytes to tens of kilobytes; the
checked-in zkvote fixture's ABI is about 9.5 KB. Measure your own data rather
than multiplying a guess:

```sql
SELECT
  pg_size_pretty(pg_database_size(current_database())) AS database,
  pg_size_pretty(pg_total_relation_size('contracts'))  AS contracts,
  pg_size_pretty(pg_total_relation_size('specs'))      AS specs,
  pg_size_pretty(pg_total_relation_size('wasm_specs')) AS wasm_specs;

SELECT c.network,
       count(DISTINCT c.contract_id) AS contracts,
       count(s.id)                   AS versions
FROM contracts c
LEFT JOIN specs s USING (network, contract_id)
GROUP BY c.network
ORDER BY c.network;
```

Storage grows append-only in `specs`; a contract that is upgraded weekly keeps
every old version, because old interfaces stay retrievable by hash. Keep normal
autovacuum settings and monitor table and index bloat. The existing indexes
cover the common paths: `contracts_by_last_refreshed`, `specs_by_contract` and
`contracts_by_wasm_hash`.

## Backup and restore

There are two kinds of backup, and they are not substitutes.

### Physical/logical database backup

Use `pg_dump` (or your provider's continuous backup/WAL archiving) for disaster
recovery. It captures the whole schema, including `wasm_specs` and the
`schema_migrations` version table.

```console
$ pg_dump --format=custom --no-owner \
    --file="sorovault-$(date +%F).dump" "$DATABASE_URL"
```

Restore into a fresh database, then point the service at it:

```console
$ createdb sorovault_restore
$ pg_restore --no-owner --dbname=sorovault_restore sorovault-2026-10-23.dump
$ DATABASE_URL=postgres://.../sorovault_restore sorovault migrate
$ DATABASE_URL=postgres://.../sorovault_restore sorovault doctor
```

`pg_dump` takes a consistent snapshot while the service is running. A restore
does not: stop writers, or restore into a new database and switch the
connection string, so a half-restored registry is never served.

If you need point-in-time recovery, configure WAL archiving in PostgreSQL. That
is a database concern, not a SoroVault setting.

### Registry export

`sorovault export` writes a versioned JSON snapshot of the logical registry:

```console
$ sorovault export registry-2026-10-23.json
$ sorovault import --merge registry-2026-10-23.json
```

It contains every contract and every stored interface version with its
timestamps. It does not contact RPC, so it works during an outage. Import
without `--merge` requires an empty registry; with `--merge` it adds only what
is missing and leaves existing rows alone.

The export is for portability, not full disaster recovery: it does **not**
include `wasm_specs` (uploaded code catalogued without a contract), and it does
not include the `schema_migrations` table. Use `pg_dump` when you need the whole
database. The snapshot format has a version field; an unsupported version is
refused rather than guessed at.

Test a restore on a schedule. A backup that has never been restored is a
hypothesis.

## Migrations and upgrades

Schema changes ship as paired files in `migrations/` and are embedded in the
binary. `sorovault migrate` applies every pending version and is safe to run
against an up-to-date database; golang-migrate takes an advisory lock, so
concurrent invocations serialise. There is no `migrate down` command: rollback
is restore-from-backup, not a CLI operation.

Recommended upgrade order:

1. Read the release notes and verify the release archive or image using the
   checksum, SBOM and attestation described in the
   [README](../README.md#verifying-a-release).
2. Take a database backup (and confirm it completed).
3. Run the new binary's migrations as a one-off job:
   `sorovault migrate`. In Helm this already happens in the `pre-install` and
   `pre-upgrade` hook; in Compose the `migrate` service runs before the API.
4. Roll out the new binary.
5. Run `sorovault doctor` and check `GET /api/stats` and the logs.

The Helm chart's migration Job uses the same image and environment as the
Deployment. If it fails, Helm stops the upgrade rather than starting a new
binary against a schema it does not understand.

A migration that fails partway leaves the database marked **dirty**. `sorovault doctor`
reports this explicitly, with the version at which it stopped. Do not start the
new version and do not re-run blindly: repair the schema or restore the backup,
then bring the service up. The migrator deliberately does not expose a force
command.

Rolling back the binary is only safe if the schema is compatible with it.
The migrations currently shipped are additive, but do not assume that for a
future release: if a migration removes or changes something the old binary
reads, restore the pre-upgrade database and then run the old binary. Keep the
previous image/archive until the new version has served traffic successfully.

The server drains in-flight requests for 15 seconds after SIGTERM and caps any
request at 60 seconds. If you override the Kubernetes
`terminationGracePeriodSeconds`, keep it above 15 seconds.

## Refresh tuning

Automatic refresh is off by default (`REFRESH_INTERVAL=0`). When enabled, it
must be at least `1m`.

`REFRESH_INTERVAL` is both the schedule and the staleness threshold. `serve`
runs one sweep immediately at startup, then on each tick of the interval timer.
The timer is not paused while a sweep runs, so a sweep that lasts longer than
the interval is followed immediately by the next tick rather than by a skipped
one. A sweep selects contracts whose `last_refreshed` is older than the
interval, then spaces their refreshes evenly across that interval.

Staleness is measured against `last_refreshed`, not against when the previous
sweep started. A contract refreshed during a sweep therefore has a
`last_refreshed` newer than the next tick's cutoff and is not selected by the
immediately following sweep; it becomes eligible at the sweep after that. The
practical re-check period is up to about twice `REFRESH_INTERVAL`, and the
sweep summaries can alternate between a full sweep and an empty one. Do not
assume a `1h` interval gives a one-hour detection SLA; halve it if you need
that.

The RPC budget has two numbers:

- **Startup:** the first sweep refreshes every stale contract (on a fresh
  registry, all of them), paced across one interval. That is
  `contracts ÷ REFRESH_INTERVAL` instance reads per second for the first
  interval.
- **Steady state:** because of the staleness check above, the long-run average
  is roughly half that — about `contracts ÷ (2 × REFRESH_INTERVAL)`.

An unchanged contract costs one `getLedgerEntries` call. A changed contract
costs a second call (the code entry) plus a decode. The startup sweep begins as
soon as the process is healthy, so a large registry does not wait for the first
full interval before generating load.

Choose the interval from the RPC endpoint's rate limit and your tolerance for
staleness, not from how often you would like the dashboard to move:

- **Small registry, public RPC:** `6h` or `12h` is usually plenty and keeps
  well inside public limits.
- **Large registry:** budget from the startup rate first. For example, 10,000
  contracts over `1h` averages about 2.8 instance reads per second during that
  first sweep; over `15m` it is about 11 per second. The long-run average is
  about half, but the startup sweep is the burst that will hit a rate limit.
- **Change detection:** a shorter interval only reduces detection latency. The
  webhook and the upgrade feed are driven by sweeps, so if you need faster
  notification, shorten the interval and pay for the extra reads. If you need a
  precise cadence independent of `serve`, run `sorovault refresh --all` from a
  cron job and leave `REFRESH_INTERVAL=0`.

`MAX_CONCURRENT_REGISTRATIONS` bounds how many registrations and refreshes run
at once, across the API, CLI and refresher. The default is 4, the accepted range
is 1–1000. A sweep refreshes one contract at a time, so the sweeper itself uses
one slot; the setting mostly protects peak memory and RPC concurrency when
clients register contracts during a sweep. Each registration holds a module
(up to 2 MiB) and its decoded interface while it works, so raising the limit
raises peak memory. When the budget is exhausted, a request waits two seconds
and is then refused with **503** and `Retry-After: 2`.

`RPC_TIMEOUT` (default `30s`) bounds one contract fetch in the CLI and one
contract within a refresh sweep. It does not bound API requests: registration
and refresh over HTTP are bounded by the server's 60-second request timeout.
Keep
`RPC_TIMEOUT` comfortably below `REFRESH_INTERVAL`; a hung RPC call holds the
sweep at that contract until the timeout expires.

### Webhook delivery

Set `REFRESH_WEBHOOK_URL` to post one JSON message per upgraded contract. The
URL must be absolute `http` or `https`; an invalid URL fails at startup rather
than on the first upgrade.

Delivery is best effort: one POST, a five-second timeout, no retries. A
receiver that is down cannot stall the sweep, but the event is not queued for
later. If you need guaranteed delivery, consume `GET /api/upgrades` (or the Atom
feed) as the source of truth and treat the webhook as a latency optimisation.
Alert on the `refresh webhook failed` log line.

Set `REFRESH_WEBHOOK_SECRET` to sign payloads. The signature is the hex-encoded
HMAC-SHA256 of the raw request body, sent as:

```
X-SoroVault-Signature: sha256=<hex>
```

Verify it over the exact bytes received, in constant time, before parsing the
JSON. Rotating the secret is a coordinated change: SoroVault reads it at
startup, and a receiver that still expects the old value will reject new
messages.

## Monitoring

There is no Prometheus metrics endpoint. Monitor the process, the database, RPC
and the logs; add instrumentation if you need metrics beyond that.

### Liveness and readiness

`GET /healthz` returns `{"status":"ok","version":"..."}` and **does not touch**
PostgreSQL or RPC. It answers "is this process serving?", which is what a
liveness probe should restart on. It is not a readiness check.

There is no readiness endpoint. `sorovault doctor` is the dependency check: it
verifies the database answers, the migrations are current and not dirty, the RPC
endpoint is reachable, and its passphrase matches `NETWORK_PASSPHRASE`. It exits
non-zero if any check fails and supports `--json` for scripts:

```console
$ sorovault doctor
PASS  database    the database answers
PASS  migrations  the database is at version 6, the latest is 6
PASS  rpc         the endpoint is reachable, serving testnet
PASS  passphrase  the endpoint serves the configured network
```

Run it as a pre-deployment gate and on a schedule (a Kubernetes CronJob, for
example), not as a per-request readiness probe: it opens a new database
connection and calls RPC on every invocation. The `rpc` check calls `getNetwork`
only, so a pass does not prove that `getLedgerEntries` is healthy.

### What to alert on

- **`GET /api/stats`** — when refresh is enabled, `last_refresh` describes the
  most recent completed sweep: `checked`, `changed`, `failed`, `busy`,
  `missing` and `completed_at`. Because a full sweep is paced across an
  interval and the next tick sees those contracts as fresh, a full sweep and an
  empty `checked: 0` sweep can alternate; a zero count alone is not a fault.
  Alert when `completed_at` is older than about `2 × REFRESH_INTERVAL` plus a
  margin (the refresher has stopped or is stuck), or when the latest sweep
  reports `failed` or `busy` above zero. The summary only covers the last sweep
  — an empty sweep after a bad one replaces it — so use the logs for failures
  that happened earlier.
- **Sweep log line** — `refresh sweep complete` carries `checked`, `changed`,
  `missing`, `failed` and `took`. A full sweep deliberately takes about one
  interval; alert if it consistently takes longer, if an empty sweep is slow
  (listing contracts is unhealthy), or if a scheduled sweep never appears.
- **`refresh failed` warnings** — per-contract RPC or database failures. A few
  are normal; a sustained rate means the RPC endpoint or database is unhealthy.
  These contracts stay stale and are retried on a later sweep.
- **`contract no longer on the network`** — logged once when a contract is
  first marked missing. This is not a failure; it usually means the instance
  expired. The interface is still served with `missing_since` set.
- **`refresh webhook failed`** — the receiver is down or rejecting the
  signature. Since there are no retries, reconcile from `/api/upgrades`.
- **PostgreSQL** — connection saturation (`pg_stat_activity`), disk space, WAL
  growth, long transactions and autovacuum lag. `specs` is append-only, so
  watch its growth over time.
- **RPC** — latency and error rate outside SoroVault, plus the sweep warnings
  above. Public endpoints rate-limit; a sudden rise in `failed` after a deploy
  is often the refresh interval being too short.

`LOG_LEVEL=info` gives one line per sweep, per upgrade and per failure.
`LOG_LEVEL=debug` adds one line per HTTP request and per unchanged contract,
which is useful while diagnosing but noisy for a large registry.

## A short production checklist

- [ ] `DATABASE_URL` or `DATABASE_URL_FILE` points at a backed-up PostgreSQL;
      TLS verified if the database is not local.
- [ ] `RPC_URL` and `NETWORK_PASSPHRASE` name the same network; `doctor` passes.
- [ ] TLS terminates at a proxy, with `Origin`/`Referer` preserved and a
      timeout above 60 seconds.
- [ ] Write routes are authenticated or restricted at the proxy.
- [ ] `REFRESH_INTERVAL` is either `0` or sized from the contract count and the
      RPC budget; only one replica sweeps.
- [ ] `MAX_CONCURRENT_REGISTRATIONS` reflects available memory and RPC
      concurrency.
- [ ] `REFRESH_WEBHOOK_URL` (if used) is reachable and its receiver verifies
      `X-SoroVault-Signature`.
- [ ] Backups run and a restore has been tested.
- [ ] Alerts cover `doctor`, `failed`/`busy` sweeps, webhook failures, database
      saturation and disk.
