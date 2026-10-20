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

Use a disposable benchmark database. `initialize: true` drops and recreates
pgbench tables. An omitted or empty `database` uses the driver's resolution of
upstream connection details; it does not create a database.

Resources and node affinity remain optional shared fields outside `options`.
The selected driver must support the database type returned by OpenEverest;
clients cannot override that type. The configured image comes from trusted
deployment settings, never from the request.

The API checks the shared request envelope; the driver strictly decodes its
options into a typed struct and validates them. Arrays, strings, and `null` are
not options objects. Unknown option fields and explicitly null pgbench fields
are rejected. Omitted pgbench fields receive these defaults:

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
its measurements. For pgbench, this happens after a successful benchmark and
after its stdout has been drained. The record uses this version-1 format:

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
summary. Shared backend code only decodes named numbers with units. Multi-step
operations such as initialization, prepare/run/cleanup, or load/run belong inside
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
The pgbench runner warns generically if metrics cannot be produced/written and
preserves its benchmark exit status; failed/cancelled pgbench runs do not write a
success record. Shared collection can retain valid metrics from another runner
even if the Job fails. Log or cleanup errors can still make execution fail while
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
registered UI driver. The UI explicitly selects a 30-second run and initialization
enabled by default, while the API defaults above apply to omitted fields.

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
