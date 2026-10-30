# OpenEverest Plugin Bench

OpenEverest Plugin Bench runs PostgreSQL (pgbench) and MySQL (Sysbench)
benchmarks as temporary Kubernetes Jobs from the database cluster tab or backend API.
Both drivers are enabled by default in the Helm chart.

> **MVP complete:** The PostgreSQL pgbench workflow supports configuring,
> launching, and inspecting benchmark runs.

## How it works

The plugin backend coordinates a benchmark run:

1. Resolve authorized connection details for the selected instance through the
   OpenEverest API.
2. Pick the database: the one entered in the form, otherwise the instance's
   default database (the provider's `database` connection key, or the database
   in its connection URI).
3. Select the requested driver, validate its options and connection details,
   and create a temporary Secret containing its password.
4. Create a Kubernetes Job using the configured driver image and prepared settings.
5. Wait for the Job and collect the runner logs and optional structured metrics.
6. Delete the Job and temporary Secret.

The benchmark tab is built with MUI and themed from the host through
`@openeverest/plugin-theme`.
The backend uses a shared driver interface with pgbench and Sysbench implementations.
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
`storage`, `message`, and a sorted `drivers` array of enabled driver IDs, such as
`["pgbench", "sysbench"]`. Disabled drivers are excluded. The former PostgreSQL-only
`database` field has been removed. This endpoint does not test database
connectivity or runner-image availability.

See [the request and runner result contracts](docs/runner-result-contract.md)
for payloads, defaults, compatibility, and metrics transport details.

### MySQL benchmarks through the API

Sysbench is enabled by default in Helm. Submit an explicit `driver: "sysbench"`
with the OpenEverest target and Sysbench options to `POST /api/runs`:

```json
{
  "target": {"k8sCluster": "main", "namespace": "default", "instance": "my-mysql"},
  "database": "sbtest",
  "driver": "sysbench",
  "options": {"durationSeconds": 5, "threads": 2, "tables": 1, "tableSize": 1000, "initialize": true}
}
```

The target must report database type `mysql`; its database and account must
already exist. Poll `GET /api/runs/{id}` with the accepted ID and caller
authorization. See the [request contract](docs/runner-result-contract.md#sysbench-mysql-requests)
for defaults and connection rules, and the [runner README](runners/sysbench/README.md)
for standalone usage and table cleanup.

### Using the benchmark tab

Open a supported instance's **Performance Benchmark** tab. Its provider determines
which driver is offered:

| Instance provider | Driver |
| --- | --- |
| `provider-cloudnative-pg`, `provider-percona-postgresql` | pgbench |
| `percona-server-mysql`, `percona-xtradb-cluster` | Sysbench |

The tab checks `/api/status` and only offers drivers enabled on the backend.
If none are enabled for the provider, run lookup remains available.

Database, **Job resources**, and **Job placement** are shared settings. A blank
database uses the instance default when available; otherwise enter an existing
database name. Use a disposable benchmark database.
For both `percona-server-mysql` and PXC (`percona-xtradb-cluster`), a database name
is required because neither provider supplies a default. Create the database
before starting the benchmark; table preparation does not create it.

**pgbench** starts with 30 seconds, one client/thread, scale 1, and initialization
enabled. Initialization drops and recreates pgbench tables. When reusing tables,
scale is disabled and displayed as **From existing tables** in the run result.

**Sysbench** starts with 30 seconds, one thread, one table, 10,000 rows per table,
and preparation on. It runs `oltp_read_write`. **Prepare Sysbench tables** creates
a fresh dataset; existing table names cause preparation to fail. To reuse
tables, turn preparation off and match **Tables** and **Rows per table** to the
existing dataset. Tables remain after Job cleanup. Sysbench must be enabled with
an accessible runner image; see [Driver configuration](#driver-configuration).

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
observations resets it. It does not limit benchmark runtime or image downloading.
Choose a period that allows enough time for autoscalers to provision nodes, or
set `"0"` to disable early failure and skip the extra Pod lookup on each Job poll.

`BENCHMARK_EXECUTION_TIMEOUT` (default `5m`) bounds the whole run, including
scheduling, image startup, connection setup, and table preparation. The duration
option limits only the measured benchmark phase.

## Requirements

For local development, install:

- Docker
- kubectl
- Helm
- k3d
- Tilt
- Go
- Node.js and npm

An OpenEverest installation and a provider for the selected database type are
required when running the plugin in a cluster. Sysbench matches database type
`mysql` rather than provider name. A live benchmark against a PXC
(`percona-xtradb-cluster`) instance has been verified. The API also accepts
`provider-mariadb` targets reporting `mysql`, but MariaDB compatibility has not
been verified. The Sysbench runner has been verified against MySQL 8.4 on Linux/ARM64.

## Local development

Create the development cluster and start the Tilt environment:

```bash
make dev-up
```

If the cluster already exists, run:

```bash
tilt up -f dev/Tiltfile
```

Tilt builds the frontend, backend image, and both pgbench and Sysbench runner
images. It injects each runner's repository and tag
through `drivers.<id>.image.repository` and `drivers.<id>.image.tag`. Runner images
are used when the coordinator starts benchmark Jobs; no published runner image
is needed for this flow.

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
  sysbench:
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

Both drivers are enabled by default and use their published release images.
To use a custom Sysbench image, override its repository or tag:

```yaml
drivers:
  sysbench:
    enabled: true
    image:
      repository: registry.example.com/team/plugin-bench-sysbench
      tag: tested-version
      pullPolicy: IfNotPresent
```

The example registry and tag are placeholders for an image you supply. Empty
Sysbench image fields resolve to
`ghcr.io/openeverest/plugin-bench-sysbench:<appVersion>` and `IfNotPresent`;
the release workflow publishes the matching image alongside the chart.

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

Helm serializes the resolved settings into one backend environment variable.
For a chart with `appVersion: 0.2.0`:

```json
{"pgbench":{"enabled":true,"image":"ghcr.io/openeverest/plugin-bench-pgbench:0.2.0","imagePullPolicy":"IfNotPresent"},"sysbench":{"enabled":true,"image":"ghcr.io/openeverest/plugin-bench-sysbench:0.2.0","imagePullPolicy":"IfNotPresent"}}
```

The variable is named `BENCHMARK_DRIVERS`. Each entry requires an explicit
`enabled` boolean. Enabled entries require a nonempty image reference, and pull
policy must be `Always`, `IfNotPresent`, or `Never` (omitted/empty defaults to
`IfNotPresent`). Only drivers compiled into the backend can be enabled; adding
a Helm entry does not install an implementation. The compiled drivers are
pgbench and Sysbench. At least one must remain enabled; disabling pgbench is
valid when Sysbench is enabled with a usable image.

For deployments setting environment variables directly, `RUNNER_IMAGE` and
`RUNNER_IMAGE_PULL_POLICY` remain deprecated pgbench fallbacks **only when
`BENCHMARK_DRIVERS` is absent**. In that legacy mode, `RUNNER_IMAGE` is required
and pull policy defaults to `IfNotPresent`. A present payload is authoritative:
empty/malformed JSON, unknown fields, unknown enabled drivers, or no enabled
drivers stop startup instead of falling back to legacy variables. Explicit
disablement is never reversed by a legacy image setting.

## Runner settings

The pgbench runner receives its PostgreSQL connection through these variables:

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

The Sysbench runner receives `MYSQL_HOST`, `MYSQL_PORT`, `MYSQL_DATABASE`,
`MYSQL_USER`, and `MYSQL_PASSWORD`, plus `BENCH_DURATION`, `BENCH_THREADS`,
`BENCH_TABLES`, `BENCH_TABLE_SIZE`, and `BENCH_INITIALIZE`. The backend supplies
the password through a Kubernetes Secret reference; ordinary connection and
benchmark settings use environment values. See the
[Sysbench configuration reference](runners/sysbench/README.md#configuration)
for defaults and credential-file encoding rules.

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
docker build -t plugin-bench-sysbench:dev runners/sysbench
```

Both PR CI and release builds run these frontend test suites before building the
bundle. Component tests include the complete submission/polling/results and run
lookup workflows with mocked API responses, including concurrent cards and
cancellation on unmount or target changes.

For a live check after starting Tilt, use a disposable PostgreSQL or supported
MySQL database:

1. Start a short run and confirm the POST contains `driver` and nested `options`.
2. Confirm polling reaches a terminal status and stops. Check effective options,
   metrics, and expandable logs; look up the same ID and confirm no duplicate card.
3. Start two runs and confirm both cards retain their own status and results.
4. Leave the tab during a run and confirm status requests stop. A missing run ID
   should pause status checks with a clear error rather than resubmit a benchmark.

For Sysbench, prepare a small fresh dataset, then repeat with preparation off
and matching table settings. Preparing the same tables again should fail clearly.

## Releasing

Push a `vX.Y.Z` tag, or `vX.Y.Z-<pre-release>` for a pre-release, to run the
release workflow. It publishes:

- `ghcr.io/openeverest/plugin-bench`, `ghcr.io/openeverest/plugin-bench-pgbench`,
  and `ghcr.io/openeverest/plugin-bench-sysbench` images for `linux/amd64` and `linux/arm64`
- the `oci://ghcr.io/openeverest/charts/plugin-bench` Helm chart
- a GitHub release with the chart archive and release notes

The workflow tests both runners from their built AMD64 archives before publishing.
Release notes include each image reference and digest.

GHCR creates new packages as private, so the first release fails at the
"Verify public access" step. Make each new package, including
`plugin-bench-sysbench`, public in its GHCR package settings, then re-run the failed
job. Publishing is idempotent, so re-runs are safe.

## Project structure

```text
src/                         React frontend
backend/                     Go backend
backend/internal/everest/    OpenEverest API client
backend/internal/driver/     Driver contracts, registry, pgbench/Sysbench preparation
backend/internal/coordinator Kubernetes Job and Secret orchestration
backend/internal/runnerresult Generic runner metrics decoder
runners/pgbench/             pgbench runner image
runners/sysbench/            MySQL Sysbench runner image
charts/plugin-bench/         Helm chart and Plugin resource
dev/                         Tilt and k3d configuration
spec.md                      MVP requirements
```

## Current limitations

The UI supports the providers listed above; MySQL requires an enabled Sysbench
driver and a usable image. Additional workloads and MongoDB remain future work.
Run records, including output and metrics, are held in backend memory and are
lost on restart.
Completed records become unavailable one hour after completion; running records
do not expire. Expired records are removed from memory when a new run is created.
Keep one backend replica until shared storage and cross-pod coordination exist.
Persistent history and comparisons remain future work.

## License

Licensed under the Apache License 2.0. See [LICENSE](LICENSE).
