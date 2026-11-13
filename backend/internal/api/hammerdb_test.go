package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	"github.com/openeverest/plugin-bench/backend/internal/driver"
	"github.com/openeverest/plugin-bench/backend/internal/driver/hammerdb"
	"github.com/openeverest/plugin-bench/backend/internal/everest"
	"github.com/openeverest/plugin-bench/backend/internal/runnerresult"
)

const hammerDBRunnerImage = "example.com/plugin-bench-hammerdb:test"

func newHammerDBAPI(t *testing.T, credentials *everest.Credentials, runner BenchmarkRunner, defaults coordinator.Resources) *API {
	t.Helper()
	registry, err := driver.NewRegistry(driver.Registration{
		Driver: hammerdb.New(),
		Runner: driver.RunnerConfig{
			Image: hammerDBRunnerImage, ImagePullPolicy: "Never", ExecutionTimeout: 30 * time.Minute,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	checkTarget := func(token, cluster, namespace, instance string) {
		if token != "test-token" || cluster != "local" || namespace != "dbs" || instance != "db-1" {
			t.Error("OpenEverest lookup did not use the caller's token and target")
		}
	}
	return NewAPI(func(_ context.Context, token, cluster, namespace, instance string) (*everest.Credentials, error) {
		checkTarget(token, cluster, namespace, instance)
		return credentials, nil
	}, func(_ context.Context, token, cluster, namespace, instance string) error {
		checkTarget(token, cluster, namespace, instance)
		return nil
	}, runner, NewRunStore(), context.Background(), defaults, registry, nil)
}

func TestHammerDBAPIExecutesPostgreSQLAndMySQLTargets(t *testing.T) {
	for _, test := range []struct {
		name, databaseType, provider, uri, wantType string
	}{
		{"PostgreSQL", "PostgreSQL", "provider-pg", "postgres://bench-user:hammer-secret@pg.example:5432/app?sslmode=require", "postgresql"},
		{"MySQL", "MySQL", "provider-mysql", "mysql://bench-user:hammer-secret@mysql.example:3306/app?tls=false", "mysql"},
	} {
		t.Run(test.name, func(t *testing.T) {
			credentials := &everest.Credentials{
				Host:     map[string]string{"PostgreSQL": "pg.example", "MySQL": "mysql.example"}[test.name],
				Port:     map[string]string{"PostgreSQL": "5432", "MySQL": "3306"}[test.name],
				Username: "bench-user", Password: "hammer-secret", Type: test.databaseType, Provider: test.provider,
				Database: "app", URI: test.uri,
			}
			calls := make(chan coordinator.RunInput, 1)
			wantMetrics := []runnerresult.Metric{
				{Name: "nopm", Unit: "orders/min", Value: 600},
				{Name: "tpm", Unit: "tx/min", Value: 1234},
				{Name: "latency_new_order_avg", Unit: "ms", Value: 2.5},
				{Name: "latency_payment_avg", Unit: "ms", Value: 1.25},
				{Name: "latency_delivery_avg", Unit: "ms", Value: 3},
				{Name: "latency_order_status_avg", Unit: "ms", Value: 0.75},
				{Name: "latency_stock_level_avg", Unit: "ms", Value: 1.5},
			}
			defaults := coordinator.Resources{CPURequest: "100m", CPULimit: "2", MemoryRequest: "128Mi", MemoryLimit: "512Mi"}
			wantResources := coordinator.Resources{CPURequest: "500m", CPULimit: "2", MemoryRequest: "128Mi", MemoryLimit: "1Gi"}
			affinity := testNodeAffinity()
			api := newHammerDBAPI(t, credentials, testRunnerFunc(func(_ context.Context, input coordinator.RunInput) (coordinator.Result, error) {
				calls <- input
				return coordinator.Result{
					Output:  "HammerDB failed to connect with hammer-secret using " + credentials.URI,
					Metrics: wantMetrics,
				}, nil
			}), defaults)
			// Leave rampupMinutes omitted to verify that the effective snapshot
			// includes the driver's default along with the supplied options.
			wantOptions := hammerdb.Options{Warehouses: 2, VirtualUsers: 2, RampupMinutes: 0, DurationMinutes: 3, Initialize: true}
			request := CreateRunRequest{
				Target: Target{K8sCluster: "local", Namespace: "dbs", Instance: "db-1"},
				Driver: "hammerdb", Database: "tpcc",
				Options:   json.RawMessage(`{"warehouses":2,"virtualUsers":2,"durationMinutes":3,"initialize":true}`),
				Resources: coordinator.Resources{CPURequest: "500m", MemoryLimit: "1Gi"}, NodeAffinity: affinity,
			}
			body, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			run := awaitAcceptedRun(t, api, submitRun(api, string(body)))

			var input coordinator.RunInput
			select {
			case input = <-calls:
			default:
				t.Fatal("accepted HammerDB run never reached the coordinator")
			}
			if input.Runner != (driver.RunnerConfig{Image: hammerDBRunnerImage, ImagePullPolicy: "Never", ExecutionTimeout: 30 * time.Minute}) {
				t.Fatalf("runner configuration = %+v", input.Runner)
			}
			if input.Spec.Env["DB_TYPE"] != test.wantType || input.Spec.Env["DB_NAME"] != "tpcc" ||
				string(input.Spec.SecretData["DB_PASSWORD"]) != credentials.Password || input.Spec.SecretEnv["DB_PASSWORD"] != "DB_PASSWORD" {
				t.Fatal("prepared HammerDB workload lost its target or secret mapping")
			}
			if input.Spec.Env["BENCH_WAREHOUSES"] != "2" || input.Spec.Env["BENCH_VIRTUAL_USERS"] != "2" ||
				input.Spec.Env["BENCH_INITIALIZE"] != "true" {
				t.Fatal("prepared HammerDB workload lost effective benchmark options")
			}
			if input.Resources != request.Resources || !reflect.DeepEqual(input.NodeAffinity, affinity) {
				t.Fatal("execution lost resource or node affinity overrides")
			}
			if run.Resources != wantResources || !reflect.DeepEqual(&run.NodeAffinity, affinity) {
				t.Fatal("stored run lost effective resource defaults or node affinity")
			}
			if run.Status != RunStatusSucceeded || run.Snapshot.Driver != "hammerdb" || run.Snapshot.DatabaseType != test.wantType ||
				run.Snapshot.RunnerImage != hammerDBRunnerImage || strings.Contains(run.Output, credentials.Password) ||
				strings.Contains(run.Output, credentials.URI) || !strings.Contains(run.Output, "[REDACTED]") || !reflect.DeepEqual(run.Metrics, wantMetrics) {
				t.Fatalf("stored HammerDB run = %+v", run)
			}
			mux := http.NewServeMux()
			api.RegisterRoutes(mux)
			get := httptest.NewRequest(http.MethodGet, "/api/runs/"+run.ID, nil)
			get.Header.Set("Authorization", "Bearer test-token")
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, get)
			var status RunStatusResponse
			if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || status.Status != RunStatusSucceeded ||
				status.Output != run.Output || !reflect.DeepEqual(status.Metrics, wantMetrics) || strings.Contains(response.Body.String(), credentials.Password) {
				t.Fatalf("HammerDB status response = %d %s", response.Code, response.Body.String())
			}
			if status.Resources != wantResources || !reflect.DeepEqual(&status.NodeAffinity, affinity) {
				t.Fatal("status response lost effective resources or node affinity")
			}
			if input.Spec.Snapshot.Database != "tpcc" || run.Snapshot.Database != "tpcc" || status.Database != "tpcc" {
				t.Fatal("execution, stored run, or status response lost the effective database")
			}
			for source, raw := range map[string]json.RawMessage{
				"execution": input.Spec.Snapshot.Options, "stored run": run.Snapshot.Options, "status response": status.Options,
			} {
				var effective hammerdb.Options
				if err := json.Unmarshal(raw, &effective); err != nil || effective != wantOptions {
					t.Fatalf("%s options = %+v, error = %v; want %+v", source, effective, err, wantOptions)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != 5 || string(fields["rampupMinutes"]) != "0" {
					t.Fatalf("%s snapshot did not retain all effective option fields: %s", source, raw)
				}
			}
			serialized, err := json.Marshal(run)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(serialized), credentials.Password) || strings.Contains(string(serialized), credentials.URI) {
				t.Fatal("stored run exposed database credentials")
			}
		})
	}
}

func TestHammerDBAPIRejectsInvalidRequestsBeforeRunner(t *testing.T) {
	for _, test := range []struct {
		name, databaseType, options string
		wantStatus                  int
	}{
		{"invalid options", "mysql", `"options":{"virtualUsers":2}`, http.StatusBadRequest},
		{"unsupported database type", "mongodb", `"options":{}`, http.StatusUnprocessableEntity},
	} {
		t.Run(test.name, func(t *testing.T) {
			var called atomic.Bool
			api := newHammerDBAPI(t, &everest.Credentials{
				Host: "db.example", Port: "3306", Username: "bench-user", Password: "hammer-secret",
				Type: test.databaseType, Database: "app",
			}, testRunnerFunc(func(context.Context, coordinator.RunInput) (coordinator.Result, error) {
				called.Store(true)
				return coordinator.Result{}, nil
			}), coordinator.Resources{})
			body := `{"target":{"k8sCluster":"local","namespace":"dbs","instance":"db-1"},"driver":"hammerdb",` + test.options + `}`
			r := httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer test-token")
			w := httptest.NewRecorder()
			api.createRun(w, r)
			if w.Code != test.wantStatus || called.Load() || storedRunCount(api) != 0 {
				t.Fatalf("invalid HammerDB request response = %d %s; runner called=%t", w.Code, w.Body.String(), called.Load())
			}
		})
	}
}
