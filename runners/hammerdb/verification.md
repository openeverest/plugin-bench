# HammerDB 6.1.1 verification

Step 1 checks the upstream tool before implementing our wrapper. These are
standalone Docker/CLI checks, not tests of a completed plugin driver.

## Provenance

- Upstream source: tag `v6.1.1`, including `config/postgresql.xml`,
  `config/mysql.xml`, `src/postgresql/pgoltp.tcl`, `src/mysql/mysqloltp.tcl`,
  `modules/jobs-1.0.tm`, and the base/database-client Dockerfiles.
- Live CLI inspection used the locally available combined image identified by
  `sha256:25b27a98198cd8c6ccbc8f60795e48627bff3540729c62ec4281c2aefde9dbe4`.
  Its banner reports `HammerDB CLI v6.1.1`; it runs Tcl 9.0.2.
  This image is an upstream verification tool, not the planned minimal runner.
- The `v6.1.1-base` manifest index was inspected separately and advertises both
  AMD64 and ARM64. No native ARM64 workload execution is claimed by this check.

## Confirmed source and CLI behavior

- Both `mysqltcl` and `Pgtcl` load successfully. The combined image's missing
  Db2 client is unrelated to these checks.
- `buildschema` and `vurun` wait for their respective phase and return a string
  containing the job ID. Preserve that phase ID before another command replaces
  the global `jobid`.
- `vustatus` prints a dictionary of workers; the Tcl global `vustatus` retains
  their states before destruction. `jobs ... status` returns a sequence of
  virtual-user IDs and recorded output messages, not a scalar status.
- `jobs format JSON` changes output formatting. `jobs <id> result` prints a
  JSON array containing job ID, timestamp and result text; the throughput numbers
  are still embedded in the final result string. The wrapper must parse that
  pinned format rather than assume it is already a numeric metrics object.
- Timing rows use operation names and `avg_ms` plus other timing fields. A
  syntactically valid JSON response can still mean "no timing data" and must not
  be accepted as actual latency.
- Automatic scripts should use explicit `exit <status>`. `quit` raised a
  readline teardown error in the offline CLI probe.
- The PostgreSQL builder can queue role/database changes. Its ordinary existing,
  empty, owned-database branch overwrites the queued password statement with an
  owner statement; checking that whole execution path matters more than finding
  one `ALTER USER` string in the source.
- MySQL checks for an existing empty database and avoids its database creation
  path when one exists. It does not clean up a partially prepared schema for us.

## Live database probes

The disposable probes use PostgreSQL 17 and MySQL 8.4, a pre-created database
`hdb_probe`, an existing account `hdb_user`, one warehouse, one workload user,
zero warm-up minutes and one measured minute with timing profiling enabled.

PostgreSQL's account owns the target database and has `NOSUPERUSER NOCREATEDB
NOCREATEROLE`. Both administrative-named and benchmark connection settings refer
to that same account/database; `pg_vacuum` is false. MySQL's account is granted
privileges only on the two disposable probe databases. It retains MySQL 8.4's
default authentication plugin; the script requests one-way TLS with the default
CA/client certificate filenames cleared.

Before/after checks compare the account's privilege flags and password verifier,
database inventory and ownership. PostgreSQL DDL logging also records account
and database statements. Only sanitized output/result fixtures belong in the
repository; password verifiers and generated credential-bearing scripts do not.

The first attempt stalled during MySQL readiness under host memory pressure.
Sequential PostgreSQL probes then completed. MySQL authenticated over TLS and
began schema loading, but preparation exceeded its 360-second probe budget.
Testing was stopped at the user's request; retain that timeout rather than retry.

| Probe | PostgreSQL | MySQL |
| --- | --- | --- |
| Prepare existing database using supplied account | Passed | Timed out during loading |
| One-minute measured run with profiling | Passed | Not reached |
| Non-empty preparation, wrong password, missing schema | Failed workers correctly reported; CLI still exited zero | Not reached |
| Account/password and database metadata unchanged | Verified | No completed before/after comparison |
| TLS with default MySQL authentication | Not applicable | Negotiated `TLS_AES_128_GCM_SHA256` |

The PostgreSQL preparation took approximately 262 seconds and the subsequent run
82 seconds on this host, including CLI startup/collection. Those are functional
probe timings, not performance baselines. Native PostgreSQL phase/result/timing
fixtures are in `testdata/tprocc-postgresql-6.1.1.json`. No MySQL success fixture
or successful MySQL workload verification is claimed.
