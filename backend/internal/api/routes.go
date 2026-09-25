package api

import (
	"context"
	"net/http"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	"github.com/openeverest/plugin-bench/backend/internal/everest"
)

// CredentialLookup resolves connection details for an OpenEverest target.
// Keeping it injectable lets API handlers be tested without an OpenEverest cluster.
type CredentialLookup func(ctx context.Context, token, k8sCluster, namespace, instance string) (*everest.Credentials, error)

type BenchmarkRunner interface {
	Run(ctx context.Context, connection coordinator.Connection, options coordinator.Options) (coordinator.Result, error)
}

type API struct {
	getCredentials CredentialLookup
	runner         BenchmarkRunner
	store          *RunStore
	lifecycleCtx   context.Context
	runSlots       chan struct{}
}

func NewAPI(getCredentials CredentialLookup, runner BenchmarkRunner, store *RunStore, lifecycleCtx context.Context) *API {
	if store == nil {
		store = NewRunStore()
	}
	if lifecycleCtx == nil {
		lifecycleCtx = context.Background()
	}
	return &API{
		getCredentials: getCredentials,
		runner:         runner,
		store:          store,
		lifecycleCtx:   lifecycleCtx,
		runSlots:       make(chan struct{}, maxConcurrentRuns),
	}
}

func (a *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/runs", a.createRun)
	mux.HandleFunc("GET /api/runs/{id}", a.getRun)
}
