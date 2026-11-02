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
// and their leftover Jobs and credential Secrets are deleted.
func recoverInterruptedRuns(ctx context.Context, store api.RunStore, cleaner orphanCleaner) error {
	ctx, cancel := context.WithTimeout(ctx, recoveryTimeout)
	defer cancel()
	ids, err := store.FailRunning(ctx, interruptedRunMessage)
	if err != nil {
		return fmt.Errorf("mark interrupted benchmark runs failed: %w", err)
	}
	if len(ids) > 0 {
		log.Printf("marked %d interrupted benchmark run(s) failed: %s", len(ids), strings.Join(ids, ", "))
	}
	// Cleanup failure must not prevent startup; a Role missing the list verb
	// on Secrets, for example, should not take the benchmark service down.
	cleanup, err := cleaner.CleanupOrphanedResources(ctx)
	if cleanup.Jobs > 0 || cleanup.Secrets > 0 {
		log.Printf("removed %d orphaned runner Job(s) and %d credential Secret(s)", cleanup.Jobs, cleanup.Secrets)
	}
	if err != nil {
		log.Printf("warning: orphaned runner resources could not be fully removed: %v", err)
	}
	return nil
}
