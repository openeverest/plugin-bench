#!/usr/bin/env bash
set -euo pipefail

image=${1:?Usage: test-hammerdb-runner.sh IMAGE [postgresql|mysql|all]}
selected=${2:-all}
case "$selected" in postgresql|mysql|all) ;; *) echo 'Unsupported engine' >&2; exit 1 ;; esac
for tool in docker python3 timeout; do command -v "$tool" > /dev/null; done
timeout -k 5s 20s docker info > /dev/null || {
  echo 'Docker is unavailable; start Docker Desktop and enable WSL integration' >&2
  exit 1
}
timeout -k 5s 20s docker image inspect "$image" > /dev/null

work=$(mktemp -d)
network="hammerdb-test-${work##*/}"
server="hammerdb-db-${work##*/}"
runner="hammerdb-runner-${work##*/}"
# Braces exercise Tcl literal handling with a real account password.
password='hdb{test}-password'
wrong_password='wrong-hdb-password'
admin_password='hdb-admin-password'
network_id=''
cleanup() {
  local status=$?
  trap - EXIT
  timeout -k 5s 20s docker rm -f "$runner" > /dev/null 2>&1 || true
  timeout -k 5s 20s docker rm -fv "$server" > /dev/null 2>&1 || true
  if [[ -n "$network_id" ]]; then
    timeout -k 5s 20s docker network rm "$network_id" > /dev/null || status=1
  fi
  rm -rf -- "$work"
  if (( status == 0 )); then echo 'HammerDB integration checks passed'; fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

safe_log() {
  if grep -Fq -e "$password" -e 'hdb\{test\}-password' -e "$admin_password" -e "$wrong_password" "$1"; then
    echo 'Credentials found in runner output or metrics' >&2
    exit 1
  fi
}
show_log() { safe_log "$1"; cat "$1" >&2; }

# Administration is confined to disposable servers. Runner credentials have
# privileges only in the pre-created benchmark database.
admin_query() {
  if [[ "$engine" == postgresql ]]; then
    timeout -k 5s 15s docker exec -i -e PGCONNECT_TIMEOUT=3 -e PGPASSWORD="$admin_password" "$server" \
      psql -X -v ON_ERROR_STOP=1 -h 127.0.0.1 -U postgres -d postgres -Atc "$1"
  else
    timeout -k 5s 15s docker exec -i -e MYSQL_PWD="$admin_password" "$server" \
      mysql --protocol=TCP --host=127.0.0.1 --connect-timeout=3 \
      --user=root --batch --skip-column-names --execute="$1"
  fi
}
query() {
  if [[ "$engine" == postgresql ]]; then
    timeout -k 5s 15s docker exec -e PGPASSWORD="$password" "$server" \
      psql -X -v ON_ERROR_STOP=1 -h 127.0.0.1 -U hdb_user -d hdb_probe -Atc "$1"
  else
    timeout -k 5s 15s docker exec -e MYSQL_PWD="$password" "$server" \
      mysql --protocol=TCP --host=127.0.0.1 --user=hdb_user \
      --database=hdb_probe --connect-timeout=3 --batch --skip-column-names --execute="$1"
  fi
}
metadata() {
  if [[ "$engine" == postgresql ]]; then
    admin_query "SELECT datname, pg_get_userbyid(datdba) FROM pg_database ORDER BY datname;
      SELECT rolname, rolsuper, rolcreatedb, rolcreaterole, rolpassword FROM pg_authid ORDER BY rolname;"
  else
    admin_query "SHOW DATABASES;
      SELECT User,Host,plugin,authentication_string FROM mysql.user ORDER BY User,Host;
      SHOW GRANTS FOR 'hdb_user'@'%';"
  fi
}

check_metrics() {
  python3 - "$log" "$metrics" "$engine" <<'PY'
import json, math, pathlib, re, sys
log = pathlib.Path(sys.argv[1]).read_text()
raw = pathlib.Path(sys.argv[2]).read_bytes()
assert 0 < len(raw) <= 4096, 'Missing or oversized metrics'
record = json.loads(raw)
assert set(record) == {'version', 'metrics'} and record['version'] == 1
metrics = record['metrics']
assert isinstance(metrics, list) and metrics
seen = {}
for m in metrics:
    assert set(m) == {'name', 'unit', 'value'}
    assert isinstance(m['name'], str) and m['name'].strip()
    assert isinstance(m['unit'], str) and m['unit'].strip()
    assert type(m['value']) in (int, float) and math.isfinite(m['value']) and m['value'] >= 0
    key = (m['name'], m['unit'])
    assert key not in seen, 'Duplicate metric'
    seen[key] = m['value']
engine = {'postgresql': 'PostgreSQL', 'mysql': 'MySQL'}[sys.argv[3]]
rows = re.findall(r'TEST RESULT : System achieved (\S+) NOPM from (\S+) '+engine+r' TPM', log)
assert len(rows) == 1, 'Expected one native throughput summary'
assert seen.pop(('nopm', 'orders/min')) == float(rows[0][0])
assert seen.pop(('tpm', 'tx/min')) == float(rows[0][1])
# Check each available native timing average against the termination record.
blocks = re.findall(r'RUNNER TIMING BEGIN (\w+)\s*\n(.*?)RUNNER TIMING END', log, re.S)
assert len(blocks) == 1, 'Missing native timing block'
timing = json.loads(blocks[0][1])
for operation, name in [('NEWORD', 'new_order'), ('PAYMENT', 'payment'),
                        ('DELIVERY', 'delivery'), ('OSTAT', 'order_status'), ('SLEV', 'stock_level')]:
    if operation in timing:
        assert seen.pop(('latency_'+name+'_avg', 'ms')) == float(timing[operation]['avg_ms'])
assert not seen, 'Unexpected metrics'
assert 'runner warning: structured metrics unavailable' not in log
PY
}

run_case() {
  local name=$1 initialize=$2 budget=$3
  shift 3
  log="$work/$engine-$name.log"
  metrics="$work/$engine-$name.json"
  touch "$metrics"
  # Only this file is mounted; its parent remains mode 0700 on the host.
  chmod 666 "$metrics"
  runner_status=0
  echo "Running $engine $name (limit ${budget}s)"
  timeout -k 10s "${budget}s" docker run --rm --name "$runner" --network "$network_id" \
    --mount "type=bind,src=$metrics,dst=/dev/termination-log" \
    -e DB_TYPE="$engine" -e DB_HOST=hammerdb-db -e DB_NAME=hdb_probe \
    -e DB_USER=hdb_user -e DB_PASSWORD="$password" \
    -e BENCH_WAREHOUSES=1 -e BENCH_VIRTUAL_USERS=1 \
    -e BENCH_RAMPUP_MINUTES=0 -e BENCH_DURATION_MINUTES=1 \
    -e BENCH_INITIALIZE="$initialize" -e PGSSLMODE=disable -e MYSQL_SSL=true \
    "$@" "$image" > "$log" 2>&1 || runner_status=$?
  safe_log "$log"
  safe_log "$metrics"
  case "$runner_status" in
    0|1) ;;
    *) echo "$engine $name could not execute or timed out (exit $runner_status)" >&2
       show_log "$log"; exit 1 ;;
  esac
}

check_metadata() {
  metadata > "$work/metadata-after"
  cmp -s "$work/metadata-before" "$work/metadata-after" || {
    echo 'Runner changed database/account metadata or credentials' >&2; exit 1;
  }
}

run_success() {
  local name=$1
  run_case "$@"
  if (( runner_status != 0 )); then
    echo "$engine $name failed" >&2
    show_log "$log"
    exit 1
  fi
  check_metrics
  local warehouse
  warehouse=$(query 'SELECT count(*), min(w_id), max(w_id) FROM warehouse')
  [[ "$warehouse" == $'1\t1\t1' || "$warehouse" == '1|1|1' ]]
  check_metadata
  echo "Verified $engine $name: native results, termination metrics and unchanged accounts/databases"
}

expect_failure() {
  local name=$1 initialize=$2 pattern=$3
  shift 3
  run_case "$name" "$initialize" 60 "$@"
  if (( runner_status != 1 )) || ! grep -Eq "$pattern" "$log" || [[ -s "$metrics" ]]; then
    echo "Expected $engine $name failure without success metrics" >&2
    show_log "$log"; exit 1
  fi
  # A preparation/preflight failure must never launch measurement.
  if grep -Eq 'TEST RESULT|Beginning rampup|Timing test period' "$log"; then
    echo 'Measurement started after a failed prerequisite' >&2; exit 1
  fi
  [[ $(grep -c 'HammerDB CLI v6.1.1' "$log" || true) -le 1 ]]
  check_metadata
  echo "Verified $engine $name: expected failure, no measurement or metrics"
}

check_cancellation() {
  log="$work/$engine-cancel.log"
  metrics="$work/$engine-cancel.json"
  touch "$metrics"
  chmod 666 "$metrics"
  timeout -k 5s 20s docker create --name "$runner" --network "$network_id" \
    --mount "type=bind,src=$metrics,dst=/dev/termination-log" \
    -e DB_TYPE="$engine" -e DB_HOST=hammerdb-db -e DB_NAME=hdb_probe \
    -e DB_USER=hdb_user -e DB_PASSWORD="$password" -e BENCH_INITIALIZE=false \
    -e BENCH_WAREHOUSES=1 -e BENCH_VIRTUAL_USERS=1 \
    -e BENCH_RAMPUP_MINUTES=0 -e BENCH_DURATION_MINUTES=1 \
    -e PGSSLMODE=disable -e MYSQL_SSL=true "$image" > /dev/null
  timeout -k 5s 20s docker start "$runner" > /dev/null
  local deadline=$((SECONDS + 60))
  until grep -q 'Timing test period' "$log" 2>/dev/null; do
    timeout -k 5s 10s docker logs "$runner" > "$log" 2>&1
    safe_log "$log"
    if (( SECONDS >= deadline )) || [[ $(timeout -k 5s 10s docker inspect --format '{{.State.Running}}' "$runner") != true ]]; then
      echo 'Cancellation case did not reach active measurement' >&2
      show_log "$log"; exit 1
    fi
    sleep 1
  done
  timeout -k 5s 10s docker kill --signal=TERM "$runner" > /dev/null
  local status
  status=$(timeout -k 5s 20s docker wait "$runner")
  timeout -k 5s 10s docker logs "$runner" > "$log" 2>&1
  safe_log "$log"
  safe_log "$metrics"
  [[ "$status" == 1 && ! -s "$metrics" ]]
  grep -q 'runner cancelled' "$log"
  # The stopped container retains its filesystem: verify deferred cleanup before
  # docker rm can hide any private scripts, passwords or Jobs-state leftovers.
  timeout -k 5s 20s docker cp "$runner:/tmp/." - | python3 -c '
import re, sys, tarfile
with tarfile.open(fileobj=sys.stdin.buffer, mode="r|") as files:
    for member in files:
        assert not re.search(r"(^|/)hammerdb-[0-9]+(/|$)", member.name), "Private runner files survived cancellation"
'
  if [[ "$engine" == postgresql ]]; then
    [[ $(admin_query "SELECT count(*) FROM pg_stat_activity WHERE usename='hdb_user'") == 0 ]]
  else
    [[ $(admin_query "SELECT count(*) FROM information_schema.processlist WHERE USER='hdb_user'") == 0 ]]
  fi
  check_metadata
  timeout -k 5s 20s docker rm "$runner" > /dev/null
  echo "Verified $engine cancellation: timely exit, no metrics, sessions or private files"
}

network_id=$(timeout -k 5s 20s docker network create "$network")
for engine in postgresql mysql; do
  [[ "$selected" == all || "$selected" == "$engine" ]] || continue
  echo "Starting disposable $engine"
  if [[ "$engine" == postgresql ]]; then
    timeout -k 10s 180s docker run -d --name "$server" --network "$network_id" \
      --network-alias hammerdb-db -e POSTGRES_PASSWORD="$admin_password" postgres:17 > /dev/null
  else
    timeout -k 10s 180s docker run -d --name "$server" --network "$network_id" \
      --network-alias hammerdb-db -e MYSQL_ROOT_PASSWORD="$admin_password" mysql:8.4 > /dev/null
  fi
  deadline=$((SECONDS + 120))
  until admin_query 'SELECT 1' > "$work/readiness" 2>&1; do
    if (( SECONDS >= deadline )); then
      echo "$engine readiness timed out" >&2
      # Administrative errors can contain password verifiers; keep them private.
      exit 1
    fi
    sleep 2
  done
  if [[ "$engine" == postgresql ]]; then
    admin_query "CREATE USER hdb_user PASSWORD '$password' NOSUPERUSER NOCREATEDB NOCREATEROLE;" > /dev/null
    admin_query 'CREATE DATABASE hdb_probe OWNER hdb_user;' > /dev/null
    admin_query "CREATE USER hdb_readonly PASSWORD '$password' NOSUPERUSER NOCREATEDB NOCREATEROLE;" > /dev/null
    admin_query 'CREATE DATABASE hdb_denied;' > /dev/null
    admin_query 'GRANT CONNECT ON DATABASE hdb_denied TO hdb_readonly;' > /dev/null
  else
    admin_query "CREATE DATABASE hdb_probe;
      CREATE USER 'hdb_user'@'%' IDENTIFIED BY '$password';
      GRANT ALL PRIVILEGES ON hdb_probe.* TO 'hdb_user'@'%';
      CREATE DATABASE hdb_denied;
      CREATE USER 'hdb_readonly'@'%' IDENTIFIED BY '$password';
      GRANT SELECT ON hdb_denied.* TO 'hdb_readonly'@'%';
      FLUSH PRIVILEGES;" > /dev/null
  fi
  # Readiness of the server is distinct from readiness of runner credentials.
  query 'SELECT 1' > /dev/null
  if [[ "$engine" == mysql ]]; then admin_query 'FLUSH PRIVILEGES;' > /dev/null; fi
  metadata > "$work/metadata-before"
  expect_failure missing-schema false 'HammerDB phase failed'
  expect_failure authentication false 'HammerDB phase failed' -e DB_PASSWORD="$wrong_password"
  expect_failure unreachable false 'HammerDB phase failed' -e DB_HOST=127.0.0.1
  expect_failure invalid-config false 'BENCH_VIRTUAL_USERS must be an integer' -e BENCH_VIRTUAL_USERS=0
  expect_failure privileges true 'HammerDB phase failed' -e DB_NAME=hdb_denied -e DB_USER=hdb_readonly
  run_success initialized true 480
  # Persist a recognizable field to prove reuse did not rebuild the schema.
  query "UPDATE warehouse SET w_name='preserved' WHERE w_id=1" > /dev/null
  run_success reused false 180
  [[ $(query 'SELECT w_name FROM warehouse WHERE w_id=1') == preserved ]]
  expect_failure warehouse-count false 'HammerDB phase failed' -e BENCH_WAREHOUSES=2
  expect_failure nonempty-preparation true 'HammerDB phase failed'
  [[ $(query 'SELECT w_name FROM warehouse WHERE w_id=1') == preserved ]]
  check_cancellation
  timeout -k 5s 30s docker rm -fv "$server" > /dev/null
done
