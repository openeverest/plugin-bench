# Sysbench Runner

The runner executes MySQL benchmarks with sysbench's bundled `oltp_read_write`
workload. Its Go entrypoint validates environment settings, optionally prepares
tables, streams native logs, and writes structured TPS and average latency
metrics to Kubernetes' termination message file.

## Build

From the repository root:

```bash
docker build -t plugin-bench-sysbench:dev runners/sysbench
```

The image uses a Go 1.25 build stage and a Debian 12 (`bookworm-slim`) runtime.
It installs [Debian's sysbench 1.0.20 package](https://packages.debian.org/bookworm/sysbench)
(`1.0.20+ds-5`), including its Lua workloads and MySQL connector. The dynamically
linked sysbench executable uses the `libmariadb3` client library; only the Go
wrapper is compiled with `CGO_ENABLED=0`.

The build checks the sysbench version, compiled MySQL driver, and availability of
the `oltp_read_write` workload. It prints the exact installed versions of sysbench
and `libmariadb3`. To inspect those versions in an image:

```bash
docker run --rm --entrypoint dpkg-query plugin-bench-sysbench:dev \
  -W '-f=${Package}=${Version}\n' sysbench libmariadb3
```

The initial Linux/AMD64 image build verified `sysbench=1.0.20+ds-5` with
`libmariadb3=1:10.11.19-0+deb12u1`. Client library versions can change as Debian
publishes security updates; inspect each built image for its resolved versions.

The default entrypoint is `/usr/local/bin/sysbench-runner`. It runs as user
`runner` (UID/GID `10001`), with `/tmp` available for private connection files.
The image contains the benchmark client and wrapper; the MySQL server runs
separately. No database credentials are supplied during the build.

Check the runtime user, tool version, and workload without connecting to MySQL:

```bash
docker run --rm --entrypoint id plugin-bench-sysbench:dev
docker run --rm --entrypoint sysbench plugin-bench-sysbench:dev --version
docker run --rm --entrypoint sysbench plugin-bench-sysbench:dev oltp_read_write help
```

The Dockerfile uses architecture-specific base images selected by Docker and does
not hardcode a Go target architecture. Local packaging checks cover Linux/AMD64;
ARM64 verification belongs to the later delivery integration. Database execution
is verified separately against disposable MySQL.

## Configuration

| Environment variable | Default | Meaning |
| --- | --- | --- |
| `MYSQL_HOST` | Required | One hostname or IP address; comma-separated hosts are rejected. |
| `MYSQL_PORT` | `3306` | TCP port, from 1 to 65535. |
| `MYSQL_DATABASE` | Required | Existing benchmark database. |
| `MYSQL_USER` | Required | Account permitted to run the workload and, when requested, create tables. |
| `MYSQL_PASSWORD` | Required | Nonempty password, preserved without trimming. |
| `BENCH_DURATION` | `60` | Measured benchmark duration in seconds. |
| `BENCH_THREADS` | `1` | Measured workers/connections; preparation uses one worker. |
| `BENCH_TABLES` | `1` | Number of `sbtest*` tables. |
| `BENCH_TABLE_SIZE` | `10000` | Rows per table used for preparation and query generation. |
| `BENCH_INITIALIZE` | `false` | Exactly `true` to prepare before running, or `false` to reuse tables. |

Unset or empty optional settings receive defaults. Numeric benchmark settings
must be integers from 1 to 2147483647. Host, database, and user cannot be blank.
Only the bundled `oltp_read_write` workload is supported, with sysbench's default
transaction mix. Custom scripts and additional workloads are outside this contract.

The database and account must already exist. Preparation is not idempotent:
existing table names cause an error, and the measured phase is then skipped.
The runner never automatically drops tables, including after a failed or
cancelled run; preparation can leave partial data. When reusing tables, supply
the matching table count and size because these also control generated queries.
`BENCH_DURATION` covers the measured phase; preparation and connection setup add
wall-clock time. The caller supplies the overall deadline.

### Credentials

The wrapper receives credentials through environment variables, then writes a
private sysbench configuration file in a mode-0700 temporary directory. The file
has mode 0600, and only its path is passed to sysbench. `MYSQL_PASSWORD` is removed
from the child's environment. The file is removed on normal return, including
handled cancellation. Docker/Kubernetes administrators can still inspect the
wrapper's environment.

Sysbench 1.0.20 has no quote escaping in configuration files. The wrapper chooses
single or double quotes when possible. A value containing both quote types is
accepted unquoted only if it has no whitespace or commas and does not begin with
a quote or `#`. NUL, CR, LF, unsupported combinations, and encoded configuration
lines longer than 254 bytes including the newline are rejected before execution.
Errors name the setting without printing its value.

## Local MySQL example

Run these commands in Bash from the repository root after building the image.
They use a disposable database and dummy credentials stored outside the checkout
in private environment files. The names below are reserved for this example;
remove an earlier example before starting another.

```bash
sysbench_demo_dir=$(mktemp -d /tmp/plugin-bench-sysbench-demo.XXXXXX)
chmod 700 "$sysbench_demo_dir"
printf '%s\n' \
  'MYSQL_ROOT_PASSWORD=demo-root-password' \
  'MYSQL_DATABASE=sbtest' \
  'MYSQL_USER=sbuser' \
  'MYSQL_PASSWORD=demo-benchmark-password' > "$sysbench_demo_dir/mysql.env"
printf '%s\n' \
  'MYSQL_HOST=sysbench-demo-db' \
  'MYSQL_PORT=3306' \
  'MYSQL_DATABASE=sbtest' \
  'MYSQL_USER=sbuser' \
  'MYSQL_PASSWORD=demo-benchmark-password' \
  'BENCH_DURATION=5' \
  'BENCH_THREADS=2' \
  'BENCH_TABLES=1' \
  'BENCH_TABLE_SIZE=1000' > "$sysbench_demo_dir/runner.env"
chmod 600 "$sysbench_demo_dir/mysql.env" "$sysbench_demo_dir/runner.env"

docker network create sysbench-demo
docker run -d --name sysbench-demo-db --network sysbench-demo \
  --env-file "$sysbench_demo_dir/mysql.env" mysql:8.4
```

Wait for an authenticated TCP query, with a bounded retry loop. Continue only
after this block succeeds; if it fails, inspect `docker logs sysbench-demo-db`.

```bash
timeout 120s bash -c '
  until docker exec sysbench-demo-db sh -ec '\''
    export MYSQL_PWD="$MYSQL_PASSWORD"
    exec mysql --protocol=TCP --host=127.0.0.1 --connect-timeout=3 \
      --user="$MYSQL_USER" --database="$MYSQL_DATABASE" --execute="SELECT 1"
  '\''; do sleep 1; done
'
```

Provide a writable termination file for the non-root runner. Its host directory
remains private; Docker mounts only this file into the container.

```bash
touch "$sysbench_demo_dir/metrics.json"
chmod 666 "$sysbench_demo_dir/metrics.json"
docker run --rm --name sysbench-demo-runner --network sysbench-demo \
  --env-file "$sysbench_demo_dir/runner.env" -e BENCH_INITIALIZE=true \
  --mount "type=bind,src=$sysbench_demo_dir/metrics.json,dst=/dev/termination-log" \
  plugin-bench-sysbench:dev
python3 -m json.tool "$sysbench_demo_dir/metrics.json"
```

Reuse the tables with the same table settings. Clear the host metrics file before
each invocation so an unsuccessful run cannot appear to have a previous result.

```bash
truncate -s 0 "$sysbench_demo_dir/metrics.json"
docker run --rm --name sysbench-demo-runner --network sysbench-demo \
  --env-file "$sysbench_demo_dir/runner.env" -e BENCH_INITIALIZE=false \
  --mount "type=bind,src=$sysbench_demo_dir/metrics.json,dst=/dev/termination-log" \
  plugin-bench-sysbench:dev
python3 -m json.tool "$sysbench_demo_dir/metrics.json"
```

To explicitly remove the benchmark tables while retaining the database, invoke
sysbench's `cleanup` command. This example configuration encoding is intended
for the simple dummy values above; the wrapper handles general credential
validation during normal runs.

```bash
docker run --rm --name sysbench-demo-runner --network sysbench-demo \
  --env-file "$sysbench_demo_dir/runner.env" --entrypoint sh \
  plugin-bench-sysbench:dev -ec '
    umask 077
    config=$(mktemp /tmp/sysbench-cleanup.XXXXXX)
    trap '\''rm -f "$config"'\'' EXIT
    printf "mysql-host=%s\nmysql-port=%s\nmysql-db=%s\nmysql-user=%s\nmysql-password=%s\n" \
      "$MYSQL_HOST" "$MYSQL_PORT" "$MYSQL_DATABASE" "$MYSQL_USER" "$MYSQL_PASSWORD" > "$config"
    unset MYSQL_PASSWORD
    sysbench --config-file="$config" --db-driver=mysql --tables="$BENCH_TABLES" \
      oltp_read_write cleanup
  '
```

When finished, remove the example containers, database volumes, network, and
temporary files. Database removal also discards any remaining benchmark data.

```bash
# Run the first command only if an interrupted invocation left a runner behind.
docker rm -f sysbench-demo-runner
docker rm -fv sysbench-demo-db
docker network rm sysbench-demo
rm -f -- "$sysbench_demo_dir/mysql.env" "$sysbench_demo_dir/runner.env" \
  "$sysbench_demo_dir/metrics.json"
rmdir -- "$sysbench_demo_dir"
unset sysbench_demo_dir
```

## Results and process lifecycle

Native stdout and stderr are streamed unchanged. Only measured stdout is observed
for the final summary; preparation output is excluded. A successful, uncancelled
run writes a version-1 record containing `tps` in `tx/s` and `latency_avg` in `ms`
to the existing `/dev/termination-log` file:

```json
{"version":1,"metrics":[{"name":"tps","unit":"tx/s","value":107.41},{"name":"latency_avg","unit":"ms","value":18.61}]}
```

The record must fit within 4096 bytes. In Kubernetes, use
`terminationMessagePath: /dev/termination-log` and `terminationMessagePolicy: File`.
Kubernetes exposes its contents through the terminated container's status.
See the shared [runner result contract](../../docs/runner-result-contract.md#runner-transport-and-wire-format).

Missing, malformed, or duplicate summary metrics, or an unavailable/unwritable
termination file, produce `runner warning: structured metrics unavailable` on
stderr while preserving benchmark success. The wrapper does not create a missing
termination file; standalone Docker runs need the explicit mount above. Failed
or cancelled runs do not write a success record.

The wrapper exits 0 when the benchmark succeeds, preserves a positive sysbench
failure exit code, and otherwise exits 1 for configuration errors, missing tools,
or cancellation. SIGINT/SIGTERM cancels the active child with SIGTERM; after five
seconds it is forcibly stopped if necessary. Cancellation during preparation
prevents the measured phase. Container stop deadlines should leave time for this
shutdown and credential-file cleanup.

## Image verification

The `Sysbench Runner` CI job runs Go vet, race tests, an image build, and the
MySQL smoke script below. To run the Go checks locally:

```bash
go -C runners/sysbench vet ./...
go -C runners/sysbench test -race ./...
```

From the repository root, with the runner image built:

```bash
bash .github/scripts/test-sysbench-runner.sh plugin-bench-sysbench:dev
```

The script requires Bash, Docker, Python 3, and GNU `timeout`. It starts a
disposable MySQL 8.4 container on a private Docker network, waits for an
authenticated query, and checks:

- Preparation, table population, and reuse with initialization disabled.
- Termination metrics matching TPS and average latency in the same run's logs.
- Preparation against existing tables failing while preserving existing data.
- Authentication, invalid configuration, unreachable endpoint, and missing-table
  failures producing no success metrics.
- Passwords absent from runner logs and metrics.

Each runner invocation has a 45-second deadline, and database readiness has a
120-second deadline. Cleanup traps remove the test containers, database's
anonymous volumes, Docker network, and temporary files on success or failure.

Preparation uses one worker because [sysbench 1.0.20's parallel command path](https://github.com/akopytov/sysbench/blob/1.0.20/src/sb_lua.c#L1431-L1470)
can discard worker failures and return success. `BENCH_THREADS` controls the
measured run; preparation errors stop execution before benchmarking.

Verified locally over TCP on Linux/AMD64 with MySQL `8.4.11`, sysbench
`1.0.20+ds-5`, and `libmariadb3=1:10.11.19-0+deb12u1`. The existing credential
parser tests were also run inside the image to check supported quotes,
whitespace, backslashes, and the configuration line-length boundary.
