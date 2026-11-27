#!/usr/bin/env bash
set -euo pipefail

runner_image=${1:?Usage: test-valkey-runner.sh IMAGE}
for tool in docker openssl python3 timeout; do
  command -v "$tool" > /dev/null
done
timeout -k 5s 20s docker info > /dev/null
timeout -k 5s 20s docker image inspect "$runner_image" > /dev/null
timeout -k 5s 20s docker run --rm --entrypoint sh "$runner_image" -ec '
  test "$(id -u)" -eq 10001
  test "$(id -g)" -eq 10001
  memtier_benchmark --version 2>&1 | grep -Eq "(^|[[:space:]])(v=)?2\.6\.1([[:space:]]|$)"
'

test_dir=$(mktemp -d)
cert_dir="$test_dir/certs"
mkdir "$cert_dir"
network_name="valkey-test-${test_dir##*/}"
database_name="valkey-db-${test_dir##*/}"
runner_name="valkey-runner-${test_dir##*/}"
network_id=""
database_id=""
test_files=()
database_password="ci-valkey-password"
wrong_password="ci-valkey-wrong-password"
restricted_user="benchmark-user"
restricted_password="ci-valkey:restricted-password"

cleanup() {
  local status=$?
  trap - EXIT
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
  rm -rf -- "$test_dir" || status=1
  if (( status == 0 )); then
    echo "Valkey runner image checks passed; test resources removed"
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

check_no_credentials() {
  if grep -Fq -e "$database_password" -e "$wrong_password" -e "$restricted_password" "$@"; then
    echo "Runner output or metrics exposed a password" >&2
    exit 1
  fi
}

show_log() {
  check_no_credentials "$1"
  cat "$1" >&2
}

make_metrics_file() {
  local name=$1
  metrics_file="$test_dir/$name.json"
  test_files+=("$metrics_file")
  touch "$metrics_file"
  chmod 666 "$metrics_file"
}

run_runner() {
  local name=$1
  shift
  log_file="$test_dir/$name.log"
  test_files+=("$log_file")
  make_metrics_file "$name"
  runner_status=0
  timeout -k 10s 45s docker run --rm --name "$runner_name" --network "$network_id" \
    --mount "type=bind,src=$metrics_file,dst=/dev/termination-log" \
    -e VALKEY_HOST=valkey-test -e VALKEY_PORT=6379 \
    -e VALKEY_USERNAME=default -e VALKEY_PASSWORD="$database_password" \
    -e VALKEY_TLS=true -e "VALKEY_TLS_CA=$(cat "$cert_dir/ca.crt")" \
    -e BENCH_DURATION=2 -e BENCH_THREADS=2 -e BENCH_CLIENTS_PER_THREAD=2 \
    -e BENCH_KEY_COUNT=100 -e BENCH_VALUE_SIZE=64 \
    "$@" "$runner_image" > "$log_file" 2>&1 || runner_status=$?
  check_no_credentials "$log_file" "$metrics_file"
  case "$runner_status" in
    124|125|126|127|137|143)
      echo "Runner case $name timed out or could not execute (exit $runner_status)" >&2
      show_log "$log_file"
      exit 1
      ;;
  esac
}

check_metrics() {
  python3 - "$1" "$2" <<'PY'
import json
import math
import pathlib
import sys


def require(condition, message):
    if not condition:
        sys.exit(message)


logs = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
raw = pathlib.Path(sys.argv[2]).read_bytes()
require(0 < len(raw) <= 4096, "Missing or oversized termination metrics")
try:
    result = json.loads(raw)
except (ValueError, UnicodeError):
    sys.exit("Invalid termination metrics JSON")
require(isinstance(result, dict) and set(result) == {"version", "metrics"},
        "Invalid termination metrics envelope")
require(type(result["version"]) is int and result["version"] == 1,
        "Unsupported termination metrics version")
expected = {
    "ops_per_second": "ops/s",
    "latency_avg": "ms",
    "latency_p95": "ms",
    "latency_p99": "ms",
    "get_ops_per_second": "ops/s",
    "set_ops_per_second": "ops/s",
    "get_hits_per_second": "ops/s",
    "get_misses_per_second": "ops/s",
}
metrics = result["metrics"]
require(isinstance(metrics, list) and len(metrics) == len(expected),
        "Unexpected number of runner metrics")
values = {}
for metric in metrics:
    require(isinstance(metric, dict) and set(metric) == {"name", "unit", "value"},
            "Invalid runner metric fields")
    name, unit, value = metric["name"], metric["unit"], metric["value"]
    require(name in expected and name not in values and unit == expected[name],
            "Unexpected or duplicate runner metric")
    require(type(value) in (int, float) and math.isfinite(value) and value >= 0,
            "Invalid runner metric value")
    values[name] = value
for name in ("ops_per_second", "get_ops_per_second", "set_ops_per_second", "get_hits_per_second"):
    require(values[name] > 0, f"Measured workload reported no {name}")
require(values["get_misses_per_second"] == 0, "Measured GET missed a verified seed key")
require("ALL STATS" in logs, "Memtier summary is missing from runner logs")
PY
}

expect_success() {
  local name=$1
  run_runner "$@"
  if (( runner_status != 0 )); then
    echo "Expected successful runner case $name (exit $runner_status)" >&2
    show_log "$log_file"
    exit 1
  fi
  if ! check_metrics "$log_file" "$metrics_file"; then
    echo "Invalid metrics for runner case $name" >&2
    show_log "$log_file"
    exit 1
  fi
  echo "Verified $name: successful benchmark and structured metrics"
}

expect_failure() {
  local name=$1
  local pattern=$2
  shift 2
  run_runner "$name" "$@"
  if (( runner_status != 1 )) || ! grep -Eq -- "$pattern" "$log_file"; then
    echo "Expected runner failure for $name (exit $runner_status)" >&2
    show_log "$log_file"
    exit 1
  fi
  if [[ -s "$metrics_file" ]]; then
    echo "Failed runner case $name wrote success metrics" >&2
    exit 1
  fi
  echo "Verified $name: expected error and no success metrics"
}

echo "Creating certificates for disposable TLS-enabled Valkey"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -keyout "$cert_dir/ca.key" -out "$cert_dir/ca.crt" \
  -subj "/CN=plugin-bench-test-ca" \
  -addext "basicConstraints=critical,CA:TRUE" \
  -addext "keyUsage=critical,keyCertSign,cRLSign" > /dev/null 2>&1
openssl req -newkey rsa:2048 -nodes \
  -keyout "$cert_dir/server.key" -out "$cert_dir/server.csr" \
  -subj "/CN=valkey-test" > /dev/null 2>&1
printf '%s\n' 'basicConstraints=critical,CA:FALSE' \
  'keyUsage=critical,digitalSignature,keyEncipherment' \
  'extendedKeyUsage=serverAuth' 'subjectAltName=DNS:valkey-test' > "$test_dir/server.ext"
openssl x509 -req -days 1 -in "$cert_dir/server.csr" \
  -CA "$cert_dir/ca.crt" -CAkey "$cert_dir/ca.key" -CAcreateserial \
  -extfile "$test_dir/server.ext" -out "$cert_dir/server.crt" > /dev/null 2>&1
openssl req -newkey rsa:2048 -nodes \
  -keyout "$cert_dir/client.key" -out "$cert_dir/client.csr" \
  -subj "/CN=plugin-bench-runner" > /dev/null 2>&1
printf '%s\n' 'basicConstraints=critical,CA:FALSE' \
  'keyUsage=critical,digitalSignature' 'extendedKeyUsage=clientAuth' > "$test_dir/client.ext"
openssl x509 -req -days 1 -in "$cert_dir/client.csr" \
  -CA "$cert_dir/ca.crt" -CAkey "$cert_dir/ca.key" -CAserial "$cert_dir/ca.srl" \
  -extfile "$test_dir/client.ext" -out "$cert_dir/client.crt" > /dev/null 2>&1
openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -keyout "$cert_dir/wrong-ca.key" -out "$cert_dir/wrong-ca.crt" \
  -subj "/CN=plugin-bench-wrong-ca" > /dev/null 2>&1
chmod 755 "$cert_dir"
chmod 644 "$cert_dir/server.key" "$cert_dir/server.crt" "$cert_dir/ca.crt" "$cert_dir/wrong-ca.crt"

network_id=$(timeout -k 5s 20s docker network create "$network_name")
database_id=$(timeout -k 10s 120s docker run -d --name "$database_name" \
  --network "$network_id" --network-alias valkey-test --network-alias valkey-alias \
  --mount "type=bind,src=$cert_dir,dst=/certs,readonly" \
  valkey/valkey:9.0.7 valkey-server \
  --port 6380 --tls-port 6379 --tls-cert-file /certs/server.crt \
  --tls-key-file /certs/server.key --tls-ca-cert-file /certs/ca.crt \
  --tls-auth-clients no --requirepass "$database_password")

readiness_log="$test_dir/readiness.log"
database_log="$test_dir/database.log"
test_files+=("$readiness_log" "$database_log")
deadline=$((SECONDS + 60))
until timeout -k 3s 8s docker exec -e REDISCLI_AUTH="$database_password" "$database_id" \
  valkey-cli --tls --cacert /certs/ca.crt -h valkey-test -p 6379 \
  --user default --no-auth-warning PING > "$readiness_log" 2>&1; do
  running=$(timeout -k 3s 8s docker inspect --format '{{.State.Running}}' "$database_id")
  if [[ "$running" != true ]] || (( SECONDS >= deadline )); then
    echo "Valkey exited or did not accept an authenticated TLS query before readiness deadline" >&2
    timeout -k 3s 8s docker logs "$database_id" > "$database_log" 2>&1 || true
    show_log "$database_log"
    show_log "$readiness_log"
    exit 1
  fi
  sleep 1
done
if [[ $(<"$readiness_log") != PONG ]]; then
  echo "Valkey TLS readiness probe did not return PONG" >&2
  show_log "$readiness_log"
  exit 1
fi

expect_empty_database() {
  local key_count
  key_count=$(timeout -k 3s 8s docker exec -e REDISCLI_AUTH="$database_password" "$database_id" \
    valkey-cli --raw --tls --cacert /certs/ca.crt -h valkey-test -p 6379 \
    --user default --no-auth-warning DBSIZE)
  if [[ "$key_count" != 0 ]]; then
    echo "Expected no benchmark keys after $1" >&2
    exit 1
  fi
}

expect_success tls-authenticated
expect_empty_database success
expect_success plaintext-authenticated -e VALKEY_PORT=6380 -e VALKEY_TLS=false -e VALKEY_TLS_CA=
expect_empty_database plaintext

acl_result=$(timeout -k 3s 8s docker exec -e REDISCLI_AUTH="$database_password" "$database_id" \
  valkey-cli --raw --tls --cacert /certs/ca.crt -h valkey-test -p 6379 \
  --user default --no-auth-warning ACL SETUSER "$restricted_user" reset on \
  ">$restricted_password" '~plugin-bench:*' +info +role +select +set +setex +get +exists +unlink)
if [[ "$acl_result" != OK ]]; then
  echo "Could not configure the restricted Valkey test user" >&2
  exit 1
fi
expect_success tls-restricted-user \
  -e "VALKEY_USERNAME=$restricted_user" -e "VALKEY_PASSWORD=$restricted_password"
expect_empty_database restricted-user

expect_failure authentication 'Valkey authentication failed' -e VALKEY_PASSWORD="$wrong_password"
if grep -Fxq 'Valkey benchmark: seeding' "$log_file"; then
  echo "Runner started seeding after invalid credentials" >&2
  exit 1
fi
expect_failure wrong-ca 'could not connect to Valkey or verify its TLS certificate' \
  -e "VALKEY_TLS_CA=$(cat "$cert_dir/wrong-ca.crt")"
# Confirm both names resolve to this server before checking the certificate name.
primary_ip=$(timeout 8s docker exec "$database_id" getent hosts valkey-test)
alias_ip=$(timeout 8s docker exec "$database_id" getent hosts valkey-alias)
[[ ${primary_ip%% *} == "${alias_ip%% *}" ]]
expect_failure wrong-hostname 'could not connect to Valkey or verify its TLS certificate' \
  -e VALKEY_HOST=valkey-alias
expect_failure invalid-config 'BENCH_THREADS must be an integer' -e BENCH_THREADS=0

# Require client certificates on the same disposable TLS endpoint.
# The Valkey CLI used to toggle this setting must also present a client cert.
mtls_cli() {
  timeout -k 3s 8s docker exec --user 0 -e REDISCLI_AUTH="$database_password" "$database_id" \
    valkey-cli --raw --tls --cacert /certs/ca.crt --cert /certs/client.crt \
    --key /certs/client.key -h valkey-test -p 6379 --user default --no-auth-warning "$@"
}

if [[ $(mtls_cli CONFIG SET tls-auth-clients yes) != OK ]]; then
  echo "Could not require client certificates on the disposable Valkey server" >&2
  exit 1
fi
expect_success mtls-authenticated \
  -e "VALKEY_TLS_CERT=$(cat "$cert_dir/client.crt")" \
  -e "VALKEY_TLS_KEY=$(cat "$cert_dir/client.key")"
expect_failure mtls-missing-client-certificate \
  'Valkey connection failed during authentication \(does the server require a client certificate\?\)'
if grep -Fxq 'Valkey benchmark: seeding' "$log_file"; then
  echo "Runner started seeding without the required client certificate" >&2
  exit 1
fi
if [[ $(mtls_cli DBSIZE) != 0 || $(mtls_cli CONFIG SET tls-auth-clients no) != OK ]]; then
  echo "Expected clean benchmark keys and restored TLS settings after the mTLS cases" >&2
  exit 1
fi

# Deliver SIGTERM during measurement and observe the runner's own exit status.
bounded_log="$test_dir/bounded.log"
test_files+=("$bounded_log")
make_metrics_file bounded
timeout -k 5s 20s docker run -d --name "$runner_name" --network "$network_id" \
  --mount "type=bind,src=$metrics_file,dst=/dev/termination-log" \
  -e VALKEY_HOST=valkey-test -e VALKEY_PORT=6379 \
  -e VALKEY_USERNAME=default -e VALKEY_PASSWORD="$database_password" \
  -e VALKEY_TLS=true -e "VALKEY_TLS_CA=$(cat "$cert_dir/ca.crt")" \
  -e BENCH_DURATION=300 -e BENCH_THREADS=2 -e BENCH_CLIENTS_PER_THREAD=2 \
  -e BENCH_KEY_COUNT=100 -e BENCH_VALUE_SIZE=64 \
  "$runner_image" > /dev/null
deadline=$((SECONDS + 30))
while :; do
  timeout -k 2s 5s docker logs "$runner_name" > "$bounded_log" 2>&1
  if grep -Fxq 'Valkey benchmark: measurement' "$bounded_log"; then
    break
  fi
  if (( SECONDS >= deadline )) || [[ $(timeout 5s docker inspect -f '{{.State.Running}}' "$runner_name") != true ]]; then
    echo "Runner did not reach measurement before cancellation" >&2
    show_log "$bounded_log"
    exit 1
  fi
  sleep 0.25
done
timeout -k 2s 5s docker kill --signal=TERM "$runner_name" > /dev/null
bounded_status=$(timeout -k 2s 15s docker wait "$runner_name")
timeout -k 2s 5s docker logs "$runner_name" > "$bounded_log" 2>&1
check_no_credentials "$bounded_log" "$metrics_file"
if [[ "$bounded_status" != 1 || -s "$metrics_file" ]] ||
   ! grep -Fxq 'Valkey runner: measurement failed: context canceled' "$bounded_log"; then
  echo "Expected cancellation during measurement, exit 1 and no success metrics" >&2
  show_log "$bounded_log"
  exit 1
fi
expect_empty_database cancellation
echo "Verified runner exited after SIGTERM during measurement"
timeout -k 2s 8s docker rm "$runner_name" > /dev/null

# Switch the disposable server to a read-only replica.
replica_result=$(timeout -k 3s 8s docker exec -e REDISCLI_AUTH="$database_password" "$database_id" \
  valkey-cli --raw --tls --cacert /certs/ca.crt -h valkey-test -p 6379 \
  --user default --no-auth-warning REPLICAOF 127.0.0.1 1)
if [[ "$replica_result" != OK ]]; then
  echo "Could not configure the disposable server as a replica" >&2
  exit 1
fi
expect_failure read-only-replica 'Valkey endpoint is a replica'
if grep -Fxq 'Valkey benchmark: seeding' "$log_file"; then
  echo "Runner started seeding against a read-only replica" >&2
  exit 1
fi

# One cluster-enabled node is enough to exercise the startup check.
timeout -k 3s 8s docker exec -d "$database_id" valkey-server \
  --port 6381 --cluster-enabled yes --cluster-config-file /tmp/benchmark-cluster.conf \
  --save "" --requirepass "$database_password"
deadline=$((SECONDS + 30))
until [[ $(timeout -k 2s 5s docker exec -e REDISCLI_AUTH="$database_password" "$database_id" \
  valkey-cli --raw -p 6381 --no-auth-warning PING 2>/dev/null) == PONG ]]; do
  if (( SECONDS >= deadline )); then
    echo "Valkey cluster node did not accept an authenticated PING" >&2
    exit 1
  fi
  sleep 1
done
expect_failure cluster-mode 'Valkey cluster mode is not supported' \
  -e VALKEY_PORT=6381 -e VALKEY_TLS=false -e VALKEY_TLS_CA=
if grep -Fxq 'Valkey benchmark: seeding' "$log_file"; then
  echo "Runner started seeding against a Valkey cluster node" >&2
  exit 1
fi
