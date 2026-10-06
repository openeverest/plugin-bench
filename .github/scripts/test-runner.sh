#!/usr/bin/env bash
set -euo pipefail

runner_image=${1:?Usage: test-runner.sh IMAGE}
test_dir=$(mktemp -d)
network_name="pgbench-test-${test_dir##*/}"
network_id=""
database_id=""

cleanup() {
  if [[ -n "$database_id" ]]; then
    docker rm -fv "$database_id" > /dev/null || true
  fi
  if [[ -n "$network_id" ]]; then
    docker network rm "$network_id" > /dev/null || true
  fi
  rm -f "$test_dir/benchmark.log" "$test_dir/failure.log"
  rmdir "$test_dir"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

network_id=$(docker network create "$network_name")
database_id=$(docker run -d --network "$network_id" --network-alias pgbench-db \
  -e POSTGRES_PASSWORD=ci-password -e POSTGRES_DB=pgbench postgres:16-alpine)

ready=false
for attempt in {1..30}; do
  if docker exec "$database_id" pg_isready -h 127.0.0.1 -U postgres -d pgbench; then
    ready=true
    break
  fi
  sleep 1
done
if [[ "$ready" != true ]]; then
  docker logs "$database_id"
  exit 1
fi

runner=(docker run --rm --network "$network_id"
  -e PGHOST=pgbench-db -e PGDATABASE=pgbench -e PGUSER=postgres
  -e PGCONNECT_TIMEOUT=3 -e BENCH_DURATION=2)
for initialize in true false; do
  if ! "${runner[@]}" -e PGPASSWORD=ci-password -e BENCH_INITIALIZE="$initialize" \
    "$runner_image" > "$test_dir/benchmark.log" 2>&1; then
    cat "$test_dir/benchmark.log"
    exit 1
  fi
  cat "$test_dir/benchmark.log"
  grep -Eq '^tps = [0-9]' "$test_dir/benchmark.log"
  grep -Eq '^latency average = [0-9]' "$test_dir/benchmark.log"
done
if "${runner[@]}" -e PGPASSWORD=wrong-ci-password \
  "$runner_image" > "$test_dir/failure.log" 2>&1; then
  echo "Expected authentication failure"
  exit 1
fi
grep -q 'password authentication failed' "$test_dir/failure.log"
if grep -Fq 'wrong-ci-password' "$test_dir/failure.log"; then
  echo "Failure output exposed the password"
  exit 1
fi
