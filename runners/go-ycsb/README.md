# go-ycsb runner qualification

Configuration validation and fixed-workload execution are implemented; image
packaging follows in a later step. This records qualification of the unmodified
upstream tool on 2026-11-03.

**Only the `ycsb` database is supported.** `--table` selects the collection.
A different URI database is ignored by this upstream version. Wrapper configuration
rejects that setting. The runner relies on upstream connection checks and
load/run operation-error detection to report inaccessible targets.

## Runner configuration

| Environment variable | Default |
| --- | --- |
| `MONGODB_URI` | Required; `mongodb://` with no database path or `/ycsb` |
| `BENCH_COLLECTION` | `usertable` |
| `BENCH_RECORD_COUNT` | `10000` |
| `BENCH_OPERATION_COUNT` | `20000` |
| `BENCH_THREADS` | `1` |
| `BENCH_INITIALIZE` | `false` |

The URI and its authentication settings are preserved. Numeric values must be
positive integers up to 2147483647; threads cannot exceed operation count.
Initialization accepts only `true` or `false`. Collection names cannot be blank,
start with `system.`, contain `$`, control characters or empty dot segments, or
produce a `ycsb.<collection>` namespace longer than 235 bytes.

`workloads/workloada` fixes equal reads and updates, uniform key selection and
ten 100-byte fields. Its intended image path is
`/etc/go-ycsb/workloads/workloada`; reporting is fixed to ten seconds. Dataset
size and worker count are supplied separately. Image packaging will install
the profile at that path.

## Pinned inputs

| Input | Value |
| --- | --- |
| Upstream tag | `v1.0.3` |
| Source commit | `f030f9942393a8febbf4365c2d582711723159f5` |
| Tested platform | Linux amd64 |
| Tested MongoDB | `8.0.34` from `mongo:8.0` |
| MongoDB Go driver in release executable | `v1.11.3` |
| Release executable's Go toolchain | `go1.18.10` |

Downloads and SHA-256:

- [Source archive](https://codeload.github.com/pingcap/go-ycsb/tar.gz/f030f9942393a8febbf4365c2d582711723159f5):
  `51703e27d7934931eeb7b79312b517f7ab697182211b549428e8e23c97a80300`
- [Official Linux amd64 archive](https://github.com/pingcap/go-ycsb/releases/download/v1.0.3/go-ycsb-linux-amd64.tar.gz):
  `453fff56c68f58dcb5d9ad8eec5124ad9b83bfcf0e51ce2bbcb854bf6425b47b`
  (matches GitHub release asset metadata).
- Extracted executable:
  `d6ed2fbdc4c93f7d766f9e9cf912ef5716e074384887347b58825b61a8969422`.

The source declares Go 1.18 and provides `make build` using
`go build -o bin/go-ycsb cmd/go-ycsb/*`, with optional native database bindings
selected by installed libraries. Dependencies must be downloaded to build from
source. Both upstream `LICENSE` and `YCSB-LICENSE` are Apache-2.0 and must be
retained when packaging. This qualification used the official release binary,
which links glibc/libpthread; do not assume it runs in an Alpine/musl image.
A source build with our selected image toolchain remains part of image work.

MongoDB image digest used:
`mongo@sha256:b9924c330a8bbc5800fe9c69f04a84e221eca7b28fd049a0354147d9908fa142`.

## Verified behavior

A disposable authenticated MongoDB container was used. The benchmark account
was created in `ycsb` with exactly this role:

```javascript
db.getSiblingDB("ycsb").createUser({
  user: "ycsb_only",
  pwd: "<test-password>",
  roles: [{ role: "readWrite", db: "ycsb" }]
})
```

The URI selected `/ycsb?authSource=ycsb`. Upstream's `ListDatabaseNames` call
succeeded with this account; an explicit list showed only `ycsb`. A read from
another populated database failed with authorization code 13. No administrator
role or cluster-wide database-listing privilege was granted to the benchmark user.

| Scenario | Observed result | Tool exit code |
| --- | --- | --- |
| Load 1,000 records into `ycsb.qualification_records` | 1,000 inserts; no operation errors | 0 |
| Run 4,000 operations, two workers | 4,000 successful operations; no operation errors | 0 |
| Repeat load into the same collection | 1,000 `INSERT_ERROR` operations | 0 |
| Run against an empty collection | 1,997 `READ_ERROR` and 2,003 `UPDATE_ERROR` operations | 0 |
| Incorrect password | Authentication failure before workload | 1 |
| URI selects `benchmark_test`, collection `database_probe` | 20 documents in `ycsb.database_probe`, zero in `benchmark_test.database_probe` | 0 |

The successful workload used `workload=core`, `readallfields=true`, equal
read/update proportions, zero insert/scan/read-modify-write proportions,
`requestdistribution=uniform`, `fieldcount=10`, `fieldlength=100`, and a
one-second reporting interval. Load used one worker. Counts were checked directly
with MongoDB, including zero documents in the default `usertable` collection.
The isolated test container and its anonymous volumes were removed afterward.

## Output fixtures and implications

`testdata/` contains captured load, run, authentication, missing-data and
database-selection output plus exit codes in `qualification-results.json`.
The duplicate-load fixture omits repetitive diagnostics, marked explicitly.
Connection URIs are replaced with `<redacted-mongodb-uri>` before saving;
the original upstream property banner exposes the complete URI.

- Do not interpret exit code zero as proof of successful operations.
- Parse final measurements after `Run finished, takes ...`; periodic rows use
  the same metric labels. Failed-only runs can have no successful `TOTAL` row.
- Successful runs write `ops_per_second` (`ops/s`) and `latency_avg` (`ms`)
  from the final `TOTAL` row to `/dev/termination-log`. Missing or invalid
  metrics produce a warning without failing an otherwise successful run.
- Filter both output streams before writing Pod logs.
- This check demonstrates a fresh uniform workload, not all dataset sizes or
  distributions. ARM64, TLS and replica-set behavior are not established here.
