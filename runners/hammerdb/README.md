# HammerDB runner

Standalone HammerDB **6.1.1** TPROC-C runner for PostgreSQL and MySQL. One image
contains both database clients and a Go wrapper that prepares Tcl scripts,
optionally builds the schema, runs the timed workload and collects metrics.
Plugin API/UI integration is a separate task; this image is usable directly.

## Build and check

From the repository root:

```bash
docker build -t plugin-bench-hammerdb:dev runners/hammerdb
bash .github/scripts/check-hammerdb-image.sh plugin-bench-hammerdb:dev
bash .github/scripts/test-hammerdb-runner.sh plugin-bench-hammerdb:dev
```

The integration script creates disposable PostgreSQL 17 and MySQL 8.4 servers,
checks preparation/reuse, metrics, failures, redaction and cancellation, then
removes its containers, volumes, network and private files. Pass `postgresql` or
`mysql` as its second argument to test one engine. Initialization has an
eight-minute limit, reuse three minutes, expected failures one minute, and
cancellation a 20-second exit limit. CI runs both scripts.

Build both platforms with a compatible Buildx builder/image store:

```bash
docker buildx build --platform linux/amd64,linux/arm64 --load \
  -t plugin-bench-hammerdb:dev runners/hammerdb
bash .github/scripts/check-hammerdb-image.sh plugin-bench-hammerdb:dev linux/amd64
bash .github/scripts/check-hammerdb-image.sh plugin-bench-hammerdb:dev linux/arm64
```

Foreign-platform execution requires emulation. The Dockerfile
pins upstream base/MySQL image indexes, installs PostgreSQL `libpq` 17 and keeps
upstream licenses. HammerDB uses Tcl 9, `Pgtcl` and `mysqltcl`. The wrapper runs as
UID/GID 10001 from `/home/hammerdb`. `/tmp` must be writable and permit executable
mappings because bundled Tcl libraries are extracted there.

## Environment

| Variable | Default / validation |
| --- | --- |
| `DB_TYPE` | Required: `postgresql` or `mysql` |
| `DB_HOST` | Required single IP address or DNS hostname, without a port |
| `DB_PORT` | 5432 for PostgreSQL; 3306 for MySQL; range 1–65535 |
| `DB_NAME` | Required existing disposable database |
| `DB_USER` | Required existing account with database privileges |
| `DB_PASSWORD` | Required nonempty valid UTF-8 |
| `BENCH_WORKLOAD` | `tproc-c`; no other workloads supported |
| `BENCH_WAREHOUSES` | 1; positive integer |
| `BENCH_VIRTUAL_USERS` | 1; positive integer, at most the warehouse count |
| `BENCH_RAMPUP_MINUTES` | 0; nonnegative whole minutes |
| `BENCH_DURATION_MINUTES` | 1; positive whole minutes |
| `BENCH_INITIALIZE` | `false`; `true` or `false` |
| `PGSSLMODE` | PostgreSQL: `prefer`; accepts `disable`, `prefer`, `require` |
| `MYSQL_SSL` | MySQL: `true`; `true` or `false` |

Database and user names accept ASCII letters, digits, underscores, hyphens and
dots. PostgreSQL names are limited to 63 bytes; MySQL database names to 64 and
usernames to 32. Numeric settings must fit the validated bounds and the combined
minute duration must fit Go's `time.Duration`. Passwords are Tcl-encoded for
HammerDB settings while preflight uses the original value.

## Database setup and execution

Provision the database and account before running the image. For PostgreSQL,
the account must own the database and its default schema must resolve to
`public`; superuser, `CREATEDB` and `CREATEROLE` are unnecessary. For MySQL,
grant privileges within the benchmark database; the verified setup uses
`ALL PRIVILEGES ON <database>.*` without global or account-management grants.

`BENCH_INITIALIZE=true` prepares an empty database using one schema worker.
With initialization off, the runner requires an existing TPROC-C schema with
matching warehouse count and identifier range. It uses the supplied account for
all phases and creates no database or account. Existing data and partial objects
from failed preparation remain; cleanup never drops the dataset automatically.

Measurement uses the configured virtual users plus one monitor connection.
Allow time for startup and schema preparation in addition to
`(rampupMinutes + durationMinutes) * 60` seconds. A zero CLI exit code alone is
insufficient: the wrapper checks worker states and private completion records.
Failed preparation prevents measurement.

Preflight uses a 10-second connection timeout for both engines.

MySQL requests TLS by default and checks the negotiated cipher; its integration
tests use MySQL 8.4's default authentication. PostgreSQL passes `PGSSLMODE` to
libpq; integration tests use `disable`. These settings provide encryption controls;
CA provisioning, server identity verification and mutual TLS are outside this
runner's current interface.

## Run directly and collect metrics

The example assumes an existing PostgreSQL database/account reachable as
`database-host` on `YOUR_DATABASE_NETWORK`. Replace those names and settings.
The file mount represents Kubernetes' termination-message file:

```bash
results_dir=$(mktemp -d)
runner_name="hammerdb-example-${results_dir##*/}"
touch "$results_dir/metrics.json"
chmod 666 "$results_dir/metrics.json"
read -rs -p 'Database password: ' DB_PASSWORD; echo
export DB_PASSWORD

timeout -k 10s 10m docker run --rm --name "$runner_name" \
  --network YOUR_DATABASE_NETWORK --tmpfs /tmp:rw,exec,mode=1777 \
  --mount "type=bind,src=$results_dir/metrics.json,dst=/dev/termination-log" \
  -e DB_TYPE=postgresql -e DB_HOST=database-host -e DB_NAME=hammerdb_test \
  -e DB_USER=benchmark -e DB_PASSWORD -e PGSSLMODE=prefer \
  -e BENCH_INITIALIZE=true -e BENCH_WAREHOUSES=1 -e BENCH_VIRTUAL_USERS=1 \
  -e BENCH_RAMPUP_MINUTES=0 -e BENCH_DURATION_MINUTES=1 \
  plugin-bench-hammerdb:dev
cat "$results_dir/metrics.json"

# Remove a surviving container if the Docker client timed out, then local files.
docker rm -f "$runner_name" 2>/dev/null || true
rm -rf -- "$results_dir"
unset DB_PASSWORD
```

Only the metrics file is world-writable so UID 10001 can write it; its parent
remains private. For MySQL, use `DB_TYPE=mysql` and `MYSQL_SSL=true` instead of
`PGSSLMODE`, with the corresponding host, database and account. To reuse prepared
data, set `BENCH_INITIALIZE=false`. Database cleanup is a separate explicit action
by the database owner after results are collected.

## Logs, credentials and results

Generated scripts, completion records and HammerDB state live in a private
0700 directory with 0600 script files. The child environment omits `DB_PASSWORD`;
stdout/stderr are redacted before reaching container logs. Temporary state is
removed on success, failure and handled cancellation. SIGTERM terminates the
CLI process group with a bounded shutdown wait.

Successful measurement writes a version-1 JSON record to `/dev/termination-log`
(maximum 4096 bytes). The runner never creates that file itself.

| Metric | Unit |
| --- | --- |
| `nopm` | `orders/min` |
| `tpm` | `tx/min` |
| `latency_new_order_avg`, `latency_payment_avg`, `latency_delivery_avg` | `ms` |
| `latency_order_status_avg`, `latency_stock_level_avg` | `ms` |

TPM is engine-specific and is not equivalent to pgbench/Sysbench TPS. Optional
latencies come from the completed job's timing data; missing timing preserves
throughput. Missing/invalid metrics produce a warning without failing a verified
successful workload. Failed or cancelled workloads write no success metrics.

Releases build AMD64/ARM64 OCI archives, test the AMD64 image and publish
`ghcr.io/openeverest/plugin-bench-hammerdb:<version>`. A maintainer must make the
new GHCR package public for the first release's anonymous-access check to pass.
See the [shared result contract](../../docs/runner-result-contract.md) and
[verification notes](verification.md) for details.
