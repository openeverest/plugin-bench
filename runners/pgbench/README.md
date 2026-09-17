# pgbench Runner

The runner connects to PostgreSQL, optionally initializes pgbench tables, and prints native pgbench results. Results are not stored yet; the coordinator will later run this image as a Kubernetes Job.

## Build

From the repository root:

```bash
docker build -t plugin-bench-pgbench:dev runners/pgbench
```

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

## Cleanup

```bash
docker rm -f pgbench-db-test
docker network rm pgbench-test
```
