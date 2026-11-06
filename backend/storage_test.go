package main

import (
	"context"
	"errors"
	"math"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openeverest/plugin-bench/backend/internal/api"
	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
)

func TestRunStoreFromEnvSelectsStorageExplicitly(t *testing.T) {
	for _, test := range []struct {
		name, storage, uri, wantStatus, wantErr string
	}{
		{name: "default memory", wantStatus: statusStorageEphemeral},
		{name: "explicit memory ignores URI", storage: "memory", uri: "postgres://unused", wantStatus: statusStorageEphemeral},
		{name: "postgresql requires URI", storage: "postgresql", wantErr: "BENCHMARK_STORAGE_POSTGRES_URI is required"},
		{name: "unknown storage", storage: "sqlite", wantErr: "BENCHMARK_STORAGE must be"},
		{name: "unreachable postgresql fails startup", storage: "postgresql",
			uri: "postgres://bench:password-sentinel@127.0.0.1:1/bench?connect_timeout=1", wantErr: "connect to PostgreSQL"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("BENCHMARK_STORAGE", test.storage)
			t.Setenv("BENCHMARK_STORAGE_POSTGRES_URI", test.uri)
			store, status, closeStore, err := runStoreFromEnv(context.Background(), "plugin-bench")
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) || strings.Contains(err.Error(), "password-sentinel") {
					t.Fatalf("error = %v; want %q without credentials", err, test.wantErr)
				}
				return
			}
			if err != nil || status != test.wantStatus {
				t.Fatalf("status = %q, err = %v", status, err)
			}
			defer closeStore()
			if _, ok := store.(*api.MemoryRunStore); !ok {
				t.Fatalf("store = %T, want memory", store)
			}
		})
	}
}

func TestRunStoreFromEnvOpensPostgreSQL(t *testing.T) {
	uri := os.Getenv("PLUGIN_BENCH_TEST_POSTGRES_URI")
	if uri == "" {
		t.Skip("set PLUGIN_BENCH_TEST_POSTGRES_URI to run PostgreSQL integration tests")
	}
	// An isolated schema keeps this test independent of other runs' state.
	schema := "plugin_bench_main_test_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	admin, err := pgx.Connect(context.Background(), uri)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	if _, err := admin.Exec(context.Background(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	separator := "?"
	if strings.Contains(uri, "?") {
		separator = "&"
	}
	t.Setenv("BENCHMARK_STORAGE", "postgresql")
	t.Setenv("BENCHMARK_STORAGE_POSTGRES_URI", uri+separator+"search_path="+schema)
	_, status, closeStore, err := runStoreFromEnv(context.Background(), "plugin-bench")
	if err != nil || status != storagePostgreSQL {
		t.Fatalf("status = %q, err = %v", status, err)
	}
	closeStore()
}

type recordingCleaner struct {
	called bool
	result coordinator.OrphanCleanup
	err    error
}

func (c *recordingCleaner) CleanupOrphanedResources(context.Context) (coordinator.OrphanCleanup, error) {
	c.called = true
	return c.result, c.err
}

func TestRecoverInterruptedRunsFailsRunningRecordsAndCleansUp(t *testing.T) {
	store := api.NewRunStore()
	ctx := context.Background()
	for _, id := range []string{"running", "done"} {
		if _, err := store.Create(ctx, id, api.Target{Instance: "pg"}, api.RunSnapshot{}, coordinator.Resources{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Complete(ctx, "done", api.RunStatusSucceeded, coordinator.Result{}, ""); err != nil {
		t.Fatal(err)
	}
	cleaner := &recordingCleaner{err: errors.New("list Secrets forbidden")}
	cleanupPending, err := recoverInterruptedRuns(ctx, store, cleaner)
	if err != nil {
		t.Fatalf("cleanup failure must not stop startup: %v", err)
	}
	if !cleaner.called || !cleanupPending {
		t.Fatalf("cleanup called = %t, pending = %t; want a failed cleanup reported for retry", cleaner.called, cleanupPending)
	}
	interrupted, _ := store.Get(ctx, "running")
	done, _ := store.Get(ctx, "done")
	if interrupted.Status != api.RunStatusFailed || interrupted.Error != interruptedRunMessage || done.Status != api.RunStatusSucceeded {
		t.Fatalf("interrupted = %+v, done = %+v", interrupted, done)
	}
}

type failingRecoveryStore struct{ *api.MemoryRunStore }

func (failingRecoveryStore) FailRunning(context.Context, string) ([]string, error) {
	return nil, errors.New("storage unavailable")
}

func TestRecoverInterruptedRunsStopsStartupWhenRecordsCannotBeUpdated(t *testing.T) {
	cleaner := &recordingCleaner{}
	if _, err := recoverInterruptedRuns(context.Background(), failingRecoveryStore{api.NewRunStore()}, cleaner); err == nil {
		t.Fatal("startup continued with running records that can never complete")
	}
}

func TestRecoverInterruptedRunsNeedsNoRetryAfterSuccessfulCleanup(t *testing.T) {
	cleanupPending, err := recoverInterruptedRuns(context.Background(), api.NewRunStore(), &recordingCleaner{})
	if err != nil || cleanupPending {
		t.Fatalf("pending = %t, err = %v; want nothing left to retry", cleanupPending, err)
	}
}

type flakyCleaner struct {
	failures int
	calls    atomic.Int32
}

func (c *flakyCleaner) CleanupOrphanedResources(context.Context) (coordinator.OrphanCleanup, error) {
	if int(c.calls.Add(1)) <= c.failures {
		return coordinator.OrphanCleanup{}, errors.New("Kubernetes API unavailable")
	}
	return coordinator.OrphanCleanup{Secrets: 1}, nil
}

func TestRetryOrphanCleanupStopsAfterSuccess(t *testing.T) {
	cleaner := &flakyCleaner{failures: 2}
	done := make(chan struct{})
	go func() {
		retryOrphanCleanup(context.Background(), cleaner, time.Millisecond, 2*time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup retry did not finish after cleanup succeeded")
	}
	if calls := cleaner.calls.Load(); calls != 3 {
		t.Fatalf("cleanup attempts = %d, want retries until the first success", calls)
	}
}

func TestRetryOrphanCleanupStopsOnShutdown(t *testing.T) {
	cleaner := &flakyCleaner{failures: math.MaxInt}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		retryOrphanCleanup(ctx, cleaner, time.Millisecond, time.Millisecond)
		close(done)
	}()
	for cleaner.calls.Load() < 2 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup retry kept running after shutdown")
	}
}
