# Valkey runner

The runner benchmarks a writable Valkey endpoint on database 0, including the
primary in a replication deployment. Sharded cluster mode is outside its scope.
It seeds a fresh range of keys, verifies the seed, then measures a workload of
90% GET and 10% SET. It writes operations per second and latency metrics to
`/dev/termination-log`.

Every run uses a unique key prefix. Seeded keys expire after about one hour,
including keys left by interrupted runs. Each measured SET renews that key's
TTL. Traffic passes through the runner's private Unix socket, so the results
include the overhead of forwarding to Valkey.

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
| `VALKEY_USERNAME` | Required | ACL username, such as `default`. |
| `VALKEY_PASSWORD` | Required | ACL password. |
| `VALKEY_TLS` | `true` | Set to `false` for a plaintext endpoint. |
| `VALKEY_TLS_CA` | Required with TLS | PEM CA certificate contents. The runner checks the server certificate and hostname. |
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
