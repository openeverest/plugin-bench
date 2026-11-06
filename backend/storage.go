package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/api"
	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	"github.com/openeverest/plugin-bench/backend/internal/pgstore"
)

const (
	storageMemory     = "memory"
	storagePostgreSQL = "postgresql"

	// statusStorageEphemeral is the /api/status value for in-memory storage,
	// kept from before persistent storage existed.
	statusStorageEphemeral = "ephemeral"

	recoveryTimeout       = 30 * time.Second
	orphanCleanupRetry    = 5 * time.Second
	orphanCleanupMaxRetry = 5 * time.Minute
	interruptedRunMessage = "benchmark run was interrupted because the benchmark service restarted"
)

// runStoreFromEnv selects storage explicitly, so a missing connection string
// fails startup instead of silently falling back to memory.
func runStoreFromEnv(ctx context.Context, installationID string) (store api.RunStore, statusStorage string, closeStore func(), err error) {
	switch kind := envOrDefault("BENCHMARK_STORAGE", storageMemory); kind {
	case storageMemory:
		return api.NewRunStore(), statusStorageEphemeral, func() {}, nil
	case storagePostgreSQL:
		uri := strings.TrimSpace(os.Getenv("BENCHMARK_STORAGE_POSTGRES_URI"))
		if uri == "" {
			return nil, "", nil, fmt.Errorf("BENCHMARK_STORAGE_POSTGRES_URI is required when BENCHMARK_STORAGE is %s", storagePostgreSQL)
		}
		pg, err := pgstore.Open(ctx, uri, installationID)
		if err != nil {
			return nil, "", nil, err
		}
		return pg, storagePostgreSQL, pg.Close, nil
	default:
		return nil, "", nil, fmt.Errorf("BENCHMARK_STORAGE must be %s or %s", storageMemory, storagePostgreSQL)
	}
}

type orphanCleaner interface {
	CleanupOrphanedResources(ctx context.Context) (coordinator.OrphanCleanup, error)
}

// recoverInterruptedRuns runs before the API accepts requests. Interrupted
// runs cannot be resumed: redacting their logs requires target credentials
// that only existed in the previous process. They are marked failed instead,
// and their leftover Jobs and credential Secrets are deleted. It reports
// whether leftovers remain so the caller can keep retrying in the background.
func recoverInterruptedRuns(ctx context.Context, store api.RunStore, cleaner orphanCleaner) (cleanupPending bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, recoveryTimeout)
	defer cancel()
	ids, err := store.FailRunning(ctx, interruptedRunMessage)
	if err != nil {
		return false, fmt.Errorf("mark interrupted benchmark runs failed: %w", err)
	}
	if len(ids) > 0 {
		log.Printf("marked %d interrupted benchmark run(s) failed: %s", len(ids), strings.Join(ids, ", "))
	}
	// Cleanup failure must not prevent startup; a Role missing the list verb
	// on Secrets, for example, should not take the benchmark service down.
	return !cleanupOrphanedResources(ctx, cleaner), nil
}

// retryOrphanCleanup retries with capped exponential backoff until cleanup
// succeeds or ctx ends. Leftover credential Secrets hold target database
// passwords, so a transient Kubernetes error must not strand them until the
// next restart. Retrying while runs are active is safe because cleanup skips
// resources created by the current process.
func retryOrphanCleanup(ctx context.Context, cleaner orphanCleaner, delay, maxDelay time.Duration) {
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		attemptCtx, cancel := context.WithTimeout(ctx, recoveryTimeout)
		cleaned := cleanupOrphanedResources(attemptCtx, cleaner)
		cancel()
		if cleaned {
			return
		}
		delay = min(2*delay, maxDelay)
	}
}

func cleanupOrphanedResources(ctx context.Context, cleaner orphanCleaner) bool {
	cleanup, err := cleaner.CleanupOrphanedResources(ctx)
	if cleanup.Jobs > 0 || cleanup.Secrets > 0 {
		log.Printf("removed %d orphaned runner Job(s) and %d credential Secret(s)", cleanup.Jobs, cleanup.Secrets)
	}
	if err != nil {
		log.Printf("warning: orphaned runner resources could not be fully removed; will retry: %v", err)
		return false
	}
	return true
}
