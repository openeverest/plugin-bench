# Benchmark request and runner result contracts

## Request and effective settings

Submit `POST /api/runs` with bearer authentication and
`Content-Type: application/json`. The request body must be one JSON object, at
most 16 KiB. New clients always identify the driver and provide an options object:

```json
{
  "target": {"k8sCluster": "main", "namespace": "default", "instance": "my-postgres"},
  "database": "bench",
  "driver": "pgbench",
  "options": {"durationSeconds": 60, "clients": 4, "threads": 2, "scale": 1, "initialize": false}
}
```

Use a disposable benchmark database. For pgbench, `initialize: true` drops and
recreates pgbench tables. An omitted or empty `database` uses the driver's
resolution of upstream connection details; it does not create a database.

Resources and node affinity remain optional shared fields outside `options`.
The selected driver must support the database type returned by OpenEverest;
clients cannot override that type. The configured image comes from trusted
deployment settings, never from the request.

The API checks the shared request envelope; the driver strictly decodes its
options into a typed struct and validates them. Arrays, strings, and `null` are
not options objects. Both drivers reject unknown option fields and explicitly
null fields. Omitted pgbench fields receive these defaults:

| Field | Default | Validation |
| --- | --- | --- |
| `durationSeconds` | 60 | Integer from 1 to 2147483647 |
| `clients` | 1 | Integer from 1 to 2147483647 |
| `threads` | 1 | Integer from 1 to 2147483647; no greater than clients |
| `scale` | 1 | Integer from 1 to 2147483647 |
| `initialize` | false | Boolean |

An empty options object is valid and receives all defaults. Explicit zero or
negative numeric settings fail validation. Scale remains a valid positive number
even when initialization is disabled, though it is only used for initialization.
Shared backend code resolves Kubernetes resource defaults from deployment
settings and per-run overrides.

### Sysbench MySQL requests

The Sysbench driver uses the same shared request fields with its own options:

```json
{
  "target": {"k8sCluster": "main", "namespace": "default", "instance": "my-mysql"},
  "database": "sbtest",
  "driver": "sysbench",
  "options": {"durationSeconds": 5, "threads": 2, "tables": 1, "tableSize": 1000, "initialize": true}
}
```

| Field | Default | Validation |
| --- | --- | --- |
| `durationSeconds` | 60 | Integer from 1 to 2147483647 |
| `threads` | 1 | Integer from 1 to 2147483647 |
| `tables` | 1 | Integer from 1 to 2147483647 |
| `tableSize` | 10000 | Integer from 1 to 2147483647 |
| `initialize` | false | Boolean |

An empty options object applies all defaults. Explicit zero, negative values,
nulls, and fields such as `clients`, `scale`, or `workload` are rejected. The
runner currently fixes the database driver to MySQL and uses the bundled
`oltp_read_write` workload; these are not selectable options.

The authorized OpenEverest credentials must report database type `mysql`, matched
case-insensitively after trimming. The provider name does not select the driver.
The API therefore also accepts `provider-mariadb` and `percona-xtradb-cluster`
targets reporting `mysql`; compatibility with those providers has not been verified.
Clients must explicitly send `driver: "sysbench"`; omitting it does not infer
Sysbench from the target. The legacy flat envelope remains pgbench-only.

The driver resolves the database in this order:

1. A nonblank `database` supplied in the request.
2. A nonblank database field in the upstream credentials.
3. A database path in the upstream `mysql://` URI.

If no database is available, the request returns `400` asking for a name.
Structured host, port, username, and password remain required: the URI supplies
only a database hint, and URI-only credentials return `422`. Invalid structured
credentials, malformed URI hints, or values incompatible with the runner's
configuration file return `502`. Request database encoding errors return `400`.
See [runner credential constraints](../runners/sysbench/README.md#credentials).

The database and account must already exist. With `initialize: true`, the runner
performs `prepare` using one worker before the measured phase. Existing table
names fail preparation, and benchmarking is skipped. With `initialize: false`,
the expected tables must already exist; keep `tables` and `tableSize` consistent
with them because both settings control generated queries. Both remain positive
required effective values even when initialization is disabled. Preparation may
leave partial tables after failure; the runner never automatically drops them.
Job and Secret cleanup does not remove database tables.

`durationSeconds` limits the measured phase. Scheduling, image startup, and
preparation also consume the shared overall execution timeout. Resources and
node affinity work identically for both drivers. The password is supplied through
a per-run Secret reference; other connection fields and benchmark settings use
ordinary environment values. Effective options and snapshots contain no credentials.

The chart enables Sysbench by default using its published runner image tagged
with the chart's `appVersion`. Administrators can disable it or override the image;
Tilt builds and injects a local image. Release builds target AMD64 and ARM64, with
image integration tests on AMD64. MySQL UI/provider exposure is a separate follow-up.

### Admission and stored settings

Successful admission returns `202` with `id` and `status: "running"`. Running
includes time spent awaiting scheduling or initialization. Invalid requests,
invalid options, and unknown/disabled drivers return `400`; a known driver that
does not support the target database type returns `422`. Unavailable service
dependencies return `503`; these are distinct from invalid driver selection.

For compatibility, deprecated requests with the flat pgbench fields
`durationSeconds`, `clients`, `threads`, `scale`, and `initialize` remain accepted
when both `driver` and `options` are absent. They are adapted to pgbench before
preparation. Legacy numeric omissions remain zero and fail validation, preserving
the old requirement to supply them. Do not mix flat fields with the new envelope:
mixed payloads and unknown top-level fields return `400`.

The run record retains the driver ID, effective database and options after
defaulting/validation, configured runner image reference, effective resources,
and prepared affinity. `GET /api/runs/{id}` exposes driver, image, and options as
top-level `driver`, `runnerImage`, and `options` fields after checking access to
the stored target. Credentials are kept out of snapshots and API responses.
Image provenance is the configured reference, not a resolved registry digest;
mutable tags do not guarantee identical runner binaries across runs.

## Runner transport and wire format

Each runner writes one final JSON record to `/dev/termination-log` after obtaining
its measurements. Both pgbench and Sysbench write it after a successful,
uncancelled benchmark and after measured stdout has been drained. The record
uses this version-1 format:

```json
{"version":1,"metrics":[{"name":"tps","unit":"tx/s","value":1234.5},{"name":"latency_avg","unit":"ms","value":3.2}]}
```

- The complete record must fit within 4096 bytes, Kubernetes' per-container
  termination-message limit. Larger records may be truncated and become invalid.
- `version` must be 1 and `metrics` must be a nonempty array.
- Each metric requires a nonblank string `name`, nonblank string `unit`, and a
  finite numeric `value`. Zero is valid; a missing or null value is invalid.
- `(name, unit)` pairs must be unique. Different tools may use different names
  and units; shared code does not enforce pgbench-specific names or positive values.
- Unknown fields, malformed JSON, trailing JSON/text, unsupported versions,
  invalid metrics, or oversized messages invalidate the entire record.

The runner Job explicitly sets `terminationMessagePath: /dev/termination-log`
and `terminationMessagePolicy: File`. Keep `File`: `FallbackToLogsOnError` can
substitute ordinary log text when a failed container leaves the file empty.
Kubernetes exposes the file through
`pod.Status.ContainerStatuses[].State.Terminated.Message`.

The coordinator reads only the current terminated container named `benchmark-runner`.
Sidecar messages and previous termination states are ignored. It extracts metrics
before collecting logs and deleting the Job/Secret, so log truncation, log-read
errors, or cleanup errors do not discard metrics already collected.

Tool-specific parsing belongs inside each runner image. The pgbench runner
extracts TPS without initial connection time and average latency from its own
summary. The Sysbench runner extracts transaction throughput and average latency
from the final SQL statistics and latency sections of `oltp_read_write`, excluding
preparation output and intermediate reports. It emits `tps` in `tx/s` and
`latency_avg` in `ms`. Shared names and units do not make results from different
workloads or engines directly comparable. Shared backend code only decodes named
numbers with units. Multi-step operations such as initialization,
prepare/run/cleanup, or load/run belong inside
the runner; the coordinator manages one Job per benchmark run.

## API availability and output

Selected fields from a completed `GET /api/runs/{id}` response look like:

```json
{
  "status": "succeeded",
  "driver": "pgbench",
  "runnerImage": "ghcr.io/openeverest/plugin-bench-pgbench:0.1.0",
  "options": {"durationSeconds": 60, "clients": 4, "threads": 2, "scale": 1, "initialize": false},
  "output": "...native pgbench output...",
  "outputTruncated": false,
  "metrics": [{"name":"tps","unit":"tx/s","value":1234.5},{"name":"latency_avg","unit":"ms","value":3.2}]
}
```

The wire version belongs to the termination record; the API stores and returns
the decoded metric array. Logs remain separate, with a 1 MiB collection limit
and `outputTruncated` flag. This limit does not truncate termination metrics.

Missing or invalid records mean metrics are unavailable. Running records and
terminal runs without available metrics omit `metrics`; this alone never changes
execution status or removes log output. Older runner images remain usable.
Both runners warn generically if metrics cannot be produced/written and preserve
their benchmark exit status; failed/cancelled runs do not write a success record.
Shared collection can retain valid metrics from another runner even if the Job
fails. Log or cleanup errors can still make execution fail while
leaving previously collected metrics available.

Before storing results, the API redacts logs using Secret data and additional
sensitive representations supplied by the driver. Metrics whose names or units
contain those values are dropped rather than renamed; accepted numeric values
are preserved. If none remain, `metrics` is omitted. Raw termination messages are
not logged or returned.

History remains in backend memory. Restarts lose records, completed runs become
unavailable after one hour, and running records do not expire. Expired completed
records are removed from memory on subsequent creation. Durable storage,
comparison charts, and a metric taxonomy are separate follow-up work.

## Frontend display and compatibility

The benchmark tab submits explicit `driver` and `options` fields. Its shared form
renders the selected driver's options component; pgbench is currently the only
registered UI driver. Sysbench backend support does not add a MySQL form or
expose MySQL providers in the plugin manifest. The UI explicitly selects a
30-second run and initialization enabled by default, while the API defaults
above apply to omitted fields.

Result cards display the stored driver, configured image reference, and effective
options without interpreting a driver's option schema. Completed runs show metric
names, values, and units in a separate table. Raw logs are collapsed initially and
can be expanded, with truncation clearly identified. Failed runs can show available
metrics and logs alongside their error.

Older status responses without snapshots or metrics remain readable. The client
omits malformed optional metadata and treats an invalid or empty metric set as
unavailable, including duplicate `(name, unit)` pairs or non-finite values. It does
not invent zero values or change the execution status. Core status fields, saved
resources, and affinity still undergo the existing response validation. The UI
shows metrics availability only after a run finishes.
