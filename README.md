# OpenEverest Plugin Bench

OpenEverest Plugin Bench provides PostgreSQL benchmarking for OpenEverest.
It runs `pgbench` benchmarks from the OpenEverest
database cluster view, with each benchmark executing in a temporary Kubernetes
Job.

> **MVP complete:** The PostgreSQL pgbench workflow supports configuring,
> launching, and inspecting benchmark runs. Additional drivers and features
> are planned for future releases.

## How it works

The plugin backend coordinates a benchmark run:

1. Resolve connection details for the selected PostgreSQL instance through the
   OpenEverest API.
2. Pick the database: the one entered in the form, otherwise the instance's
   default database (the provider's `database` connection key, or the database
   in its connection URI).
3. Create a temporary Secret containing the database credentials.
4. Create a Kubernetes Job using the pgbench runner image.
5. Wait for the Job and collect the runner logs and optional structured metrics.
6. Delete the Job and temporary Secret.

The frontend is currently available as a PostgreSQL cluster detail tab. It is
built with MUI and themed from the host through `@openeverest/plugin-theme`.
The backend uses a shared driver interface; pgbench is the current implementation.
Additional benchmark tools can supply their own option validation and workload
preparation while sharing the Kubernetes execution lifecycle.

### Backend API and results

New clients submit `driver` and a driver-specific `options` object to
`POST /api/runs`. Target, database, resources, and node affinity remain shared
request fields. The driver applies defaults, validates options, and records the
effective configuration and configured runner image for later inspection.

`GET /api/runs/{id}` returns status, effective settings, bounded log output, and
optional numeric metrics. Access requires authorization for the stored target.
Missing or invalid metrics do not turn a successful benchmark into a failed run.
The frontend submits the explicit driver/options envelope and displays these
effective settings and metrics.

`GET /api/status` returns generic backend readiness with `status`, `runnerReady`,
`storage`, and `message`. The former PostgreSQL-only `database` field has been
removed. This endpoint does not test database connectivity or runner-image
availability; an individual run's `driver` identifies its benchmark tool.

See [the request and runner result contracts](docs/runner-result-contract.md)
for payloads, defaults, compatibility, and metrics transport details.

### Using the benchmark tab

Open a PostgreSQL instance's **Performance Benchmark** tab. Choose a benchmark
driver and configure its options; pgbench is currently the only available driver.
Database, **Job resources**, and **Job placement** are shared settings. Leaving
the database empty uses the instance's default database.

The pgbench form starts with 30 seconds, one client/thread, scale 1, and
initialization enabled. These are explicit UI choices, distinct from the API's
defaults for omitted options. Initialization drops and recreates pgbench tables,
so use a disposable benchmark database. When reusing existing tables, scale is
disabled and the frontend sends a valid fallback of 1; the run's effective
options show scale as **From existing tables**, since pgbench uses the scale of
the tables it finds.

Each accepted run has its own card and polls for status every two seconds until
completion. Completed cards show the driver, configured runner image, effective
options, and a metrics table. **Raw logs** expands the captured output; a
**Truncated** badge identifies output that reached the backend limit. Missing or
invalid metrics show **Structured metrics unavailable** while preserving the
run's status and logs. Failed runs show their error alongside any available results.

Use **Look up a benchmark run** to retrieve a stored run by ID. Looking up an
already displayed ID does not add a duplicate card. Records remain subject to the
in-memory retention described under [Current limitations](#current-limitations).

### Runner placement

Administrators can pin every runner Job to specific nodes, such as a tainted,
dedicated benchmark node pool, with `runner.nodeSelector` and `runner.tolerations`
in Helm:

```yaml
runner:
  nodeSelector:
    node-role/benchmark: "true"
  tolerations:
    - key: dedicated
      operator: Equal
      value: benchmark
      effect: NoSchedule
```

Invalid values stop the backend at startup. Users can additionally require node
labels for a single run under **Job placement** in the UI. Those rules are sent as
`nodeAffinity` and apply on top of the deployment's node selector, so a node must
satisfy both.

### Scheduling failures

While waiting for a Job, the coordinator checks the runner Pod's scheduling
condition. If the same Pod is continuously observed as `PodScheduled=False` with
reason `Unschedulable` for `runner.schedulingGracePeriod` (default `2m`, or
`BENCHMARK_SCHEDULING_GRACE_PERIOD`), the run fails and its Job and temporary
credential Secret are cleaned up. Users see a generic "could not be scheduled"
message; the scheduler's explanation, which reveals node counts and taints, is
written to the backend log.

The grace period starts at the first observation of scheduler rejection, not at
submission. Scheduling recovery, Pod replacement, or a gap in scheduling
observations resets it. It does not limit pgbench runtime or image downloading.
Choose a period that allows enough time for autoscalers to provision nodes, or
set `"0"` to disable early failure and skip the extra Pod lookup on each Job poll.

The existing `BENCHMARK_EXECUTION_TIMEOUT` (default `5m`) still bounds the whole
run, and time spent waiting to be scheduled counts against it. A run that waits
long for a node therefore has less time left to benchmark.

## Requirements

For local development, install:

- Docker
- kubectl
- Helm
- k3d
- Tilt
- Go
- Node.js and npm

An OpenEverest installation and a supported PostgreSQL provider are required
when running the plugin in a cluster.

## Local development

Create the development cluster and start the Tilt environment:

```bash
make dev-up
```

If the cluster already exists, run:

```bash
tilt up -f dev/Tiltfile
```

Tilt builds the frontend, coordinator image, and pgbench runner image. The
runner image is used only when the coordinator starts a benchmark Job.
Tilt injects that local image through `drivers.pgbench.image.repository` and
`drivers.pgbench.image.tag`; no published runner image is needed for this flow.

## Driver configuration

Configure enabled benchmark drivers and their runner images through Helm:

```yaml
drivers:
  pgbench:
    enabled: true
    image:
      repository: ""
      tag: ""
      pullPolicy: ""
```

Empty image fields use the defaults below after any legacy pgbench overrides.
With no overrides, pgbench uses
`ghcr.io/openeverest/plugin-bench-pgbench:<appVersion>` and `IfNotPresent`.
`appVersion` comes from the chart being installed, including metadata set by
`helm package --app-version` during a release.

| Image field | First choice | Deprecated pgbench fallback | Default |
| --- | --- | --- | --- |
| Repository | `drivers.<id>.image.repository` | `runner.image.repository` | `ghcr.io/openeverest/plugin-bench-<id>` |
| Tag | `drivers.<id>.image.tag` | `runner.image.tag` | Chart `appVersion` |
| Pull policy | `drivers.<id>.image.pullPolicy` | `runner.image.pullPolicy` | `IfNotPresent` |

Precedence applies independently to each field. For example, this pins just the
pgbench runner version while inheriting its repository and pull policy:

```yaml
drivers:
  pgbench:
    image:
      tag: "0.1.0"
```

Legacy `runner.image` overrides apply only to pgbench. Migrate them to
`drivers.pgbench.image` when updating your values. Shared resources, timeouts,
node selectors, and tolerations remain under `runner`; the backend Deployment's
own image remains under the top-level `image` settings.

Helm serializes the resolved settings into one backend environment variable:

```json
{"pgbench":{"enabled":true,"image":"ghcr.io/openeverest/plugin-bench-pgbench:0.1.0","imagePullPolicy":"IfNotPresent"}}
```

The variable is named `BENCHMARK_DRIVERS`. Each entry requires an explicit
`enabled` boolean. Enabled entries require a nonempty image reference, and pull
policy must be `Always`, `IfNotPresent`, or `Never` (omitted/empty defaults to
`IfNotPresent`). Only drivers compiled into the backend can be enabled; adding
a Helm entry does not install an implementation. Currently only pgbench is
available, so disabling it leaves no enabled drivers and stops backend startup.

For deployments setting environment variables directly, `RUNNER_IMAGE` and
`RUNNER_IMAGE_PULL_POLICY` remain deprecated pgbench fallbacks **only when
`BENCHMARK_DRIVERS` is absent**. In that legacy mode, `RUNNER_IMAGE` is required
and pull policy defaults to `IfNotPresent`. A present payload is authoritative:
empty/malformed JSON, unknown fields, unknown enabled drivers, or no enabled
drivers stop startup instead of falling back to legacy variables. Explicit
disablement is never reversed by a legacy image setting.

## Runner settings

The runner receives its PostgreSQL connection through these variables:

```text
PGHOST
PGDATABASE
PGUSER
PGPASSWORD or PGPASSFILE
```

Benchmark options are configured with:

```text
BENCH_INITIALIZE   default false
BENCH_DURATION     default 60 seconds
BENCH_CLIENTS      default 1
BENCH_THREADS      default 1
BENCH_SCALE        default 1
```

`BENCH_INITIALIZE=true` drops and recreates the pgbench tables. Use it only
with a disposable database. See [runners/pgbench/README.md](runners/pgbench/README.md)
for detailed runner instructions.

## Useful commands

```bash
npm ci
npm run test:form
npm run test:components
npm run test:submission
npm run test:status
npm run test:polling
npm run test:result
npm run build

cd backend && go test ./...
cd backend && go vet ./...

helm lint charts/plugin-bench/
docker build -t plugin-bench-pgbench:dev runners/pgbench
```

Both PR CI and release builds run these frontend test suites before building the
bundle. Component tests include the complete submission/polling/results and run
lookup workflows with mocked API responses, including concurrent cards and
cancellation on unmount or target changes.

For a live check after starting Tilt, use a disposable PostgreSQL database:

1. Start a short run and confirm the POST contains `driver` and nested `options`.
2. Confirm polling reaches a terminal status and stops. Check effective options,
   metrics, and expandable logs; look up the same ID and confirm no duplicate card.
3. Start two runs and confirm both cards retain their own status and results.
4. Leave the tab during a run and confirm status requests stop. A missing run ID
   should pause status checks with a clear error rather than resubmit a benchmark.

## Releasing

Push a `vX.Y.Z` tag, or `vX.Y.Z-<pre-release>` for a pre-release, to run the
release workflow. It publishes:

- `ghcr.io/openeverest/plugin-bench` and `ghcr.io/openeverest/plugin-bench-pgbench`
  images for `linux/amd64` and `linux/arm64`
- the `oci://ghcr.io/openeverest/charts/plugin-bench` Helm chart
- a GitHub release with the chart archive and release notes

GHCR creates new packages as private, so the first release fails at the
"Verify public access" step. Make each new package public in its GHCR package
settings, then re-run the failed job. Publishing is idempotent, so re-runs are
safe.

## Project structure

```text
src/                         React frontend
backend/                     Go backend
backend/internal/everest/    OpenEverest API client
backend/internal/driver/     Driver contracts, registry, and pgbench preparation
backend/internal/coordinator Kubernetes Job and Secret orchestration
backend/internal/runnerresult Generic runner metrics decoder
runners/pgbench/             pgbench runner image
charts/plugin-bench/         Helm chart and Plugin resource
dev/                         Tilt and k3d configuration
spec.md                      MVP requirements
```

## Current limitations

The MVP targets PostgreSQL instances managed by OpenEverest. Run records,
including output and metrics, are held in backend memory and are lost on restart.
Completed records become unavailable one hour after completion; running records
do not expire. Expired records are removed from memory when a new run is created.
Keep one backend replica until shared storage and cross-pod coordination exist.
Persistent history, comparisons, and additional database technologies remain
future work.

## License

Licensed under the Apache License 2.0. See [LICENSE](LICENSE).
