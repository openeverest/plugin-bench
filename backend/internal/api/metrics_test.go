package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	"github.com/openeverest/plugin-bench/backend/internal/everest"
	"github.com/openeverest/plugin-bench/backend/internal/runnerresult"
)

func TestSanitizeRunnerMetricsPreservesValuesAndOwnership(t *testing.T) {
	const uri = "postgres://user:encoded%2Dsecret@host/app"
	metrics := []runnerresult.Metric{
		{Name: "tps", Unit: "tx/s", Value: 5432.10},
		{Name: "password=secret-value", Unit: "ms", Value: 1},
		{Name: "latency_avg", Unit: "secret-value", Value: 2},
		{Name: uri, Unit: "count", Value: 3},
		{Name: "delta", Unit: "count", Value: -2},
		{Name: "failures", Unit: "count", Value: 0},
		{Name: "[REDACTED]", Unit: "count", Value: 4},
	}
	before := slices.Clone(metrics)
	got := sanitizeRunnerMetrics(metrics, "secret-value", uri, "[REDACTED]", "")
	want := []runnerresult.Metric{metrics[0], metrics[4], metrics[5]}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(metrics, before) {
		t.Fatalf("metric filtering changed values, order, or the original slice: %+v", got)
	}
	got[0].Name = "changed"
	if !reflect.DeepEqual(metrics, before) {
		t.Fatal("sanitized metrics share mutable storage with the original")
	}
	for _, input := range [][]runnerresult.Metric{nil, {}, {metrics[1]}} {
		if got := sanitizeRunnerMetrics(input, "secret-value"); got != nil {
			t.Fatal("unavailable metrics must return nil")
		}
	}
}

func TestRunMetricsAreSanitizedStoredAndReturned(t *testing.T) {
	const uri = "postgres://bench-user:bench%2Dpassword@postgres.example.com:5432/bench"
	safe := runnerresult.Metric{Name: "tps", Unit: "tx/s", Value: 5432.10}
	unsafe := runnerresult.Metric{Name: "bench-password", Unit: "ms", Value: 1}
	for _, test := range []struct {
		name    string
		metrics []runnerresult.Metric
		want    []runnerresult.Metric
		err     error
	}{
		{"successful metrics", []runnerresult.Metric{safe, unsafe, {Name: "latency_avg", Unit: uri, Value: 2}}, []runnerresult.Metric{safe}, nil},
		{"failed metrics", []runnerresult.Metric{safe, unsafe}, []runnerresult.Metric{safe}, errors.New("runner failed")},
		{"unavailable metrics", nil, nil, nil},
		{"all metrics filtered", []runnerresult.Metric{unsafe}, nil, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			var diagnostics bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&diagnostics)
			defer log.SetOutput(previous)
			a := newTestAPI(func(context.Context, string, string, string, string) (*everest.Credentials, error) {
				creds := testCredentials()
				creds.URI = uri
				return creds, nil
			}, func(context.Context, string, string, string, string) error { return nil },
				testRunnerFunc(func(context.Context, coordinator.RunInput) (coordinator.Result, error) {
					return coordinator.Result{Output: "pgbench tps = 5432.10", Metrics: test.metrics}, test.err
				}), NewRunStore(), context.Background(), coordinator.Resources{})
			run := awaitAcceptedRun(t, a, submitRun(a, modernRunBody))
			wantStatus := RunStatusSucceeded
			if test.err != nil {
				wantStatus = RunStatusFailed
			}
			if run.Status != wantStatus || !reflect.DeepEqual(run.Metrics, test.want) || run.Output != "pgbench tps = 5432.10" {
				t.Fatal("metric sanitization changed status, numeric results, or log output")
			}
			r := httptest.NewRequest(http.MethodGet, "/api/runs/"+run.ID, nil)
			r.SetPathValue("id", run.ID)
			r.Header.Set("Authorization", "Bearer test-token")
			w := httptest.NewRecorder()
			a.getRun(w, r)
			var response RunStatusResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusOK || response.Status != wantStatus || !reflect.DeepEqual(response.Metrics, test.want) {
				t.Fatalf("GET returned incorrect metrics or status: %s", w.Body.String())
			}
			if strings.Contains(w.Body.String(), `"metrics":`) != (len(test.want) > 0) {
				t.Fatal("metrics must be omitted when unavailable")
			}
			for _, secret := range []string{"bench-password", uri} {
				if strings.Contains(w.Body.String(), secret) || strings.Contains(diagnostics.String(), secret) {
					t.Fatal("metric metadata exposed credentials in the API or logs")
				}
			}
		})
	}
}

func TestRunMetricsRequireTargetAccessAndAreOmittedWhileRunning(t *testing.T) {
	for _, test := range []struct {
		name      string
		completed bool
		token     string
		accessErr error
		status    int
	}{
		{"running", false, "Bearer test-token", nil, http.StatusOK},
		{"unauthenticated", true, "", nil, http.StatusUnauthorized},
		{"unauthorized", true, "Bearer test-token", everest.ErrUnauthorized, http.StatusUnauthorized},
		{"forbidden", true, "Bearer test-token", everest.ErrForbidden, http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewRunStore()
			if _, err := store.Create("run", Target{K8sCluster: "main", Namespace: "db", Instance: "instance"}, RunSnapshot{}, coordinator.Resources{}, nil); err != nil {
				t.Fatal(err)
			}
			if test.completed {
				result := coordinator.Result{Metrics: []runnerresult.Metric{{Name: "private_metric", Unit: "count", Value: 9}}}
				if err := store.Complete("run", RunStatusSucceeded, result, ""); err != nil {
					t.Fatal(err)
				}
			}
			a := newTestAPI(nil, func(context.Context, string, string, string, string) error { return test.accessErr }, nil, store, context.Background(), coordinator.Resources{})
			r := httptest.NewRequest(http.MethodGet, "/api/runs/run", nil)
			r.SetPathValue("id", "run")
			r.Header.Set("Authorization", test.token)
			w := httptest.NewRecorder()
			a.getRun(w, r)
			if w.Code != test.status || strings.Contains(w.Body.String(), `"metrics":`) || strings.Contains(w.Body.String(), "private_metric") {
				t.Fatalf("running or unauthorized response exposed metrics: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
