package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	"github.com/openeverest/plugin-bench/backend/internal/driver"
	"github.com/openeverest/plugin-bench/backend/internal/driver/sysbench"
	"github.com/openeverest/plugin-bench/backend/internal/everest"
	"github.com/openeverest/plugin-bench/backend/internal/runnerresult"
)

const mariadbRunnerImage = "example.com/plugin-bench-sysbench:test"
const mariadbRunBody = `{"target":{"k8sCluster":"local","namespace":"dbs","instance":"mariadb-1"},"driver":"mariadb","options":{}}`

func mariadbCredentials() *everest.Credentials {
	return &everest.Credentials{
		Host: "mariadb.example.com", Port: "3306", Username: "bench-user", Password: "mariadb-password",
		Database: "benchdb", Type: "mysql", Provider: "mariadb",
		URI: "mysql://bench-user:mariadb-password@mariadb.example.com:3306/benchdb?tls=true", TLS: "true",
	}
}

func newMariaDBAPI(t *testing.T, credentials *everest.Credentials, runner BenchmarkRunner, databaseType string) *API {
	t.Helper()
	var implementation driver.Driver
	switch databaseType {
	case "mariadb":
		implementation = sysbench.NewMariaDB()
	case "mysql":
		implementation = sysbench.New()
	default:
		t.Fatalf("unsupported test driver type %q", databaseType)
	}
	registry, err := driver.NewRegistry(driver.Registration{
		Driver: implementation,
		Runner: driver.RunnerConfig{Image: mariadbRunnerImage, ImagePullPolicy: "Never"},
	})
	if err != nil {
		t.Fatal(err)
	}
	checkTarget := func(token, cluster, namespace, instance string) {
		if token != "test-token" || cluster != "local" || namespace != "dbs" || instance != "mariadb-1" {
			t.Error("OpenEverest lookup did not use the caller's token and target")
		}
	}
	return NewAPI(func(_ context.Context, token, cluster, namespace, instance string) (*everest.Credentials, error) {
		checkTarget(token, cluster, namespace, instance)
		return credentials, nil
	}, func(_ context.Context, token, cluster, namespace, instance string) error {
		checkTarget(token, cluster, namespace, instance)
		return nil
	}, runner, NewRunStore(), context.Background(), coordinator.Resources{}, registry, nil)
}

func TestMariaDBRunThroughAPI(t *testing.T) {
	for _, test := range []struct {
		name         string
		database     string
		connectionDB string
		options      string
		wantDatabase string
		wantOptions  sysbench.Options
	}{
		{
			name:         "provider database default",
			connectionDB: "benchdb", options: `{}`,
			wantDatabase: "benchdb",
			wantOptions:  sysbench.Options{DurationSeconds: 60, Threads: 1, Tables: 1, TableSize: 10000},
		},
		{
			name:     "explicit database and effective options",
			database: "  otherdb  ", connectionDB: "benchdb", options: `{"durationSeconds":10,"threads":3,"tables":2,"tableSize":500,"initialize":true}`,
			wantDatabase: "otherdb",
			wantOptions:  sysbench.Options{DurationSeconds: 10, Threads: 3, Tables: 2, TableSize: 500, Initialize: true},
		},
		{
			name:         "URI database fallback",
			connectionDB: "", options: `{}`,
			wantDatabase: "benchdb",
			wantOptions:  sysbench.Options{DurationSeconds: 60, Threads: 1, Tables: 1, TableSize: 10000},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			credentials := mariadbCredentials()
			credentials.Database = test.connectionDB
			calls := make(chan coordinator.RunInput, 1)
			a := newMariaDBAPI(t, credentials, testRunnerFunc(func(_ context.Context, input coordinator.RunInput) (coordinator.Result, error) {
				calls <- input
				return coordinator.Result{
					Output:  "sysbench complete password=" + credentials.Password + " uri=" + credentials.URI,
					Metrics: []runnerresult.Metric{{Name: "tps", Unit: "tx/s", Value: 1200.5}, {Name: "latency_avg", Unit: "ms", Value: 2.4}},
				}, nil
			}), "mariadb")
			request := CreateRunRequest{
				Target: Target{K8sCluster: "local", Namespace: "dbs", Instance: "mariadb-1"},
				Driver: "mariadb", Database: test.database, Options: json.RawMessage(test.options),
			}
			body, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			run := awaitAcceptedRun(t, a, submitRun(a, string(body)))
			var input coordinator.RunInput
			select {
			case input = <-calls:
			default:
				t.Fatal("accepted MariaDB run never reached execution")
			}
			if input.Runner != (driver.RunnerConfig{Image: mariadbRunnerImage, ImagePullPolicy: "Never"}) {
				t.Fatalf("unexpected registered runner: %+v", input.Runner)
			}
			if input.Spec.Env["MYSQL_DATABASE"] != test.wantDatabase || input.Spec.Env["MYSQL_SSL"] != "false" ||
				string(input.Spec.SecretData["MYSQL_PASSWORD"]) != credentials.Password ||
				input.Spec.SecretEnv["MYSQL_PASSWORD"] != "MYSQL_PASSWORD" {
				t.Fatal("MariaDB preparation lost the resolved database, Sysbench TLS setting, or Secret mapping")
			}
			if _, exists := input.Spec.Env["MYSQL_PASSWORD"]; exists {
				t.Fatal("password was included in literal environment settings")
			}
			var effective sysbench.Options
			if err := json.Unmarshal(input.Spec.Snapshot.Options, &effective); err != nil || effective != test.wantOptions {
				t.Fatalf("runner did not receive effective MariaDB options: %v", err)
			}
			if run.Status != RunStatusSucceeded || run.Snapshot.Driver != "mariadb" ||
				run.Snapshot.DatabaseType != "mysql" || run.Snapshot.RunnerImage != mariadbRunnerImage ||
				run.Snapshot.Database != test.wantDatabase || string(run.Snapshot.Options) != string(input.Spec.Snapshot.Options) {
				t.Fatalf("unexpected persisted MariaDB run: %+v", run)
			}
			const wantOutput = "sysbench complete password=[REDACTED] uri=[REDACTED]"
			if run.Output != wantOutput || len(run.Metrics) != 2 || run.Metrics[0].Name != "tps" || run.Metrics[1].Name != "latency_avg" {
				t.Fatalf("unexpected stored output or metrics: output=%q metrics=%+v", run.Output, run.Metrics)
			}
			encodedRun, err := json.Marshal(run)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{credentials.Password, credentials.URI, credentials.Host, credentials.Username, "SecretData", "SecretEnv", "MYSQL_PASSWORD"} {
				if strings.Contains(string(encodedRun), secret) {
					t.Fatalf("stored run exposed internal connection data: %s", secret)
				}
			}
			mux := http.NewServeMux()
			a.RegisterRoutes(mux)
			r := httptest.NewRequest(http.MethodGet, "/api/runs/"+run.ID, nil)
			r.Header.Set("Authorization", "Bearer test-token")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			var response RunStatusResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusOK || response.Driver != "mariadb" || response.Status != RunStatusSucceeded ||
				response.DatabaseType != "mysql" || response.Database != test.wantDatabase || response.Output != wantOutput ||
				len(response.Metrics) != 2 || response.Metrics[0].Value != 1200.5 {
				t.Fatalf("GET did not return MariaDB results: %s", w.Body.String())
			}
			for _, secret := range []string{credentials.Password, credentials.URI, credentials.Host, credentials.Username} {
				if strings.Contains(w.Body.String(), secret) {
					t.Fatal("GET exposed MariaDB connection credentials")
				}
			}
		})
	}
}

func TestMariaDBRejectedBeforeRunAdmission(t *testing.T) {
	for _, test := range []struct {
		name       string
		databaseID string
		body       string
		status     int
	}{
		{"driver not enabled", "mysql", mariadbRunBody, http.StatusBadRequest},
		{"target type mismatch", "mariadb", mariadbRunBody, http.StatusUnprocessableEntity},
		{"unknown option", "mariadb", strings.Replace(mariadbRunBody, `"options":{}`, `"options":{"duratonSeconds":5}`, 1), http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			credentials := mariadbCredentials()
			if test.name == "target type mismatch" {
				credentials.Provider = "percona-server-mysql"
			}
			var called atomic.Bool
			a := newMariaDBAPI(t, credentials, testRunnerFunc(func(context.Context, coordinator.RunInput) (coordinator.Result, error) {
				called.Store(true)
				return coordinator.Result{}, errors.New("unexpected runner call")
			}), test.databaseID)
			w := submitRun(a, test.body)
			if w.Code != test.status || called.Load() || storedRunCount(a) != 0 || len(a.runSlots) != 0 {
				t.Fatalf("POST = %d: %s; rejected request entered execution", w.Code, w.Body.String())
			}
			for _, secret := range []string{credentials.Password, credentials.URI, "mariadb-password"} {
				if strings.Contains(w.Body.String(), secret) {
					t.Fatal("rejected request exposed credentials")
				}
			}
		})
	}
}
