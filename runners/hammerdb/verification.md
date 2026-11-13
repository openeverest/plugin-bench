# HammerDB provenance and behavior

The runner targets upstream HammerDB `v6.1.1`. Its Dockerfile pins the base
and MySQL client image indexes and installs PostgreSQL `libpq` 17. Relevant
upstream sources include `config/{postgresql,mysql}.xml`,
`src/{postgresql/pgoltp,mysql/mysqloltp}.tcl`, `src/generic/gentpcc.tcl`, and
`modules/jobs-1.0.tm`.

- `buildschema` and `vurun` return a job ID after their phase. The runner
  preserves it before another command replaces the global `jobid`.
- CLI exit status alone does not establish success. The runner checks worker
  states and job output before writing a completion record.
- `jobs format JSON` wraps throughput text in a JSON array; NOPM and TPM still
  require parsing. Optional timing records contain transaction names and
  `avg_ms`; empty timing data does not establish a latency measurement.
- Passwords pass through `diset`, `quotemeta`, and generated Tcl templates.
  The runner encodes them for both quoted and unquoted contexts and disables
  connection pooling, whose upstream path does not decode those values.
- Preparation uses an existing database and account. Failed preparation can
  leave partial schema objects; cleanup does not remove benchmark data.
- MySQL's default socket setting must remain populated because clearing it
  removes an argument from an upstream generated call. A remote host selects TCP.

The sanitized PostgreSQL phase/result/timing fixture is in
`testdata/tprocc-postgresql-6.1.1.json`. Reproduce image and live database checks
using the commands in the [runner README](README.md).
