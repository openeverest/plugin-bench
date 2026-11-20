#!/usr/bin/env bash
# Offline image checks; real database execution is tested separately.
set -euo pipefail

image=${1:?Usage: check-hammerdb-image.sh IMAGE [linux/amd64|linux/arm64]}
platform_args=()
expected_machine=""
case "${2:-}" in
  "") ;;
  linux/amd64) platform_args=(--platform linux/amd64); expected_machine=x86_64 ;;
  linux/arm64) platform_args=(--platform linux/arm64); expected_machine=aarch64 ;;
  *) echo 'Unsupported platform' >&2; exit 1 ;;
esac
for tool in docker timeout; do
  command -v "$tool" > /dev/null
done
timeout -k 5s 20s docker info > /dev/null
entrypoint=$(timeout -k 5s 20s docker image inspect --format '{{json .Config.Entrypoint}}' "$image")
[[ "$entrypoint" == '["/usr/local/bin/hammerdb-runner"]' ]]

test_dir=$(mktemp -d)
container="hammerdb-image-${test_dir##*/}"
cleanup() {
  local status=$?
  trap - EXIT
  timeout -k 5s 15s docker rm -f "$container" > /dev/null 2>&1 || true
  rm -rf -- "$test_dir"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Tcl extracts bundled shared libraries into /tmp, so the tmpfs needs exec.
# Use an empty /tmp and no network: client loading must not need a database.
timeout -k 5s 90s docker run "${platform_args[@]}" --rm --name "$container" --network none \
  -e EXPECTED_MACHINE="$expected_machine" \
  --tmpfs /tmp:rw,exec,mode=1777 --entrypoint /bin/sh "$image" -ec '
  test "$(id -u)" = 10001
  test "$(id -g)" = 10001
  test -z "$EXPECTED_MACHINE" || test "$(uname -m)" = "$EXPECTED_MACHINE"
  test -x ./hammerdbcli
  umask 077
  state=$(mktemp -d /tmp/hammerdb-image.XXXXXX)
  export HOME="$state" TMP="$state" TEMP="$state" TMPDIR="$state"
  cat > "$state/check.tcl" <<'"'"'TCL'"'"'
if {[catch {
    if {![string match "9.*" [info patchlevel]]} {error "Tcl 9 required"}
    puts "CLIENT_POSTGRESQL=[package require Pgtcl 2.1.1.2]"
    puts "CLIENT_MYSQL=[package require mysqltcl 3.052.1]"
} message]} {
    puts stderr $message
    exit 1
}
puts "HAMMERDB_IMAGE_CLIENTS_OK"
exit 0
TCL
  test "$(stat -c %a "$state")" = 700
  test "$(stat -c %a "$state/check.tcl")" = 600
  ./hammerdbcli auto "$state/check.tcl"
' > "$test_dir/clients.log" 2>&1 || { cat "$test_dir/clients.log" >&2; exit 1; }
grep -F 'HammerDB CLI v6.1.1' "$test_dir/clients.log"
grep -F 'HAMMERDB_IMAGE_CLIENTS_OK' "$test_dir/clients.log"

# The real entrypoint must reject missing settings before invoking HammerDB.
set +e
timeout -k 5s 30s docker run "${platform_args[@]}" --rm --name "$container" --network none \
  --tmpfs /tmp:rw,exec,mode=1777 -e DB_TYPE=invalid "$image" > "$test_dir/invalid.log" 2>&1
status=$?
set -e
[[ "$status" == 1 ]] || { cat "$test_dir/invalid.log" >&2; exit 1; }
grep -F 'configuration error: DB_TYPE must be postgresql or mysql' "$test_dir/invalid.log"
echo 'HammerDB image checks passed'
