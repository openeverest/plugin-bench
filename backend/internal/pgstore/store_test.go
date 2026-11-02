package pgstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openeverest/plugin-bench/backend/internal/api"
	"github.com/openeverest/plugin-bench/backend/internal/api/storetest"
	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
)

// Integration tests need a disposable PostgreSQL server; each test uses its
// own schema so tests stay isolated and leave nothing behind.
const testURIVariable = "PLUGIN_BENCH_TEST_POSTGRES_URI"

func isolatedConfig(t *testing.T) *pgxpool.Config {
	t.Helper()
	uri := os.Getenv(testURIVariable)
	if uri == "" {
		t.Skipf("set %s to run PostgreSQL integration tests", testURIVariable)
	}
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	schema := "plugin_bench_test_" + hex.EncodeToString(suffix[:])
	admin, err := pgx.Connect(context.Background(), uri)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	if _, err := admin.Exec(context.Background(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		conn, err := pgx.Connect(context.Background(), uri)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close(context.Background())
		if _, err := conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	config, err := pgxpool.ParseConfig(uri)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	return config
}

func openStore(t *testing.T, config *pgxpool.Config) *Store {
	t.Helper()
	store, err := open(context.Background(), config.Copy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store
}

func TestStoreConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) api.RunStore { return openStore(t, isolatedConfig(t)) })
}

func TestRecordsSurviveReconnect(t *testing.T) {
	config := isolatedConfig(t)
	first := openStore(t, config)
	target := api.Target{K8sCluster: "main", Namespace: "db", Instance: "pg"}
	if _, err := first.Create(context.Background(), "run", target, api.RunSnapshot{Driver: "pgbench"}, coordinator.Resources{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := first.Complete(context.Background(), "run", api.RunStatusSucceeded, coordinator.Result{Output: "done"}, ""); err != nil {
		t.Fatal(err)
	}
	first.Close()

	second := openStore(t, config)
	run, err := second.Get(context.Background(), "run")
	if err != nil || run.Status != api.RunStatusSucceeded || run.Output != "done" || run.Target != target {
		t.Fatalf("record after reconnect = %+v, %v", run, err)
	}
}

func TestMigrationsAreSerializedAndIdempotent(t *testing.T) {
	config := isolatedConfig(t)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range cap(errs) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store, err := open(context.Background(), config.Copy())
			if err == nil {
				store.Close()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent startup failed: %v", err)
		}
	}
	store := openStore(t, config)
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var applied, latest int
	if err := store.pool.QueryRow(context.Background(),
		"SELECT count(*), max(version) FROM plugin_bench_schema_migrations").Scan(&applied, &latest); err != nil {
		t.Fatal(err)
	}
	if applied != len(migrations) || latest != len(migrations) {
		t.Fatalf("applied %d migrations up to %d, want each of %d once", applied, latest, len(migrations))
	}
}

func TestRefusesNewerSchema(t *testing.T) {
	config := isolatedConfig(t)
	store := openStore(t, config)
	if _, err := store.pool.Exec(context.Background(), "INSERT INTO plugin_bench_schema_migrations (version) VALUES (9999)"); err != nil {
		t.Fatal(err)
	}
	if _, err := open(context.Background(), config.Copy()); err == nil || !strings.Contains(err.Error(), "newer than this release") {
		t.Fatalf("open error = %v; want a newer-schema refusal", err)
	}
}

func TestOpenErrorsDoNotExposePassword(t *testing.T) {
	for _, uri := range []string{
		"postgres://bench:password-sentinel@127.0.0.1:1/bench?connect_timeout=1",
		"postgres://bench:password-sentinel@127.0.0.1:1/bench?sslmode=not-a-mode",
	} {
		_, err := Open(context.Background(), uri)
		if err == nil || strings.Contains(err.Error(), "password-sentinel") {
			t.Fatalf("Open error = %v; want a failure without the password", err)
		}
	}
}
