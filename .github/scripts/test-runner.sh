#!/usr/bin/env bash
set -euo pipefail

runner_image=${1:?Usage: test-runner.sh IMAGE}
command -v python3 > /dev/null
test_dir=$(mktemp -d)
network_name="pgbench-test-${test_dir##*/}"
runner_name="pgbench-runner-${test_dir##*/}"
network_id=""
database_id=""

cleanup() {
  # Also remove a runner left behind if its Docker client was interrupted.
  docker rm -f "$runner_name" > /dev/null 2>&1 || true
  if [[ -n "$database_id" ]]; then
    docker rm -fv "$database_id" > /dev/null || true
  fi
  if [[ -n "$network_id" ]]; then
    docker network rm "$network_id" > /dev/null || true
  fi
  rm -f "$test_dir/benchmark.log" "$test_dir/failure.log"
  rm -f "$test_dir/metrics-true.json" "$test_dir/metrics-false.json" "$test_dir/metrics-failure.json"
  rmdir "$test_dir"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

check_no_credentials() {
  if grep -Fq -e 'ci-password' -e 'wrong-ci-password' "$@"; then
    echo "Runner output or metrics exposed a password"
    exit 1
  fi
}

check_metrics() {
  python3 - "$1" "$2" <<'PY'
import json
import math
import pathlib
import re
import sys


def require(condition, message):
    if not condition:
        sys.exit(message)


logs = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
with open(sys.argv[2], "rb") as file:
    raw = file.read(4097)
require(0 < len(raw) <= 4096, "Missing or oversized runner metrics record")
try:
    record = json.loads(raw)
except (ValueError, UnicodeError):
    sys.exit("Invalid runner metrics JSON")
require(isinstance(record, dict) and set(record) == {"version", "metrics"},
        "Invalid runner metrics envelope")
require(type(record["version"]) is int and record["version"] == 1,
        "Unsupported runner metrics version")
metrics = record["metrics"]
require(isinstance(metrics, list) and len(metrics) == 2,
        "Expected TPS and average latency metrics")
expected = {
    "tps": ("tx/s", r"^tps = (\S+) \(without initial connection time\)$"),
    "latency_avg": ("ms", r"^latency average = (\S+) ms(?: \(including failures\))?$"),
}
seen = set()
for metric in metrics:
    require(isinstance(metric, dict) and set(metric) == {"name", "unit", "value"},
            "Invalid runner metric fields")
    name, value = metric["name"], metric["value"]
    require(isinstance(name, str) and name in expected and name not in seen,
            "Unexpected or duplicate runner metric")
    unit, pattern = expected[name]
    require(metric["unit"] == unit and type(value) in (int, float),
            "Invalid runner metric unit or value")
    require(math.isfinite(value) and value >= 0, "Invalid runner metric value")
    matches = re.findall(pattern, logs, re.MULTILINE)
    require(len(matches) == 1, "Expected one matching pgbench summary line")
    try:
        summary_value = float(matches[0])
    except ValueError:
        sys.exit("Invalid pgbench summary number")
    require(value == summary_value, "Runner metrics differ from pgbench output")
    seen.add(name)
print("Verified termination metrics match pgbench output")
PY
}

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

runner=(docker run --rm --name "$runner_name" --network "$network_id"
  -e PGHOST=pgbench-db -e PGDATABASE=pgbench -e PGUSER=postgres
  -e PGCONNECT_TIMEOUT=3 -e BENCH_DURATION=2)
for initialize in true false; do
  metrics_file="$test_dir/metrics-$initialize.json"
  touch "$metrics_file"
  # Only this file is mounted. Its private host directory stays inaccessible
  # to other users; the container's postgres user needs write access to it.
  chmod 666 "$metrics_file"
  if ! "${runner[@]}" --mount "type=bind,src=$metrics_file,dst=/dev/termination-log" \
    -e PGPASSWORD=ci-password -e BENCH_INITIALIZE="$initialize" \
    "$runner_image" > "$test_dir/benchmark.log" 2>&1; then
    check_no_credentials "$test_dir/benchmark.log" "$metrics_file"
    cat "$test_dir/benchmark.log"
    exit 1
  fi
  check_no_credentials "$test_dir/benchmark.log" "$metrics_file"
  check_metrics "$test_dir/benchmark.log" "$metrics_file"
  cat "$test_dir/benchmark.log"
done
failure_metrics="$test_dir/metrics-failure.json"
touch "$failure_metrics"
chmod 666 "$failure_metrics"
if "${runner[@]}" --mount "type=bind,src=$failure_metrics,dst=/dev/termination-log" \
  -e PGPASSWORD=wrong-ci-password \
  "$runner_image" > "$test_dir/failure.log" 2>&1; then
  echo "Expected authentication failure"
  exit 1
fi
check_no_credentials "$test_dir/failure.log" "$failure_metrics"
grep -q 'password authentication failed' "$test_dir/failure.log"
if [[ -s "$failure_metrics" ]]; then
  echo "Failed benchmark wrote a success metrics record"
  exit 1
fi
