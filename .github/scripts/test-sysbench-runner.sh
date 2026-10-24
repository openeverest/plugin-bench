#!/usr/bin/env bash
set -euo pipefail

runner_image=${1:?Usage: test-sysbench-runner.sh IMAGE}
for tool in docker python3 timeout; do
  command -v "$tool" > /dev/null
done
timeout -k 5s 20s docker info > /dev/null
timeout -k 5s 20s docker image inspect "$runner_image" > /dev/null

test_dir=$(mktemp -d)
network_name="sysbench-test-${test_dir##*/}"
database_name="sysbench-db-${test_dir##*/}"
runner_name="sysbench-runner-${test_dir##*/}"
network_id=""
database_id=""
test_files=()
database_password="ci-sysbench-password"
root_password="ci-sysbench-root-pass"
wrong_password="wrong-ci-sysbench-pass"

cleanup() {
  local status=$?
  trap - EXIT
  # A timed-out Docker client can leave its container running in the daemon.
  timeout -k 5s 15s docker rm -f "$runner_name" > /dev/null 2>&1 || true
  if [[ -n "$database_id" ]]; then
    timeout -k 5s 20s docker rm -fv "$database_id" > /dev/null || status=1
  else
    timeout -k 5s 20s docker rm -fv "$database_name" > /dev/null 2>&1 || true
  fi
  if [[ -n "$network_id" ]]; then
    timeout -k 5s 15s docker network rm "$network_id" > /dev/null || status=1
  else
    timeout -k 5s 15s docker network rm "$network_name" > /dev/null 2>&1 || true
  fi
  if (( ${#test_files[@]} )); then
    rm -f -- "${test_files[@]}" || status=1
  fi
  rmdir -- "$test_dir" || status=1
  if (( status == 0 )); then
    echo "Sysbench runner image checks passed; test resources removed"
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

check_no_credentials() {
  if grep -Fq -e "$database_password" -e "$root_password" -e "$wrong_password" "$@"; then
    echo "Runner output or metrics exposed a password" >&2
    exit 1
  fi
}

show_log() {
  check_no_credentials "$1"
  cat "$1" >&2
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
    "tps": ("tx/s", r"^[ \t]+transactions:[ \t]+\d+[ \t]+\((\S+) per sec\.\)[ \t]*$"),
    "latency_avg": ("ms", r"^Latency \(ms\):\n[ \t]+min:[^\n]*\n[ \t]+avg:[ \t]+(\S+)[ \t]*$"),
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
    require(len(matches) == 1, "Expected one matching sysbench summary value")
    try:
        summary_value = float(matches[0])
    except ValueError:
        sys.exit("Invalid sysbench summary number")
    require(value == summary_value, "Runner metrics differ from sysbench output")
    seen.add(name)
PY
}

query() {
  timeout -k 5s 10s docker exec -e MYSQL_PWD="$database_password" "$database_id" \
    mysql --protocol=TCP --host=127.0.0.1 --port=3306 \
    --user=sbuser --database=sbtest --connect-timeout=3 \
    --batch --skip-column-names --execute="$1"
}

# Each case gets separate logs and an empty metrics file. The runner's UID can
# write the mounted file while its private host directory stays mode 0700.
run_runner() {
  local name=$1
  shift
  log_file="$test_dir/$name.log"
  metrics_file="$test_dir/$name.json"
  test_files+=("$log_file" "$metrics_file")
  touch "$metrics_file"
  chmod 666 "$metrics_file"
  runner_status=0
  timeout -k 10s 45s docker run --rm --name "$runner_name" --network "$network_id" \
    --mount "type=bind,src=$metrics_file,dst=/dev/termination-log" \
    -e MYSQL_HOST=sysbench-db -e MYSQL_DATABASE=sbtest -e MYSQL_USER=sbuser \
    -e MYSQL_PASSWORD="$database_password" -e BENCH_DURATION=2 \
    -e BENCH_THREADS=2 -e BENCH_TABLES=1 -e BENCH_TABLE_SIZE=1000 \
    "$@" "$runner_image" > "$log_file" 2>&1 || runner_status=$?
  check_no_credentials "$log_file" "$metrics_file"
  # A timeout or Docker infrastructure error must never count as an expected
  # application failure. Cleanup removes any surviving container by name.
  case "$runner_status" in
    124|125|126|127|137)
      echo "Runner case $name timed out or could not execute (exit $runner_status)" >&2
      show_log "$log_file"
      exit 1
      ;;
  esac
}

expect_success() {
  local name=$1
  run_runner "$@"
  if (( runner_status != 0 )); then
    echo "Expected successful runner case $name" >&2
    show_log "$log_file"
    exit 1
  fi
  if ! check_metrics "$log_file" "$metrics_file"; then
    echo "Invalid termination metrics for runner case $name" >&2
    show_log "$log_file"
    exit 1
  fi
  echo "Verified $name: termination metrics match sysbench output"
}

expect_failure() {
  local name=$1
  local pattern=$2
  shift 2
  run_runner "$name" "$@"
  if (( runner_status == 0 )) || ! grep -Eq -- "$pattern" "$log_file"; then
    echo "Expected runner failure for $name" >&2
    show_log "$log_file"
    exit 1
  fi
  if [[ -s "$metrics_file" ]]; then
    echo "Failed runner case $name wrote success metrics" >&2
    exit 1
  fi
  echo "Verified $name: expected error and no success metrics"
}

echo "Starting disposable MySQL 8.4 for runner image checks"
network_id=$(timeout -k 5s 20s docker network create "$network_name")
database_id=$(timeout -k 10s 180s docker run -d --name "$database_name" \
  --network "$network_id" --network-alias sysbench-db \
  -e MYSQL_ROOT_PASSWORD="$root_password" -e MYSQL_DATABASE=sbtest \
  -e MYSQL_USER=sbuser -e MYSQL_PASSWORD="$database_password" mysql:8.4)

readiness_log="$test_dir/readiness.log"
database_log="$test_dir/database.log"
test_files+=("$readiness_log" "$database_log")
deadline=$((SECONDS + 120))
until query 'SELECT 1' > "$readiness_log" 2>&1; do
  running=$(timeout -k 5s 10s docker inspect --format '{{.State.Running}}' "$database_id")
  if [[ "$running" != true ]] || (( SECONDS >= deadline )); then
    echo "MySQL exited or did not accept an authenticated query before the readiness deadline" >&2
    timeout -k 5s 10s docker logs "$database_id" > "$database_log" 2>&1 || true
    show_log "$database_log"
    show_log "$readiness_log"
    exit 1
  fi
  sleep 1
done

mysql_version=$(query 'SELECT VERSION()')
echo "MySQL server version: $mysql_version"
timeout -k 5s 30s docker run --rm --name "$runner_name" --entrypoint sh \
  "$runner_image" -ec 'sysbench --version; dpkg-query -W sysbench libmariadb3'

# Readiness authenticated sbuser with MySQL's own client. Clear the server's
# authentication cache so the runner must also handle a full authentication.
timeout -k 5s 10s docker exec -e MYSQL_PWD="$root_password" "$database_id" \
  mysql --protocol=TCP --host=127.0.0.1 --user=root --database=sbtest \
  --connect-timeout=3 --execute='FLUSH PRIVILEGES'

expect_success initialized -e BENCH_INITIALIZE=true
if [[ $(query 'SELECT COUNT(*) FROM sbtest1') != 1000 ]]; then
  echo "Preparation did not populate the expected benchmark table" >&2
  exit 1
fi
# A row outside the configured workload range must survive both reuse and a
# failed second prepare, proving neither silently drops and recreates the table.
query "INSERT INTO sbtest1 (id, k, c, pad) VALUES (2001, 1, 'preserved', 'preserved')"
expect_success reused -e BENCH_INITIALIZE=false
if [[ $(query 'SELECT COUNT(*) FROM sbtest1') != 1001 ]]; then
  echo "Reusing the benchmark table changed its row count" >&2
  exit 1
fi
if [[ $(query "SELECT COUNT(*) FROM sbtest1 WHERE id = 2001 AND c = 'preserved'") != 1 ]]; then
  echo "Reusing the benchmark table removed existing benchmark data" >&2
  exit 1
fi

expect_failure existing-tables 'already exists' -e BENCH_INITIALIZE=true
if [[ $(query "SELECT COUNT(*) FROM sbtest1 WHERE id = 2001 AND c = 'preserved'") != 1 ]]; then
  echo "Failed preparation removed existing benchmark data" >&2
  exit 1
fi
expect_failure authentication 'Access denied for user' \
  -e BENCH_INITIALIZE=false -e MYSQL_PASSWORD="$wrong_password"
expect_failure invalid-config 'BENCH_THREADS must be an integer' -e BENCH_THREADS=0
# The runner image has no MySQL server; its loopback endpoint must refuse TCP.
expect_failure unreachable "Can't connect" -e MYSQL_HOST=127.0.0.1 -e BENCH_INITIALIZE=false
query 'DROP TABLE sbtest1'
expect_failure missing-table "doesn't exist" -e BENCH_INITIALIZE=false
