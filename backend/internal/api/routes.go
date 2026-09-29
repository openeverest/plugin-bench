package api

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	"github.com/openeverest/plugin-bench/backend/internal/everest"
)

// CredentialLookup resolves connection details for an OpenEverest target.
// Keeping it injectable lets API handlers be tested without an OpenEverest cluster.
type CredentialLookup func(ctx context.Context, token, k8sCluster, namespace, instance string) (*everest.Credentials, error)

// InstanceAccessCheck verifies that the caller can read a target instance.
type InstanceAccessCheck func(ctx context.Context, token, k8sCluster, namespace, instance string) error

type BenchmarkRunner interface {
	Run(ctx context.Context, connection coordinator.Connection, options coordinator.Options) (coordinator.Result, error)
}

type API struct {
	getCredentials      CredentialLookup
	checkInstanceAccess InstanceAccessCheck
	runner              BenchmarkRunner
	store               *RunStore
	lifecycleCtx        context.Context
	runSlots            chan struct{}
	runsMu              sync.Mutex
	runs                sync.WaitGroup
	closing             bool
}

func NewAPI(getCredentials CredentialLookup, checkInstanceAccess InstanceAccessCheck, runner BenchmarkRunner, store *RunStore, lifecycleCtx context.Context) *API {
	if store == nil {
		store = NewRunStore()
	}
	if lifecycleCtx == nil {
		lifecycleCtx = context.Background()
	}
	return &API{
		getCredentials:      getCredentials,
		checkInstanceAccess: checkInstanceAccess,
		runner:              runner,
		store:               store,
		lifecycleCtx:        lifecycleCtx,
		runSlots:            make(chan struct{}, maxConcurrentRuns),
	}
}

func (a *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/runs", a.createRun)
	mux.HandleFunc("GET /api/runs/{id}", a.getRun)
}

func (a *API) registerRun() bool {
	a.runsMu.Lock()
	defer a.runsMu.Unlock()
	if a.closing {
		return false
	}
	a.runs.Add(1)
	return true
}

func (a *API) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("shutdown context is nil")
	}
	a.runsMu.Lock()
	a.closing = true
	a.runsMu.Unlock()

	done := make(chan struct{})
	go func() {
		a.runs.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
