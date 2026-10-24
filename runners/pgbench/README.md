# pgbench Runner

The runner connects to PostgreSQL, optionally initializes pgbench tables, and streams native pgbench results. It also writes structured summary metrics to the Kubernetes termination message file.

## Build

From the repository root:

```bash
docker build -t plugin-bench-pgbench:dev runners/pgbench
```

The Go wrapper is built with Go 1.25, matching the backend and Sysbench runner.
The runtime image remains PostgreSQL 16 on Alpine.

## Disposable PostgreSQL database

```bash
docker network create pgbench-test
docker run -d \
  --name pgbench-db-test \
  --network pgbench-test \
  -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=pgbench \
  postgres:16-alpine

ready=false
for attempt in {1..30}; do
  if docker exec pgbench-db-test pg_isready -U postgres -d pgbench; then
    ready=true
    break
  fi
  sleep 1
done
if [ "$ready" != true ]; then
  echo "PostgreSQL did not become ready"
  exit 1
fi
```

## Configuration

Required connection variables:

```text
PGHOST       PostgreSQL hostname
PGDATABASE   Database name
PGUSER       PostgreSQL user
PGPASSWORD   Password, or use PGPASSFILE
```

Optional connection variables include `PGPORT` (default `5432`), `PGCONNECT_TIMEOUT` (default `10`), `PGSSLMODE`, and `PGSSLROOTCERT`.

For `PGPASSFILE`, mount a password file readable by the container's `postgres` user, with mode `0600`, and supply its container path. When set, `PGPASSWORD` takes precedence.

Benchmark variables:

```text
BENCH_INITIALIZE   Initialize tables; default false
BENCH_DURATION     Duration in seconds; default 60
BENCH_CLIENTS      Concurrent clients; default 1
BENCH_THREADS      pgbench threads; default 1
BENCH_SCALE        Initialization scale; default 1
```

`BENCH_THREADS` cannot exceed `BENCH_CLIENTS`.

## Run

Initialize the disposable database and run a 30-second benchmark. `BENCH_INITIALIZE=true` drops and recreates existing pgbench tables, so use it only with a disposable database:

```bash
docker run --rm \
  --network pgbench-test \
  -e PGHOST=pgbench-db-test \
  -e PGPORT=5432 \
  -e PGDATABASE=pgbench \
  -e PGUSER=postgres \
  -e PGPASSWORD=postgres \
  -e BENCH_INITIALIZE=true \
  -e BENCH_DURATION=30 \
  -e BENCH_CLIENTS=2 \
  -e BENCH_THREADS=2 \
  plugin-bench-pgbench:dev
```

The output includes `tps`, average latency, and failed transactions. Run again without `BENCH_INITIALIZE` to reuse the tables.

The runner passes connection settings through PostgreSQL environment variables and does not put credentials in command-line arguments. It returns a non-zero exit code for invalid configuration, connection failures, initialization failures, or benchmark failures.

## Structured metrics

After a successful benchmark, the runner extracts TPS (without initial connection time) and average latency from pgbench's final summary. It writes a versioned JSON record of at most 4 KiB to `/dev/termination-log`:

```json
{"version":1,"metrics":[{"name":"tps","unit":"tx/s","value":1234.5},{"name":"latency_avg","unit":"ms","value":3.2}]}
```

Kubernetes supplies this file; use `terminationMessagePolicy: File` on the runner container. Raw stdout and stderr continue to appear in the container logs. Initialization output is excluded from metrics, and subprocesses use `LC_ALL=C` for stable summary formatting.

When metrics are missing, malformed, or cannot be written, the runner emits a generic warning and preserves the benchmark exit status. A plain Docker invocation without a writable termination file therefore still works, but reports metrics as unavailable. Failed or cancelled benchmarks do not write a success record.

The backend reads the current runner container's termination message before
cleanup and returns decoded metrics separately from logs. See the shared
[request and runner result contracts](../../docs/runner-result-contract.md).

### Inspect metrics with Docker

Docker does not supply Kubernetes' termination file. Against the disposable
database above, mount a fresh writable file to inspect it. This example enables
initialization and therefore recreates pgbench tables:

```bash
metrics_dir=$(mktemp -d)
trap 'rm -f "$metrics_dir/result.json"; rmdir "$metrics_dir"' EXIT
touch "$metrics_dir/result.json"
# The parent directory is private; allow the container's postgres user to write
# the single mounted file without assuming its numeric UID matches the host.
chmod 666 "$metrics_dir/result.json"

docker run --rm \
  --network pgbench-test \
  --mount "type=bind,src=$metrics_dir/result.json,dst=/dev/termination-log" \
  -e PGHOST=pgbench-db-test -e PGDATABASE=pgbench -e PGUSER=postgres \
  -e PGPASSWORD=postgres -e BENCH_INITIALIZE=true -e BENCH_DURATION=5 \
  plugin-bench-pgbench:dev

cat "$metrics_dir/result.json"
```

Use a fresh file for each invocation so a failed run cannot expose a previous
run's success record. From the repository root, the automated image check is:

```bash
bash .github/scripts/test-runner.sh plugin-bench-pgbench:dev
```

It requires Docker and Python 3, creates its own disposable PostgreSQL database,
checks initialization and table reuse, compares metrics with the same run's log
summary, and verifies authentication failure and cleanup.

## Cleanup

```bash
docker rm -f pgbench-db-test
docker network rm pgbench-test
```
