package pgstore

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationLockKey serializes schema changes between backend replicas that
// share one database. The value is arbitrary but must never change.
const migrationLockKey int64 = 0x706c7567696e6265 // "pluginbe"

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations reads NNNN_description.sql files in version order.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	migrations := make([]migration, 0, len(entries))
	for _, entry := range entries {
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		version, err := strconv.Atoi(prefix)
		if !ok || err != nil || version < 1 || !strings.HasSuffix(entry.Name(), ".sql") {
			return nil, fmt.Errorf("invalid migration file name %q", entry.Name())
		}
		content, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, err
		}
		migrations = append(migrations, migration{version: version, name: entry.Name(), sql: string(content)})
	}
	slices.SortFunc(migrations, func(a, b migration) int { return a.version - b.version })
	for i, m := range migrations {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration versions must be contiguous from 1; found %q", m.name)
		}
	}
	return migrations, nil
}

// migrate applies pending migrations in one transaction under an advisory
// lock. It refuses to run against a schema written by a newer release, since
// this binary cannot know whether that schema is still compatible.
func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	migrations, err := loadMigrations()
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockKey); err != nil {
			return fmt.Errorf("acquire migration lock: %w", err)
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS plugin_bench_schema_migrations (
			version    integer     PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
			return fmt.Errorf("create migration table: %w", err)
		}
		var current int
		if err := tx.QueryRow(ctx, "SELECT COALESCE(max(version), 0) FROM plugin_bench_schema_migrations").Scan(&current); err != nil {
			return fmt.Errorf("read schema version: %w", err)
		}
		if current > len(migrations) {
			return fmt.Errorf("database schema version %d is newer than this release supports (%d)", current, len(migrations))
		}
		for _, m := range migrations[current:] {
			if _, err := tx.Exec(ctx, m.sql); err != nil {
				return fmt.Errorf("apply migration %s: %w", m.name, err)
			}
			if _, err := tx.Exec(ctx, "INSERT INTO plugin_bench_schema_migrations (version) VALUES ($1)", m.version); err != nil {
				return fmt.Errorf("record migration %s: %w", m.name, err)
			}
		}
		return nil
	})
}
