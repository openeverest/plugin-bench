-- JSON (not JSONB) columns keep effective settings byte-for-byte, so the UI
-- shows options in the order the driver recorded them.
CREATE TABLE benchmark_runs (
    id               text        PRIMARY KEY,
    k8s_cluster      text        NOT NULL,
    namespace        text        NOT NULL,
    instance         text        NOT NULL,
    driver           text        NOT NULL,
    runner_image     text        NOT NULL,
    database_name    text        NOT NULL,
    options          json,
    resources        json        NOT NULL,
    node_affinity    json        NOT NULL,
    status           text        NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
    created_at       timestamptz NOT NULL,
    completed_at     timestamptz,
    job_name         text        NOT NULL DEFAULT '',
    output           text        NOT NULL DEFAULT '',
    output_truncated boolean     NOT NULL DEFAULT false,
    metrics          json,
    error            text        NOT NULL DEFAULT '',
    CHECK ((status = 'running') = (completed_at IS NULL))
);

CREATE INDEX benchmark_runs_target_created_idx
    ON benchmark_runs (k8s_cluster, namespace, instance, created_at DESC, id);

CREATE INDEX benchmark_runs_running_idx
    ON benchmark_runs (id) WHERE status = 'running';
