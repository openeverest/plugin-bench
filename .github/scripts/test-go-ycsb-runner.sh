#!/usr/bin/env bash
set -euo pipefail

runner_image=${1:?Usage: test-go-ycsb-runner.sh IMAGE}
for tool in docker python3 timeout; do
  command -v "$tool" > /dev/null
done
timeout -k 5s 20s docker info > /dev/null
runner_platform=$(timeout -k 5s 20s docker image inspect --format '{{.Os}}/{{.Architecture}}' "$runner_image")
echo "Runner image platform: $runner_platform"

test_dir=$(mktemp -d)
network_name="go-ycsb-test-${test_dir##*/}"
database_name="go-ycsb-db-${test_dir##*/}"
runner_name="go-ycsb-runner-${test_dir##*/}"
network_id=""
database_id=""
test_files=()
root_password="ci-ycsb-root-password"
database_password="ci-ycsb-benchmark-password"
wrong_password="ci-ycsb-wrong-password"

cleanup() {
  local status=$?
  trap - EXIT
  # An interrupted Docker client can leave its container running in the daemon.
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
    echo "go-ycsb runner image checks passed; test resources removed"
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

check_no_credentials() {
  if grep -Fq -e "$database_password" -e "$root_password" -e "$wrong_password" -e 'mongodb://' "$@"; then
    echo "Runner output or metrics exposed connection credentials" >&2
    exit 1
  fi
}

show_log() {
  check_no_credentials "$1"
  cat "$1" >&2
}

# Use environment variables so credentials do not become part of the JS source.
query() {
  local connect='const connection = new Mongo("mongodb://" +
    encodeURIComponent(process.env.YCSB_QUERY_USER) + ":" +
    encodeURIComponent(process.env.YCSB_QUERY_PASSWORD) +
    "@127.0.0.1:27017/?authSource=" + encodeURIComponent(process.env.YCSB_QUERY_AUTHDB) +
    "&connectTimeoutMS=3000&serverSelectionTimeoutMS=3000");'
  timeout -k 5s 15s docker exec \
    -e YCSB_QUERY_USER="$1" -e YCSB_QUERY_PASSWORD="$2" -e YCSB_QUERY_AUTHDB="$3" \
    -e YCSB_TEST_BENCHMARK_PASSWORD="$database_password" \
    "$database_id" mongosh --quiet --nodb --eval "$connect"$'\n'"$4"
}

query_root() {
  query root "$root_password" admin "$1"
}

check_metrics() {
  python3 - "$1" "$2" "$3" <<'PY'
import json
import math
import pathlib
import re
import sys


def require(condition, message):
    if not condition:
        sys.exit(message)


logs = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
markers = list(re.finditer(r"^Run finished, takes [^\n]+$", logs, re.MULTILINE))
require(len(markers) == int(sys.argv[3]), "Unexpected number of completed workload phases")
final = logs[markers[-1].end():]
totals = re.findall(r"^TOTAL[ \t]+-[ \t]+([^\n]+)$", final, re.MULTILINE)
require(len(totals) == 1, "Expected one final measured TOTAL row")
fields = {}
for field in totals[0].split(","):
    name, separator, value = field.partition(":")
    name = name.strip()
    require(separator and name not in fields, "Invalid final summary fields")
    fields[name] = value.strip()
require(fields.get("Count") == "2000", "Measured run did not complete 2000 operations")
try:
    expected = {
        "ops_per_second": ("ops/s", float(fields["OPS"])),
        "latency_avg": ("ms", float(fields["Avg(us)"]) / 1000),
    }
except (KeyError, ValueError):
    sys.exit("Invalid final go-ycsb metrics")
for count in re.findall(r"^[A-Z_]+_ERROR\s+-[^\n]*\bCount:\s*(\d+)(?:,|$)", logs, re.MULTILINE):
    require(int(count) == 0, "go-ycsb reported failed operations")

with open(sys.argv[2], "rb") as file:
    raw = file.read(4097)
require(0 < len(raw) <= 4096, "Missing or oversized termination record")
try:
    record = json.loads(raw)
except (ValueError, UnicodeError):
    sys.exit("Invalid termination metrics JSON")
require(isinstance(record, dict) and set(record) == {"version", "metrics"},
        "Invalid metrics envelope")
require(type(record["version"]) is int and record["version"] == 1,
        "Unsupported metrics version")
metrics = record["metrics"]
require(isinstance(metrics, list) and len(metrics) == 2, "Expected throughput and latency metrics")
seen = set()
for metric in metrics:
    require(isinstance(metric, dict) and set(metric) == {"name", "unit", "value"},
            "Invalid metric fields")
    name, value = metric["name"], metric["value"]
    require(isinstance(name, str) and name in expected and name not in seen,
            "Unexpected or duplicate metric")
    unit, summary = expected[name]
    require(metric["unit"] == unit and type(value) in (int, float), "Invalid metric unit or value")
    require(math.isfinite(value) and value >= 0 and value == summary,
            "Termination metrics differ from final go-ycsb output")
    seen.add(name)
PY
}

mongodb_uri() {
  # All credentials and names supplied here are URI-safe test constants.
  printf 'mongodb://%s:%s@%s:27017/%s?authSource=%s&connectTimeoutMS=3000&serverSelectionTimeoutMS=5000' \
    "$1" "$2" "${5:-go-ycsb-db}" "${4:-ycsb}" "$3"
}

run_runner() {
  local name=$1
  local initialize=$2
  shift 2
  log_file="$test_dir/$name.log"
  metrics_file="$test_dir/$name.json"
  test_files+=("$log_file" "$metrics_file")
  touch "$metrics_file"
  # Only this file is mounted; its private host directory remains mode 0700.
  chmod 666 "$metrics_file"
  runner_status=0
  timeout -k 10s 90s docker run --rm --name "$runner_name" --platform "$runner_platform" --network "$network_id" \
    --mount "type=bind,src=$metrics_file,dst=/dev/termination-log" \
    -e "MONGODB_URI=$(mongodb_uri ycsb_only "$database_password" ycsb)" \
    -e BENCH_COLLECTION=runner_records -e BENCH_RECORD_COUNT=1000 \
    -e BENCH_OPERATION_COUNT=2000 -e BENCH_THREADS=2 \
    -e BENCH_INITIALIZE="$initialize" \
    "$@" "$runner_image" > "$log_file" 2>&1 || runner_status=$?
  check_no_credentials "$log_file" "$metrics_file"
  # Docker failures and timeouts must never count as expected runner failures.
  case "$runner_status" in
    124|125|126|127|137|143)
      echo "Runner case $name timed out or could not execute (exit $runner_status)" >&2
      show_log "$log_file"
      exit 1
      ;;
  esac
}

run_success() {
  local name=$1
  local initialize=$2
  run_runner "$@"
  if (( runner_status != 0 )); then
    echo "Expected successful runner case $name (exit $runner_status)" >&2
    show_log "$log_file"
    exit 1
  fi
  local phases=1
  if [[ "$initialize" == true ]]; then phases=2; fi
  if ! check_metrics "$log_file" "$metrics_file" "$phases"; then
    echo "Invalid termination metrics for runner case $name" >&2
    show_log "$log_file"
    exit 1
  fi
  echo "Verified $name: termination metrics match the final measured summary"
}

run_failure() {
  local name=$1
  local initialize=$2
  local pattern=$3
  shift 3
  run_runner "$name" "$initialize" "$@"
  if (( runner_status != 1 )) || ! grep -Eq -- "$pattern" "$log_file"; then
    echo "Expected runner failure for $name (exit $runner_status)" >&2
    show_log "$log_file"
    exit 1
  fi
  if [[ -s "$metrics_file" ]]; then
    echo "Failed runner case $name wrote success metrics" >&2
    exit 1
  fi
  echo "Verified $name: expected failure and no success metrics"
}

timeout -k 5s 30s docker run --rm --name "$runner_name" --platform "$runner_platform" \
  --entrypoint sh "$runner_image" -ec '
    test "$(id -u)" -eq 10001
    test "$(id -g)" -eq 10001
    test -w /tmp
    test -s /etc/ssl/certs/ca-certificates.crt
    test -s /usr/share/licenses/go-ycsb/LICENSE
    test -s /usr/share/licenses/go-ycsb/YCSB-LICENSE
    test -s /etc/go-ycsb/workloads/workloada
    go-ycsb run --help > /dev/null
    printf "Verified runner startup and packaged files on "
    uname -m
  '

echo "Starting disposable MongoDB 8.0 for runner image checks"
network_id=$(timeout -k 5s 20s docker network create "$network_name")
database_id=$(timeout -k 10s 180s docker run -d --name "$database_name" \
  --network "$network_id" --network-alias go-ycsb-db \
  -e MONGO_INITDB_ROOT_USERNAME=root -e MONGO_INITDB_ROOT_PASSWORD="$root_password" mongo:8.0)

readiness_log="$test_dir/readiness.log"
database_log="$test_dir/database.log"
setup_log="$test_dir/setup.log"
test_files+=("$readiness_log" "$database_log" "$setup_log")
deadline=$((SECONDS + 120))
until query_root 'print(connection.getDB("admin").runCommand({ping: 1}).ok)' > "$readiness_log" 2>&1; do
  running=$(timeout -k 5s 10s docker inspect --format '{{.State.Running}}' "$database_id")
  if [[ "$running" != true ]] || (( SECONDS >= deadline )); then
    echo "MongoDB exited or failed authenticated readiness before the deadline" >&2
    timeout -k 5s 10s docker logs "$database_id" > "$database_log" 2>&1 || true
    show_log "$database_log"
    show_log "$readiness_log"
    exit 1
  fi
  sleep 1
done

if ! query_root '
  for (const [authdb, user, role, target] of [
    ["ycsb", "ycsb_only", "readWrite", "ycsb"],
    ["ycsb", "read_only", "read", "ycsb"],
    ["other_database", "other_only", "readWrite", "other_database"],
    ["admin", "admin_authenticated", "readWrite", "ycsb"]
  ]) {
    connection.getDB(authdb).createUser({user, pwd: process.env.YCSB_TEST_BENCHMARK_PASSWORD,
      roles: [{role, db: target}]});
  }
  connection.getDB("other_database").guard.insertOne({_id: "preserved", marker: "keep"});
' > "$setup_log" 2>&1; then
  show_log "$setup_log"
  exit 1
fi
if ! query ycsb_only "$database_password" ycsb '
  const status = connection.getDB("admin").runCommand({connectionStatus: 1});
  const roles = status.authInfo.authenticatedUserRoles;
  if (roles.length !== 1 || roles[0].role !== "readWrite" || roles[0].db !== "ycsb") {
    throw new Error("Benchmark user must have only readWrite access on ycsb");
  }
  print("Restricted benchmark user authenticated");' > "$setup_log" 2>&1; then
  show_log "$setup_log"
  exit 1
fi
mongo_version=$(query_root 'print(connection.getDB("admin").version())')
echo "MongoDB server version: $mongo_version"

run_success initialized true
if [[ $(query_root 'print(connection.getDB("ycsb").runner_records.countDocuments({}))') != 1000 ]]; then
  echo "Loading did not create the expected 1000 benchmark documents" >&2
  exit 1
fi
# This key is outside go-ycsb's generated key range and must survive reuse.
query_root 'connection.getDB("ycsb").runner_records.insertOne({_id: "preserved-sentinel", marker: "keep"})' > "$setup_log" 2>&1
run_success reused false
if [[ $(query_root 'print(connection.getDB("ycsb").runner_records.countDocuments({}))') != 1001 ]] ||
   [[ $(query_root 'print(connection.getDB("ycsb").runner_records.countDocuments({_id: "preserved-sentinel", marker: "keep"}))') != 1 ]]; then
  echo "Reusing benchmark data changed its size or removed existing documents" >&2
  exit 1
fi
if [[ $(query_root 'print(connection.getDB("ycsb").usertable.countDocuments({}))') != 0 ]] ||
   [[ $(query_root 'print(connection.getDB("other_database").runner_records.countDocuments({}))') != 0 ]]; then
  echo "Benchmark wrote to an unexpected database or default collection" >&2
  exit 1
fi

run_success admin-authenticated true \
  -e "MONGODB_URI=$(mongodb_uri admin_authenticated "$database_password" admin)" \
  -e BENCH_COLLECTION=admin_records
if [[ $(query_root 'print(connection.getDB("ycsb").admin_records.countDocuments({}))') != 1000 ]] ||
   [[ $(query_root 'print(connection.getDB("admin").admin_records.countDocuments({}))') != 0 ]]; then
  echo "Authentication source changed the benchmark database" >&2
  exit 1
fi

run_failure duplicate-load true 'load failed: go-ycsb reported failed operations'
if [[ $(grep -c '^Run finished, takes ' "$log_file") != 1 ]]; then
  echo "Failed duplicate loading did not stop before the measured phase" >&2
  exit 1
fi
if [[ $(query_root 'print(connection.getDB("ycsb").runner_records.countDocuments({}))') != 1001 ]] ||
   [[ $(query_root 'print(connection.getDB("ycsb").runner_records.countDocuments({_id: "preserved-sentinel", marker: "keep"}))') != 1 ]]; then
  echo "Failed duplicate loading removed existing benchmark data" >&2
  exit 1
fi

run_failure authentication false 'Authentication failed|unable to authenticate' \
  -e "MONGODB_URI=$(mongodb_uri ycsb_only "$wrong_password" ycsb)"
run_failure unreachable false 'connection refused|server selection' \
  -e "MONGODB_URI=$(mongodb_uri ycsb_only "$database_password" ycsb ycsb 127.0.0.1)"
run_failure invalid-config false 'BENCH_THREADS must be an integer' -e BENCH_THREADS=0

run_failure other-database-only true 'load failed: go-ycsb reported failed operations' \
  -e "MONGODB_URI=$(mongodb_uri other_only "$database_password" other_database)" \
  -e BENCH_COLLECTION=forbidden_records
if [[ $(query_root 'print(connection.getDB("ycsb").forbidden_records.countDocuments({}))') != 0 ]] ||
   [[ $(query_root 'print(connection.getDB("other_database").forbidden_records.countDocuments({}))') != 0 ]]; then
  echo "Unauthorized loading wrote benchmark data" >&2
  exit 1
fi
run_failure read-only false 'run failed: go-ycsb reported failed operations' \
  -e "MONGODB_URI=$(mongodb_uri read_only "$database_password" ycsb)"

run_failure missing-data false 'run failed: go-ycsb reported failed operations' \
  -e BENCH_COLLECTION=missing_records
if [[ $(query_root 'print(connection.getDB("ycsb").missing_records.countDocuments({}))') != 0 ]]; then
  echo "Reusing missing data inserted unexpected documents" >&2
  exit 1
fi
# Preserve the generated key range but give the expected fields embedded BSON
# documents, which cannot be decoded into the binding's map[string][]byte.
query_root '
  const docs = connection.getDB("ycsb").runner_records.find({_id: {$ne: "preserved-sentinel"}}).toArray();
  connection.getDB("ycsb").incompatible_records.insertMany(
    docs.map(doc => {
      const invalid = {_id: doc._id};
      for (let i = 0; i < 10; i++) invalid["field" + i] = {incompatible: true};
      return invalid;
    }));
' > "$setup_log" 2>&1
run_failure incompatible-data false 'run failed: go-ycsb reported failed operations' \
  -e BENCH_COLLECTION=incompatible_records
if ! grep -Eq '^READ_ERROR[[:space:]]+-.*Count:[[:space:]]*[1-9]' "$log_file"; then
  echo "Incompatible data did not produce read errors" >&2
  exit 1
fi

run_failure unsupported-database true 'configuration error: go-ycsb supports only the ycsb database' \
  -e "MONGODB_URI=$(mongodb_uri ycsb_only "$database_password" ycsb other_database)" \
  -e BENCH_COLLECTION=wrong_database_probe
if grep -Fq 'Connected to MongoDB!' "$log_file" ||
   [[ $(query_root 'print(connection.getDB("ycsb").wrong_database_probe.countDocuments({}))') != 0 ]] ||
   [[ $(query_root 'print(connection.getDB("other_database").wrong_database_probe.countDocuments({}))') != 0 ]]; then
  echo "Unsupported database was not rejected before workload execution" >&2
  exit 1
fi
if [[ $(query_root 'print(connection.getDB("other_database").guard.countDocuments({_id: "preserved", marker: "keep"}))') != 1 ]]; then
  echo "Benchmark cases changed data in another database" >&2
  exit 1
fi
