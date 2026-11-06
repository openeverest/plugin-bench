-- JSON (not JSONB) columns keep effective settings byte-for-byte, so the UI
-- shows options in the order the driver recorded them.
CREATE TABLE benchmark_runs (
    id               text        PRIMARY KEY,
    k8s_cluster      text        NOT NULL,
    namespace        text        NOT NULL,
    instance         text        NOT NULL,
    database_type    text        NOT NULL,
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
    error            text        NOT NULL DEFAULT '',
    CHECK ((status = 'running') = (completed_at IS NULL))
);

CREATE INDEX benchmark_runs_target_created_idx
    ON benchmark_runs (k8s_cluster, namespace, instance, created_at DESC, id);

CREATE INDEX benchmark_runs_running_idx
    ON benchmark_runs (id) WHERE status = 'running';

-- One row per metric so history can be compared and charted in SQL, e.g. tps
-- across runs. Names and units come from each runner, so engines can report
-- any metric without schema changes. position preserves the runner's order.
CREATE TABLE benchmark_run_metrics (
    run_id   text             NOT NULL REFERENCES benchmark_runs (id) ON DELETE CASCADE,
    position integer          NOT NULL,
    name     text             NOT NULL,
    unit     text             NOT NULL,
    value    double precision NOT NULL,
    PRIMARY KEY (run_id, position)
);

CREATE INDEX benchmark_run_metrics_name_idx
    ON benchmark_run_metrics (name, unit);
