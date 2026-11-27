# Valkey runner

The runner benchmarks a writable Valkey endpoint on database 0, including the
primary in a replication deployment. Sharded cluster mode is outside its scope.
It seeds a fresh range of keys, verifies the seed, then measures a workload of
90% GET and 10% SET. It writes operations per second and latency metrics to
`/dev/termination-log`.

Every run uses a unique key prefix. On success or failure, the runner tries
for up to 10 seconds to remove that run's keys with `UNLINK`. If cleanup cannot
finish, remaining keys expire about an hour after their last write.

Use a disposable target with enough memory for the benchmark values and Valkey's
per-key overhead. Under an `allkeys-*` policy, benchmark writes can evict existing
keys. Under `noeviction`, writes can fail when Valkey reaches its memory limit.

Before seeding, the runner connects to Valkey and checks the credentials with
`AUTH`, checks for cluster mode with `INFO cluster`, and confirms the endpoint
is a primary with `ROLE`. For replication deployments, use the primary service
so Memtier's connections reach the primary as well.

The integration test uses this ACL rule for the benchmark user.

```text
~plugin-bench:* +info +role +select +set +setex +get +exists +unlink
```

The key pattern limits key access to the benchmark prefix. Memtier uses `SELECT`
for database selection and the read/write commands for the workload. The runner
uses `EXISTS` to verify seeded keys and `UNLINK` to clean them up.

With TLS enabled, this preflight verifies the CA and hostname. Memtier
then connects directly for seeding and measurement. Memtier 2.6.1 verifies the
CA on its TLS connections. Hostname verification happens during preflight only.
The credentials are visible in Memtier's process arguments while it runs.

## Build

From the repository root:

```bash
docker build -t plugin-bench-valkey:dev runners/valkey
docker run --rm --entrypoint memtier_benchmark plugin-bench-valkey:dev --version
```

The image compiles the Go wrapper for the target architecture and reuses the
upstream Memtier 2.6.1 image pinned by digest for Linux AMD64 and ARM64. The
wrapper runs as UID and GID `10001` and uses `/tmp` for its private files.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `VALKEY_HOST` | Required | Writable endpoint hostname or IP, without a port. |
| `VALKEY_PORT` | Required | TCP port. |
| `VALKEY_USERNAME` | Required | ACL username without a colon, such as `default`. |
| `VALKEY_PASSWORD` | Required | ACL password. |
| `VALKEY_TLS` | `true` | Set to `false` for a plaintext endpoint. |
| `VALKEY_TLS_CA` | Required with TLS | PEM CA certificate contents used by preflight and Memtier. |
| `VALKEY_TLS_CERT` | Required for mTLS | PEM client certificate contents. Requires TLS and `VALKEY_TLS_KEY`. |
| `VALKEY_TLS_KEY` | Required for mTLS | PEM client private key contents matching `VALKEY_TLS_CERT`. |
| `BENCH_DURATION` | `30` | Measurement duration in seconds (1–300). |
| `BENCH_THREADS` | `2` | Memtier worker threads. |
| `BENCH_CLIENTS_PER_THREAD` | `10` | Connections per thread. Threads times clients cannot exceed 128. |
| `BENCH_KEY_COUNT` | `10000` | Keys to seed (1–100,000). |
| `BENCH_VALUE_SIZE` | `256` | Bytes per value (1–16,384). Key count times value size cannot exceed 64 MiB. |

The runner has a ten-minute deadline that covers seeding, verification, and
measurement.

## Local run

Run this in Bash with a disposable TLS-enabled Valkey instance on an existing
Docker network. Replace the network, hostname, port, username, and CA path
with those of your instance. The hostname must match the server certificate.

```bash
valkey_metrics_dir=$(mktemp -d)
touch "$valkey_metrics_dir/metrics.json"
chmod a+w "$valkey_metrics_dir/metrics.json"

export VALKEY_TLS_CA="$(cat ./valkey-ca.pem)"
read -rsp 'Valkey password: ' VALKEY_PASSWORD
printf '\n'
export VALKEY_PASSWORD

docker run --rm --network valkey-demo \
  -e VALKEY_HOST=valkey-demo-db -e VALKEY_PORT=6379 -e VALKEY_USERNAME=default \
  -e VALKEY_PASSWORD -e VALKEY_TLS_CA -e BENCH_DURATION=5 -e BENCH_KEY_COUNT=100 \
  --mount "type=bind,src=$valkey_metrics_dir/metrics.json,dst=/dev/termination-log" \
  plugin-bench-valkey:dev

cat "$valkey_metrics_dir/metrics.json"
unset VALKEY_PASSWORD VALKEY_TLS_CA
rm "$valkey_metrics_dir/metrics.json"
rmdir "$valkey_metrics_dir"
```
