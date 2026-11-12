# HammerDB runner contract

This directory contains configuration validation and Tcl configuration generation
for a standalone HammerDB **6.1.1** TPROC-C runner for PostgreSQL and MySQL.
Private staging, executable Tcl phases, Go process execution and output redaction
are implemented. Metrics and image packaging follow.

## Runtime

- Use one image based on `tpcorg/hammerdb:v6.1.1-base`, adding both database clients.
  The inspected base index is
  `sha256:1bf2882b9462834105bc39cb850327596a19c706863f6e31db9e2ac2119bcda9`;
  it contains `linux/amd64` and `linux/arm64` manifests.
- HammerDB's working directory is `/home/hammerdb`, a symlink to
  `/home/HammerDB-6.1.1`. Invoke `./hammerdbcli auto /tmp/<private-directory>/run.tcl`
  from that directory. The release CLI is a bundled executable with Tcl 9.0.2.
- PostgreSQL uses `Pgtcl` 2.1.1.2 and `libpq.so.5`. Upstream installs `libpq5`
  from the PGDG Ubuntu Noble repository, pinned to `17.*`, with
  `postgresql-client-17`.
- MySQL uses `mysqltcl` 3.052.1 and `libmysqlclient.so.24`. Upstream downloads
  the architecture-specific library from HammerDB and verifies its SHA-256,
  architecture and SONAME. Installing an arbitrary MariaDB client package is
  not an equivalent replacement for that dependency.
- `librarycheck` must report success for PostgreSQL and MySQL. Failure for an
  intentionally absent database client must not mask the two required checks.
- Run the eventual wrapper as non-root. Scripts, credentials, certificate files,
  and writable HammerDB configuration/Jobs state need a private temporary area.

## Environment

The following names and defaults are the runner interface for backend
integration. None is an OpenEverest user-facing API field by itself.

| Variable | Default / validation |
| --- | --- |
| `DB_TYPE` | Required: `postgresql` or `mysql` |
| `DB_HOST` | Required single TCP target host |
| `DB_PORT` | 5432 for PostgreSQL or 3306 for MySQL; integer 1–65535 |
| `DB_NAME` | Required pre-created disposable database |
| `DB_USER` | Required existing account with privileges in that database |
| `DB_PASSWORD` | Required valid UTF-8; no NUL, double quotes, or leading/trailing whitespace; supplied through a Secret by the later integration |
| `BENCH_WORKLOAD` | `tproc-c`; no other workloads initially |
| `BENCH_WAREHOUSES` | 1; positive integer |
| `BENCH_VIRTUAL_USERS` | 1; positive integer <= warehouses |
| `BENCH_RAMPUP_MINUTES` | 0; nonnegative integer |
| `BENCH_DURATION_MINUTES` | 1; positive integer |
| `BENCH_INITIALIZE` | `false`; strict `true`/`false` |
| `PGSSLMODE` | PostgreSQL only: `prefer`; initially accept `disable`, `prefer`, `require` |
| `MYSQL_SSL` | MySQL only: `true`; strict `true`/`false` |

Reject malformed/overflowing numeric values and NUL bytes. Minute settings are
whole minutes; their sum must fit a Go `time.Duration`. Database/user names
initially accept only ASCII letters, digits, underscores, hyphens and dots,
because the pinned builders interpolate them into Tcl and SQL. PostgreSQL names
are limited to 63 bytes; MySQL database names to 64 and usernames to 32.
Hosts must be one IP address or DNS hostname, with the port supplied separately.
Values are not trimmed. HammerDB 6.1.1 trims surrounding password whitespace in
`diset` and does not escape double quotes in its generated worker scripts, so
those passwords are rejected before staging files. Accepted passwords are kept
unchanged. Never accept arbitrary Tcl or CLI argument passthrough.

`configurationScript` generates connection and phase settings from a validated
config. Preparation uses one schema worker; measurement uses the configured
virtual users, timed mode, explicit ramp-up/duration, fail-on-error and profiling.
Every command argument passes through `tclLiteral`. The generated text contains
credentials and must be stored privately. `phaseScript` adds database/schema
checks, workload commands, worker-status validation and an explicit exit code.
Only successful phases write their job ID to a private `.done` file. Go execution
checks the record's version, phase and job ID as well as the subprocess result;
missing, stale or malformed records fail the phase.

`writeScripts` places `run.tcl` and, when initializing, `prepare.tcl` in a fresh
`0700` directory with `0600` files. Its private `state` directory is the child's
`TMP`, `TMPDIR`, `TEMP` and `HOME`, keeping default SQLite settings, Jobs data and
logs within that invocation. The caller defers the returned cleanup function;
staging failures clean up immediately. `runStaged` passes this environment to the
CLI, removes `DB_PASSWORD` from the inherited environment, and cleans up after
success, failure or cancellation. Execution uses the CLI's directory, sends
SIGTERM to its process group on cancellation and bounds shutdown with a
five-second wait before forced cleanup. Each phase buffers bounded lines and redacts raw and Tcl-escaped passwords before
forwarding either stream to Pod logs. Oversized lines are omitted; destination
errors are retained while child output drains. `main.go` loads configuration,
locates `hammerdbcli` on PATH or in the working directory, and handles SIGTERM
and interrupt through a shared cancellation context.
Cleanup removes local files, not database data.
Phase tests use real Tcl with substituted database/CLI operations; live image
verification remains a separate step.

Tests cover password restrictions, literal encoding, engine settings and Tcl
argument round trips. The round-trip test stubs `diset`; it checks our generated
commands, not the complete upstream worker/connection path.
The round-trip test uses `tclsh`, or a required interpreter path supplied through
`HAMMERDB_TEST_TCLSH`; it skips when neither is available. The local check used
Tcl 8.6.17. Pinned HammerDB/Tcl 9 checks belong to image verification.

Map `PGSSLMODE` to `diset connection pg_sslmode`. Map `MYSQL_SSL` to
`diset connection mysql_ssl`, explicitly keep `mysql_ssl_two_way false` and clear
the default CA directory/file and client certificate/key filenames for one-way
TLS. The live MySQL preparation probe negotiated `TLS_AES_128_GCM_SHA256` using
the default `caching_sha2_password` account. A successful run must not silently
accept an empty negotiated cipher when TLS was requested. Setting `MYSQL_SSL`
false may prevent a fresh caching-SHA2 login if RSA exchange is unavailable.

These initial settings cover encryption, not verification of server identity or
mutual TLS. Certificate provisioning and `verify-ca`/`verify-full` are not claimed
by this contract. Reject unsupported TLS modes explicitly. PostgreSQL's probe
used `disable` against the disposable server; encrypted PostgreSQL connections
have not been tested in this step.

## Preparation and execution

The wrapper will optionally prepare an empty requested database, then run a timed
workload with fail-on-error enabled. It will use the supplied account throughout;
no separately generated account, role, database or password is part of this
contract. Existing data is reused only when initialization is off, the schema is
compatible and its warehouse count matches. Check both the warehouse count and
identifier range, rather than accepting missing/gapped warehouse rows blindly.
The current PostgreSQL script requires the default schema to resolve to `public`
and preparation to use the existing database owner.

PostgreSQL's `pg_superuser`/`pg_superuserpass` settings must refer to the same
supplied account; their names do not justify requiring superuser privileges.
`pg_defaultdbase` and `pg_dbase` both target the requested database. Explicitly set
`pg_vacuum false` to avoid its optional checkpoint/vacuum behavior.

The pinned PostgreSQL builder queues an `ALTER USER ... PASSWORD` for an existing
role, but its existing-empty-database branch overwrites the same queued statement
with `ALTER DATABASE ... OWNER TO`. The live probe succeeded using an existing
database owner without superuser, `CREATEDB` or `CREATEROLE`; account/password,
database inventory and ownership were unchanged. The database DDL log confirmed
an assignment to the current owner, not account creation or password changes.
This verified path needs no upstream patch. MySQL's existing-empty-database
branch similarly avoids its `CREATE DATABASE` statement.

A measured run uses the configured virtual users plus one monitor connection.
Warm-up and measured duration exclude schema preparation and startup overhead.
The later backend integration must provide a bounded execution budget beyond
`(rampupMinutes + durationMinutes) * 60` seconds; the plugin's existing five-minute
default cannot be assumed sufficient. Database objects remain after a run,
including partial objects after failed preparation; never drop them implicitly.

## Completion, logs and metrics

`buildschema` and `vurun` wait internally and return a description containing the
job ID. They catch some worker errors, so a returned job ID or CLI exit status
zero is not proof of success. Capture the current phase's worker states and Jobs
status before `vudestroy`, require all expected workers to be `FINISH SUCCESS`, and reject
missing/incomplete status. `jobs ... status` represents output records, not a
single Boolean. `jobs format JSON` changes formatting, but `jobs` prints its data;
it is not an ordinary function returning a decoded result.

Use `exit <status>` in noninteractive scripts. An offline 6.1.1 probe found that
`quit` traverses readline teardown and can raise `can't rename "::_unknown"`.
All generated scripts and diagnostic streams may contain connection material;
redact it before forwarding to stdout/stderr and remove temporary files on exit.

Return `nopm` (`orders/min`) first and `tpm` (`tx/min`) second through the existing
version-1 `/dev/termination-log` contract. TPM is engine-specific; do not label it
as pgbench/Sysbench TPS. Enable timing profiling in verification and add available
transaction-specific average latency in `ms`, without inventing a global average.
The pinned Jobs timing JSON encodes numbers as strings and exposes `avg_ms` and
percentile fields per operation. Parse finite, nonnegative numeric values explicitly.

| Native timing operation | Proposed metric name (unit `ms`) |
| --- | --- |
| `NEWORD` | `latency_new_order_avg` |
| `PAYMENT` | `latency_payment_avg` |
| `DELIVERY` | `latency_delivery_avg` |
| `OSTAT` | `latency_order_status_avg` |
| `SLEV` | `latency_stock_level_avg` |

Only measured-run results count. Invalid/missing metrics produce a generic warning
without changing independently verified benchmark success. Failed/cancelled runs
must not emit a success record. The complete termination record is limited to
4096 bytes, independently of retained display logs.

See [the shared result contract](../../docs/runner-result-contract.md) and the
[verification notes](verification.md) for the checks actually performed.
