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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	"github.com/openeverest/plugin-bench/backend/internal/driver"
	"github.com/openeverest/plugin-bench/backend/internal/driver/sysbench"
	"github.com/openeverest/plugin-bench/backend/internal/everest"
	"github.com/openeverest/plugin-bench/backend/internal/runnerresult"
)

const sysbenchRunnerImage = "example.com/plugin-bench-sysbench:test"
const sysbenchRunBody = `{"target":{"k8sCluster":"local","namespace":"dbs","instance":"mysql-1"},"driver":"sysbench","options":{}}`

func sysbenchCredentials() *everest.Credentials {
	return &everest.Credentials{
		Host: "mysql.example.com", Port: "3306", Username: "sbuser", Password: "sysbench-password",
		Database: "sbtest", Type: "MySQL", Provider: "new-mysql-provider",
		URI: "mysql://sbuser:sysbench%2Dpassword@mysql.example.com:3306/sbtest",
	}
}

func newSysbenchAPI(t *testing.T, credentials *everest.Credentials, runner BenchmarkRunner, defaults coordinator.Resources) *API {
	t.Helper()
	registry, err := driver.NewRegistry(driver.Registration{
		Driver: sysbench.New(), Runner: driver.RunnerConfig{Image: sysbenchRunnerImage, ImagePullPolicy: "Never"},
	})
	if err != nil {
		t.Fatal(err)
	}
	checkTarget := func(token, cluster, namespace, instance string) {
		if token != "test-token" || cluster != "local" || namespace != "dbs" || instance != "mysql-1" {
			t.Error("OpenEverest lookup did not use the caller's token and target")
		}
	}
	return NewAPI(func(_ context.Context, token, cluster, namespace, instance string) (*everest.Credentials, error) {
		checkTarget(token, cluster, namespace, instance)
		return credentials, nil
	}, func(_ context.Context, token, cluster, namespace, instance string) error {
		checkTarget(token, cluster, namespace, instance)
		return nil
	}, runner, NewRunStore(), context.Background(), defaults, registry)
}

func getSysbenchRun(a *API, id, authorization string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	a.RegisterRoutes(mux)
	r := httptest.NewRequest(http.MethodGet, "/api/runs/"+id, nil)
	r.Header.Set("Authorization", authorization)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestSysbenchRunRetainsEffectiveSettings(t *testing.T) {
	for _, test := range []struct {
		name, database, options, wantDatabase string
		uriFallback                           bool
		wantOptions                           sysbench.Options
	}{
		{"defaults", "", `{}`, "sbtest", false, sysbench.Options{DurationSeconds: 60, Threads: 1, Tables: 1, TableSize: 10000}},
		{"explicit options", " custom ", `{"durationSeconds":5,"threads":4,"tables":2,"tableSize":500,"initialize":true}`, "custom", false, sysbench.Options{DurationSeconds: 5, Threads: 4, Tables: 2, TableSize: 500, Initialize: true}},
		{"URI database fallback", "", `{}`, "sbtest", true, sysbench.Options{DurationSeconds: 60, Threads: 1, Tables: 1, TableSize: 10000}},
	} {
		t.Run(test.name, func(t *testing.T) {
			credentials := sysbenchCredentials()
			if test.uriFallback {
				credentials.Database = ""
			}
			affinity := testNodeAffinity()
			calls := make(chan coordinator.RunInput, 1)
			a := newSysbenchAPI(t, credentials, testRunnerFunc(func(_ context.Context, input coordinator.RunInput) (coordinator.Result, error) {
				calls <- input
				return coordinator.Result{Output: "sysbench complete"}, nil
			}), coordinator.Resources{CPURequest: "100m", CPULimit: "1"})
			request := CreateRunRequest{
				Target: Target{K8sCluster: "local", Namespace: "dbs", Instance: "mysql-1"},
				Driver: "sysbench", Database: test.database, Options: json.RawMessage(test.options),
				Resources: coordinator.Resources{CPURequest: "500m"}, NodeAffinity: affinity,
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
				t.Fatal("accepted Sysbench run never reached execution")
			}
			if input.Runner != (driver.RunnerConfig{Image: sysbenchRunnerImage, ImagePullPolicy: "Never"}) ||
				input.Spec.Env["MYSQL_DATABASE"] != test.wantDatabase ||
				string(input.Spec.SecretData["MYSQL_PASSWORD"]) != credentials.Password ||
				input.Spec.SecretEnv["MYSQL_PASSWORD"] != "MYSQL_PASSWORD" {
				t.Fatal("execution lost the registered image, resolved database, or Secret mapping")
			}
			if _, exists := input.Spec.Env["MYSQL_PASSWORD"]; exists {
				t.Fatal("password was included in literal environment settings")
			}
			if input.Resources != request.Resources || !reflect.DeepEqual(input.NodeAffinity, affinity) {
				t.Fatal("Sysbench preparation lost shared resource or affinity overrides")
			}
			var effective sysbench.Options
			if err := json.Unmarshal(input.Spec.Snapshot.Options, &effective); err != nil || effective != test.wantOptions {
				t.Fatalf("execution did not receive effective options: %v", err)
			}
			// Mutating a completed runner's input must not alter saved history.
			input.Spec.Snapshot.Options[0] = 'X'
			mutateTestNodeAffinity(input.NodeAffinity)
			w := getSysbenchRun(a, run.ID, "Bearer test-token")
			var response RunStatusResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusOK || response.Status != RunStatusSucceeded || response.Driver != "sysbench" ||
				response.RunnerImage != sysbenchRunnerImage || response.Database != test.wantDatabase || response.Output != "sysbench complete" {
				t.Fatalf("unexpected Sysbench run status response: %s", w.Body.String())
			}
			if err := json.Unmarshal(response.Options, &effective); err != nil || effective != test.wantOptions {
				t.Fatalf("GET lost effective options: %v", err)
			}
			if response.Resources != (coordinator.Resources{CPURequest: "500m", CPULimit: "1"}) || !reflect.DeepEqual(&response.NodeAffinity, affinity) {
				t.Fatal("GET lost effective resource defaults or its owned affinity snapshot")
			}
			for _, private := range []string{credentials.Password, credentials.URI, credentials.Host, credentials.Username, "SecretData", "SecretEnv", "MYSQL_PASSWORD"} {
				if strings.Contains(w.Body.String(), private) {
					t.Fatal("GET exposed internal preparation data")
				}
			}
		})
	}
}

func TestSysbenchPreparationErrorsRejectBeforeAdmission(t *testing.T) {
	for _, test := range []struct {
		name, body string
		change     func(*everest.Credentials)
		disabled   bool
		status     int
	}{
		{"unknown option", strings.Replace(sysbenchRunBody, `"options":{}`, `"options":{"credential-sentinel":1}`, 1), nil, false, 400},
		{"zero threads", strings.Replace(sysbenchRunBody, `"options":{}`, `"options":{"threads":0}`, 1), nil, false, 400},
		{"null table size", strings.Replace(sysbenchRunBody, `"options":{}`, `"options":{"tableSize":null}`, 1), nil, false, 400},
		{"missing database", sysbenchRunBody, func(c *everest.Credentials) { c.Database, c.URI = "", "" }, false, 400},
		{"disabled driver", sysbenchRunBody, nil, true, 400},
		{"unsupported target", sysbenchRunBody, func(c *everest.Credentials) { c.Type = "postgresql" }, false, 422},
		{"URI-only credentials", sysbenchRunBody, func(c *everest.Credentials) { c.Password = "" }, false, 422},
		{"invalid port", sysbenchRunBody, func(c *everest.Credentials) { c.Port = "credential-sentinel" }, false, 502},
		{"missing host", sysbenchRunBody, func(c *everest.Credentials) { c.Host = "" }, false, 502},
		{"missing database type", sysbenchRunBody, func(c *everest.Credentials) { c.Type = "" }, false, 502},
		{"unrepresentable password", sysbenchRunBody, func(c *everest.Credentials) { c.Password = "credential-sentinel\nmysql-debug=on" }, false, 502},
	} {
		t.Run(test.name, func(t *testing.T) {
			credentials := sysbenchCredentials()
			if test.change != nil {
				test.change(credentials)
			}
			var called atomic.Bool
			a := newSysbenchAPI(t, credentials, testRunnerFunc(func(context.Context, coordinator.RunInput) (coordinator.Result, error) {
				called.Store(true)
				return coordinator.Result{}, nil
			}), coordinator.Resources{})
			if test.disabled {
				a.registry = testRegistry()
			}
			w := submitRun(a, test.body)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := a.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			if w.Code != test.status || called.Load() || len(a.store.runs) != 0 || len(a.runSlots) != 0 {
				t.Fatalf("POST = %d: %s; invalid request was admitted", w.Code, w.Body.String())
			}
			for _, secret := range []string{"credential-sentinel", "sysbench-password", credentials.URI} {
				if secret != "" && strings.Contains(w.Body.String(), secret) {
					t.Fatal("preparation error exposed credentials")
				}
			}
		})
	}
}

func TestSysbenchResultsRedactSecretsAndPreserveMetrics(t *testing.T) {
	safeMetrics := []runnerresult.Metric{{Name: "tps", Unit: "tx/s", Value: 3306.25}, {Name: "latency_avg", Unit: "ms", Value: 3.2}}
	for _, test := range []struct {
		name    string
		metrics bool
		failed  bool
	}{
		{"successful metrics", true, false},
		{"failed run with metrics", true, true},
		{"successful run without metrics", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			credentials := sysbenchCredentials()
			var diagnostics bytes.Buffer
			original := log.Writer()
			log.SetOutput(&diagnostics)
			defer log.SetOutput(original)
			a := newSysbenchAPI(t, credentials, testRunnerFunc(func(context.Context, coordinator.RunInput) (coordinator.Result, error) {
				output := "host=mysql.example.com user=sbuser port=3306 tps=3306.25 password=" + credentials.Password + " uri=" + credentials.URI
				result := coordinator.Result{Output: output, OutputTruncated: true}
				if test.metrics {
					result.Metrics = append([]runnerresult.Metric(nil), safeMetrics...)
					result.Metrics = append(result.Metrics, runnerresult.Metric{Name: credentials.Password, Unit: "ms", Value: 1}, runnerresult.Metric{Name: "unsafe", Unit: credentials.URI, Value: 2})
				}
				if test.failed {
					return result, errors.New(output)
				}
				return result, nil
			}), coordinator.Resources{})
			run := awaitAcceptedRun(t, a, submitRun(a, sysbenchRunBody))
			wantStatus, wantError := RunStatusSucceeded, ""
			if test.failed {
				wantStatus, wantError = RunStatusFailed, "benchmark execution failed"
			}
			var wantMetrics []runnerresult.Metric
			if test.metrics {
				wantMetrics = safeMetrics
			}
			const wantOutput = "host=mysql.example.com user=sbuser port=3306 tps=3306.25 password=[REDACTED] uri=[REDACTED]"
			if run.Status != wantStatus || run.Error != wantError || run.Output != wantOutput || !run.OutputTruncated || !reflect.DeepEqual(run.Metrics, wantMetrics) {
				t.Fatal("stored results lost status, redacted output, or structured metrics")
			}
			w := getSysbenchRun(a, run.ID, "Bearer test-token")
			var response RunStatusResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusOK || response.Status != wantStatus || response.Error != wantError || response.Output != wantOutput ||
				!response.OutputTruncated || !reflect.DeepEqual(response.Metrics, wantMetrics) {
				t.Fatal("GET did not retain stored output, status, and metrics")
			}
			if strings.Contains(w.Body.String(), `"metrics":`) != test.metrics {
				t.Fatal("unavailable metrics must be omitted")
			}
			for _, secret := range []string{credentials.Password, credentials.URI} {
				if strings.Contains(w.Body.String(), secret) || strings.Contains(diagnostics.String(), secret) {
					t.Fatal("API results or backend diagnostics exposed credentials")
				}
			}
			if test.failed && !strings.Contains(diagnostics.String(), wantOutput) {
				t.Fatal("failure diagnostic did not preserve safely redacted details")
			}
		})
	}
}

func TestSysbenchRunResultsRequireTargetAccess(t *testing.T) {
	a := newSysbenchAPI(t, sysbenchCredentials(), testRunnerFunc(func(context.Context, coordinator.RunInput) (coordinator.Result, error) {
		return coordinator.Result{Output: "private-sysbench-output", Metrics: []runnerresult.Metric{{Name: "tps", Unit: "tx/s", Value: 10}}}, nil
	}), coordinator.Resources{})
	run := awaitAcceptedRun(t, a, submitRun(a, sysbenchRunBody))
	for _, test := range []struct {
		name, authorization string
		accessErr           error
		status              int
	}{
		{"unauthenticated", "", nil, http.StatusUnauthorized},
		{"unauthorized", "Bearer test-token", everest.ErrUnauthorized, http.StatusUnauthorized},
		{"forbidden", "Bearer test-token", everest.ErrForbidden, http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			a.checkInstanceAccess = func(_ context.Context, token, cluster, namespace, instance string) error {
				if token != "test-token" || cluster != run.Target.K8sCluster || namespace != run.Target.Namespace || instance != run.Target.Instance {
					t.Error("GET did not authorize the stored run target")
				}
				return test.accessErr
			}
			w := getSysbenchRun(a, run.ID, test.authorization)
			if w.Code != test.status || strings.Contains(w.Body.String(), "private-sysbench-output") ||
				strings.Contains(w.Body.String(), `"metrics":`) || strings.Contains(w.Body.String(), `"options":`) || strings.Contains(w.Body.String(), sysbenchRunnerImage) {
				t.Fatalf("unauthorized GET exposed run details: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
