package api

import (
	"context"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	"github.com/openeverest/plugin-bench/backend/internal/driver"
	"github.com/openeverest/plugin-bench/backend/internal/driver/pgbench"
)

const testRunnerImage = "example.com/plugin-bench-pgbench:test"

func testRegistry() *driver.Registry {
	registry, err := driver.NewRegistry(driver.Registration{Driver: pgbench.New(), Runner: driver.RunnerConfig{Image: testRunnerImage, ImagePullPolicy: "IfNotPresent"}})
	if err != nil {
		panic(err)
	}
	return registry
}

func newTestAPI(get CredentialLookup, access InstanceAccessCheck, runner BenchmarkRunner, store *RunStore, ctx context.Context, defaults coordinator.Resources) *API {
	return NewAPI(get, access, runner, store, ctx, defaults, testRegistry())
}
