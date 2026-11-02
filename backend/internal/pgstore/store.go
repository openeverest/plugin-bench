// Package pgstore persists benchmark run history in PostgreSQL so records
// survive backend restarts.
package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openeverest/plugin-bench/backend/internal/api"
	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	"github.com/openeverest/plugin-bench/backend/internal/runnerresult"
	corev1 "k8s.io/api/core/v1"
)

const (
	maxConnections = 4
	startupTimeout = 30 * time.Second
)

// Store implements api.RunStore. It never stores database credentials: run
// records contain only the effective, already redacted settings and results.
type Store struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

var _ api.RunStore = (*Store)(nil)

// Open connects, applies schema migrations, and claims the database for one
// installation, refusing a database that another installation already owns.
func Open(ctx context.Context, connectionString, installationID string) (*Store, error) {
	config, err := pgxpool.ParseConfig(connectionString)
	if err != nil {
		// pgx redacts the password in its parse errors.
		return nil, fmt.Errorf("parse PostgreSQL connection string: %w", err)
	}
	return open(ctx, config, installationID)
}

func open(ctx context.Context, config *pgxpool.Config, installationID string) (*Store, error) {
	if strings.TrimSpace(installationID) == "" {
		return nil, errors.New("installation ID is required")
	}
	config.MaxConns = maxConnections
	ctx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate PostgreSQL schema: %w", err)
	}
	if err := claimInstallation(ctx, pool, installationID); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool, now: time.Now}, nil
}

// claimInstallation runs under the migration lock so concurrent first starts
// agree on a single owner.
func claimInstallation(ctx context.Context, pool *pgxpool.Pool, installationID string) error {
	var owner string
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockKey); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO plugin_bench_installation (installation_id) VALUES ($1)
			ON CONFLICT (singleton) DO NOTHING`, installationID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, "SELECT installation_id FROM plugin_bench_installation").Scan(&owner)
	})
	if err != nil {
		return fmt.Errorf("claim PostgreSQL database for this installation: %w", err)
	}
	if owner != installationID {
		return fmt.Errorf("PostgreSQL database already stores history for plugin-bench installation %q; "+
			"use a separate database or schema for installation %q", owner, installationID)
	}
	return nil
}

func (s *Store) Close() {
	s.pool.Close()
}

// timestamp matches PostgreSQL's microsecond precision so a created record
// compares equal to the same record read back later.
func (s *Store) timestamp() time.Time {
	return s.now().UTC().Truncate(time.Microsecond)
}

func (s *Store) Create(ctx context.Context, id string, target api.Target, snapshot api.RunSnapshot, resources coordinator.Resources, nodeAffinity *corev1.NodeAffinity) (api.Run, error) {
	if err := api.ValidateRunID(id); err != nil {
		return api.Run{}, err
	}
	run := api.Run{
		ID: id, Target: target, Snapshot: snapshot, Resources: resources,
		Status: api.RunStatusRunning, CreatedAt: s.timestamp(),
	}
	run.Snapshot.Options = slices.Clone(snapshot.Options)
	if nodeAffinity != nil {
		run.NodeAffinity = *nodeAffinity.DeepCopy()
	}
	resourcesJSON, err := json.Marshal(run.Resources)
	if err != nil {
		return api.Run{}, fmt.Errorf("encode resources: %w", err)
	}
	affinityJSON, err := json.Marshal(run.NodeAffinity)
	if err != nil {
		return api.Run{}, fmt.Errorf("encode node affinity: %w", err)
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO benchmark_runs (
			id, k8s_cluster, namespace, instance, driver, runner_image, database_name,
			options, resources, node_affinity, status, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (id) DO NOTHING`,
		run.ID, target.K8sCluster, target.Namespace, target.Instance,
		snapshot.Driver, snapshot.RunnerImage, snapshot.Database,
		nullableJSON(run.Snapshot.Options), resourcesJSON, affinityJSON,
		string(run.Status), run.CreatedAt,
	)
	if err != nil {
		return api.Run{}, fmt.Errorf("insert run: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return api.Run{}, api.ErrRunExists
	}
	return run, nil
}

const runColumns = `id, k8s_cluster, namespace, instance, driver, runner_image, database_name,
	options, resources, node_affinity, status, created_at, completed_at,
	job_name, output_truncated, metrics, error`

func (s *Store) Get(ctx context.Context, id string) (api.Run, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+runColumns+`, output FROM benchmark_runs WHERE id = $1`, id)
	if err != nil {
		return api.Run{}, fmt.Errorf("query run: %w", err)
	}
	run, err := pgx.CollectExactlyOneRow(rows, func(row pgx.CollectableRow) (api.Run, error) {
		return scanRun(row, true)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return api.Run{}, api.ErrRunNotFound
	}
	if err != nil {
		return api.Run{}, fmt.Errorf("read run: %w", err)
	}
	return run, nil
}

func (s *Store) Complete(ctx context.Context, id string, status api.RunStatus, result coordinator.Result, message string) error {
	if err := api.ValidateCompletion(status, message); err != nil {
		return err
	}
	var metricsJSON []byte
	if result.Metrics != nil {
		var err error
		if metricsJSON, err = json.Marshal(result.Metrics); err != nil {
			return fmt.Errorf("encode metrics: %w", err)
		}
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE benchmark_runs
		SET status = $2, completed_at = $3, job_name = $4, output = $5,
			output_truncated = $6, metrics = $7, error = $8
		WHERE id = $1 AND status = 'running'`,
		id, string(status), s.timestamp(), result.JobName, storableText(result.Output),
		result.OutputTruncated, nullableJSON(metricsJSON), storableText(message),
	)
	if err != nil {
		return fmt.Errorf("complete run: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM benchmark_runs WHERE id = $1)`, id).Scan(&exists); err != nil {
		return fmt.Errorf("check run: %w", err)
	}
	if !exists {
		return api.ErrRunNotFound
	}
	return api.ErrRunAlreadyCompleted
}

func (s *Store) ListByTarget(ctx context.Context, target api.Target, limit int) ([]api.Run, error) {
	if err := api.ValidateListLimit(limit); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+runColumns+` FROM benchmark_runs
		WHERE k8s_cluster = $1 AND namespace = $2 AND instance = $3
		ORDER BY created_at DESC, id
		LIMIT $4`,
		target.K8sCluster, target.Namespace, target.Instance, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query runs: %w", err)
	}
	runs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (api.Run, error) {
		return scanRun(row, false)
	})
	if err != nil {
		return nil, fmt.Errorf("read runs: %w", err)
	}
	if runs == nil {
		runs = make([]api.Run, 0)
	}
	return runs, nil
}

func (s *Store) FailRunning(ctx context.Context, message string) ([]string, error) {
	if err := api.ValidateCompletion(api.RunStatusFailed, message); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		UPDATE benchmark_runs SET status = 'failed', completed_at = $1, error = $2
		WHERE status = 'running'
		RETURNING id`,
		s.timestamp(), storableText(message),
	)
	if err != nil {
		return nil, fmt.Errorf("fail running runs: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read failed run IDs: %w", err)
	}
	if ids == nil {
		ids = make([]string, 0)
	}
	slices.Sort(ids)
	return ids, nil
}

func scanRun(row pgx.CollectableRow, withOutput bool) (api.Run, error) {
	var (
		run                                       api.Run
		status                                    string
		options, resources, affinity, metricsJSON []byte
	)
	destinations := []any{
		&run.ID, &run.Target.K8sCluster, &run.Target.Namespace, &run.Target.Instance,
		&run.Snapshot.Driver, &run.Snapshot.RunnerImage, &run.Snapshot.Database,
		&options, &resources, &affinity, &status, &run.CreatedAt, &run.CompletedAt,
		&run.JobName, &run.OutputTruncated, &metricsJSON, &run.Error,
	}
	if withOutput {
		destinations = append(destinations, &run.Output)
	}
	if err := row.Scan(destinations...); err != nil {
		return api.Run{}, err
	}
	run.Status = api.RunStatus(status)
	run.Snapshot.Options = options
	run.CreatedAt = run.CreatedAt.UTC()
	if run.CompletedAt != nil {
		completedAt := run.CompletedAt.UTC()
		run.CompletedAt = &completedAt
	}
	if err := json.Unmarshal(resources, &run.Resources); err != nil {
		return api.Run{}, fmt.Errorf("decode resources for run %q: %w", run.ID, err)
	}
	if err := json.Unmarshal(affinity, &run.NodeAffinity); err != nil {
		return api.Run{}, fmt.Errorf("decode node affinity for run %q: %w", run.ID, err)
	}
	if metricsJSON != nil {
		var metrics []runnerresult.Metric
		if err := json.Unmarshal(metricsJSON, &metrics); err != nil {
			return api.Run{}, fmt.Errorf("decode metrics for run %q: %w", run.ID, err)
		}
		run.Metrics = metrics
	}
	return run, nil
}

// nullableJSON stores absent JSON as SQL NULL rather than an invalid value.
func nullableJSON(value []byte) any {
	if value == nil {
		return nil
	}
	return value
}

// storableText makes runner output acceptable to PostgreSQL text columns,
// which reject NUL bytes and invalid UTF-8. Byte-limited log truncation can
// split a multibyte character, so this is expected rather than exceptional.
func storableText(value string) string {
	return strings.ToValidUTF8(strings.ReplaceAll(value, "\x00", "\uFFFD"), "\uFFFD")
}
