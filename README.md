# OpenEverest Plugin Bench

OpenEverest Plugin Bench is a plugin being developed as part of the OpenEverest
PostgreSQL benchmarking MVP. It runs `pgbench` benchmarks from the OpenEverest
database cluster view, with each benchmark executing in a temporary Kubernetes
Job.

> **MVP in progress:** The benchmark MVP is under active development and is
> not intended for production use yet.

## How it works

The plugin backend coordinates a benchmark run:

1. Resolve connection details for the selected PostgreSQL instance through the
   OpenEverest API.
2. Pick the database: the one entered in the form, otherwise the instance's
   default database (the provider's `database` connection key, or the database
   in its connection URI).
3. Create a temporary Secret containing the database credentials.
4. Create a Kubernetes Job using the pgbench runner image.
5. Wait for the Job and collect the runner logs.
6. Delete the Job and temporary Secret.

The frontend is currently available as a PostgreSQL cluster detail tab. It is
built with MUI and themed from the host through `@openeverest/plugin-theme`.
Other database technologies can be added later through a driver-based design.

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
npm run build

cd backend && go test ./...
cd backend && go vet ./...

helm lint charts/plugin-bench/
docker build -t plugin-bench-pgbench:dev runners/pgbench
```

## Project structure

```text
src/                         React frontend
backend/                     Go backend
backend/internal/everest/    OpenEverest API client
backend/internal/coordinator Kubernetes Job and Secret orchestration
runners/pgbench/             pgbench runner image
charts/plugin-bench/         Helm chart and Plugin resource
dev/                         Tilt and k3d configuration
spec.md                      MVP requirements
```

## Current limitations

The MVP targets PostgreSQL instances managed by OpenEverest. Benchmark output
is collected from the runner and is not yet persisted in an external database.
Run history, richer result parsing, and support for additional database
technologies are planned for later iterations.

## License

Licensed under the Apache License 2.0. See [LICENSE](LICENSE).
