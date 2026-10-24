package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	"github.com/openeverest/plugin-bench/backend/internal/driver"
	"github.com/openeverest/plugin-bench/backend/internal/driver/pgbench"
	"github.com/openeverest/plugin-bench/backend/internal/everest"
	corev1 "k8s.io/api/core/v1"
)

const testRunnerImage = "example.com/plugin-bench-pgbench:test"
const validCreateRunBody = `{"target":{"k8sCluster":"local","namespace":"dbs","instance":"postgres-1"},"database":"bench","durationSeconds":30,"clients":1,"threads":1,"scale":1}`
const modernRunBody = `{"target":{"k8sCluster":"local","namespace":"dbs","instance":"postgres-1"},"database":"bench","driver":"pgbench","options":{}}`

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

func testCredentials() *everest.Credentials {
	return &everest.Credentials{
		Host: "postgres.example.com", Port: "5432", Username: "bench-user",
		Password: "bench-password", Provider: "provider-cloudnative-pg", Type: "postgresql",
	}
}

type testRunner struct{}

func (testRunner) Run(context.Context, coordinator.RunInput) (coordinator.Result, error) {
	return coordinator.Result{Output: "benchmark complete"}, nil
}

type testRunnerFunc func(context.Context, coordinator.RunInput) (coordinator.Result, error)

func (f testRunnerFunc) Run(ctx context.Context, input coordinator.RunInput) (coordinator.Result, error) {
	return f(ctx, input)
}

func testAPI() *API {
	return newTestAPI(func(context.Context, string, string, string, string) (*everest.Credentials, error) {
		return testCredentials(), nil
	}, nil, testRunner{}, NewRunStore(), context.Background(), coordinator.Resources{})
}

func submitRun(a *API, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	a.createRun(w, r)
	return w
}

func awaitAcceptedRun(t *testing.T, a *API, w *httptest.ResponseRecorder) Run {
	t.Helper()
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST = %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := a.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := a.store.Get(response.ID)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func testNodeAffinity() *corev1.NodeAffinity {
	return &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
			{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "workload", Operator: corev1.NodeSelectorOpIn, Values: []string{"benchmark"}}}},
			{MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{"node-1"}}}},
		}},
		PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{{Weight: 75, Preference: corev1.NodeSelectorTerm{
			MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "disk-type", Operator: corev1.NodeSelectorOpIn, Values: []string{"ssd"}}},
			MatchFields:      []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpNotIn, Values: []string{"node-2"}}},
		}}},
	}
}

func mutateTestNodeAffinity(affinity *corev1.NodeAffinity) {
	affinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions[0].Values[0] = "changed"
	affinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[1].MatchFields[0].Values[0] = "changed"
	affinity.PreferredDuringSchedulingIgnoredDuringExecution[0].Weight = 1
	affinity.PreferredDuringSchedulingIgnoredDuringExecution[0].Preference.MatchExpressions[0].Values[0] = "changed"
	affinity.PreferredDuringSchedulingIgnoredDuringExecution[0].Preference.MatchFields[0].Values[0] = "changed"
}
