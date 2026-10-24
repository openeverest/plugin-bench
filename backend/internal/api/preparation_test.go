package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	"github.com/openeverest/plugin-bench/backend/internal/driver"
	"github.com/openeverest/plugin-bench/backend/internal/everest"
	"github.com/openeverest/plugin-bench/backend/internal/runnerresult"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestCanonicalRunStoresEffectiveOptionsAndOwnedSnapshots(t *testing.T) {
	const wantOptions = `{"durationSeconds":60,"clients":1,"threads":1,"scale":1,"initialize":false}`
	affinity := testNodeAffinity()
	affinityJSON, err := json.Marshal(affinity)
	if err != nil {
		t.Fatal(err)
	}
	defaults := coordinator.Resources{CPURequest: "100m", CPULimit: "1"}
	a := newTestAPI(func(context.Context, string, string, string, string) (*everest.Credentials, error) {
		creds := testCredentials()
		creds.Provider = "new-postgresql-provider"
		return creds, nil
	}, func(context.Context, string, string, string, string) error { return nil }, testRunnerFunc(func(_ context.Context, input coordinator.RunInput) (coordinator.Result, error) {
		if input.Runner.Image != testRunnerImage || input.Spec.Env["BENCH_DURATION"] != "60" || string(input.Spec.Snapshot.Options) != wantOptions {
			t.Error("execution did not use the registered image and defaulted options")
		}
		if input.Resources != (coordinator.Resources{CPURequest: "500m"}) || !reflect.DeepEqual(input.NodeAffinity, affinity) {
			t.Error("canonical request lost shared settings")
		}
		mutateTestNodeAffinity(input.NodeAffinity)
		input.Spec.Snapshot.Options[0] = 'X'
		input.Spec.Env["PGDATABASE"] = "changed"
		return coordinator.Result{Output: "complete"}, nil
	}), NewRunStore(), context.Background(), defaults)
	body := strings.TrimSuffix(modernRunBody, "}") + `,"resources":{"cpuRequest":"500m"},"nodeAffinity":` + string(affinityJSON) + "}"
	run := awaitAcceptedRun(t, a, submitRun(a, body))
	if run.Status != RunStatusSucceeded || run.Snapshot.Driver != "pgbench" || run.Snapshot.RunnerImage != testRunnerImage ||
		run.Snapshot.Database != "bench" || string(run.Snapshot.Options) != wantOptions {
		t.Fatalf("invalid stored snapshot: %+v", run.Snapshot)
	}
	if run.Resources != (coordinator.Resources{CPURequest: "500m", CPULimit: "1"}) || !reflect.DeepEqual(&run.NodeAffinity, affinity) {
		t.Fatal("canonical request did not retain effective resource and affinity snapshots")
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
	if w.Code != http.StatusOK || response.Driver != "pgbench" || response.RunnerImage != testRunnerImage || string(response.Options) != wantOptions {
		t.Fatalf("bad GET: %s", w.Body.String())
	}
	for _, secret := range []string{testCredentials().Host, testCredentials().Username, testCredentials().Password, "SecretData", "SecretEnv", "SensitiveValues", "PGDATABASE"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("GET exposed internal preparation data")
		}
	}
}

func TestPreparationErrorsRejectBeforeAdmission(t *testing.T) {
	for _, test := range []struct {
		name, body string
		change     func(*everest.Credentials)
		status     int
	}{
		{"unknown driver", strings.Replace(modernRunBody, `"pgbench"`, `"unregistered"`, 1), nil, 400},
		{"typo in options", strings.Replace(modernRunBody, `"options":{}`, `"options":{"client":4}`, 1), nil, 400},
		{"explicit zero", strings.Replace(modernRunBody, `"options":{}`, `"options":{"clients":0}`, 1), nil, 400},
		{"explicit null", strings.Replace(modernRunBody, `"options":{}`, `"options":{"clients":null}`, 1), nil, 400},
		{"legacy missing numbers", `{"target":{"k8sCluster":"local","namespace":"dbs","instance":"db"},"database":"bench"}`, nil, 400},
		{"unsupported engine", modernRunBody, func(c *everest.Credentials) { c.Type = "mongodb" }, 422},
		{"URI only", modernRunBody, func(c *everest.Credentials) { c.Password = ""; c.URI = "postgres://user:credential-sentinel@host/db" }, 422},
		{"invalid upstream port", modernRunBody, func(c *everest.Credentials) { c.Port = "credential-sentinel" }, 502},
		{"missing upstream host", modernRunBody, func(c *everest.Credentials) { c.Host = "" }, 502},
		{"missing upstream type", modernRunBody, func(c *everest.Credentials) { c.Type = "" }, 502},
	} {
		t.Run(test.name, func(t *testing.T) {
			var called atomic.Bool
			a := newTestAPI(func(context.Context, string, string, string, string) (*everest.Credentials, error) {
				creds := testCredentials()
				if test.change != nil {
					test.change(creds)
				}
				return creds, nil
			}, nil, testRunnerFunc(func(context.Context, coordinator.RunInput) (coordinator.Result, error) {
				called.Store(true)
				return coordinator.Result{}, nil
			}), NewRunStore(), context.Background(), coordinator.Resources{})
			w := submitRun(a, test.body)
			if w.Code != test.status || called.Load() || len(a.store.runs) != 0 || len(a.runSlots) != 0 {
				t.Fatalf("POST = %d: %s; invalid request was admitted", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "credential-sentinel") || strings.Contains(w.Body.String(), testCredentials().Password) {
				t.Fatal("preparation error leaked credentials")
			}
		})
	}
	for _, absent := range []string{"registry", "credentials"} {
		t.Run(absent, func(t *testing.T) {
			a := testAPI()
			if absent == "registry" {
				a.registry = nil
			} else {
				a.getCredentials = func(context.Context, string, string, string, string) (*everest.Credentials, error) { return nil, nil }
			}
			w := submitRun(a, modernRunBody)
			want := http.StatusBadGateway
			if absent == "registry" {
				want = http.StatusServiceUnavailable
			}
			if w.Code != want || len(a.store.runs) != 0 {
				t.Fatalf("missing %s: %d, %s", absent, w.Code, w.Body.String())
			}
		})
	}
}

func TestPreparedSecretsAreRedactedFromOutputAndDiagnostics(t *testing.T) {
	const uri = "postgres://bench-user:bench-password@postgres.example.com:5432/bench"
	var diagnostics bytes.Buffer
	original := log.Writer()
	log.SetOutput(&diagnostics)
	t.Cleanup(func() { log.SetOutput(original) })
	a := newTestAPI(func(context.Context, string, string, string, string) (*everest.Credentials, error) {
		creds := testCredentials()
		creds.URI = uri
		return creds, nil
	}, nil, testRunnerFunc(func(context.Context, coordinator.RunInput) (coordinator.Result, error) {
		text := uri + " password=bench-password host=postgres.example.com user=bench-user port=5432"
		return coordinator.Result{Output: text}, errors.New(text)
	}), NewRunStore(), context.Background(), coordinator.Resources{})
	run := awaitAcceptedRun(t, a, submitRun(a, modernRunBody))
	if run.Status != RunStatusFailed || !strings.Contains(run.Output, "[REDACTED]") {
		t.Fatal("failed execution lost status or redaction")
	}
	for _, value := range []string{uri, "bench-password"} {
		if strings.Contains(run.Output, value) || strings.Contains(diagnostics.String(), value) {
			t.Fatal("credential exposed in output or diagnostic log")
		}
	}
	const wantOutput = "[REDACTED] password=[REDACTED] host=postgres.example.com user=bench-user port=5432"
	if run.Output != wantOutput || !strings.Contains(diagnostics.String(), wantOutput) {
		t.Fatal("redaction changed ordinary connection settings in output or diagnostics")
	}
	if run.Snapshot.Database != "bench" {
		t.Fatal("safe database metadata must remain available")
	}
}

func TestRunOutputPreservesBenchmarkTextAndNumbers(t *testing.T) {
	for _, database := range []string{"bench", "a"} {
		t.Run(database, func(t *testing.T) {
			uri := "postgres://a:bench%2Dpassword@db:5432/" + database
			const output = "pgbench: starting benchmark\ntps = 5432.10\nlatency average = 1.23 ms\n"
			a := newTestAPI(func(context.Context, string, string, string, string) (*everest.Credentials, error) {
				creds := testCredentials()
				creds.Host, creds.Username, creds.URI = "db", "a", uri
				return creds, nil
			}, func(context.Context, string, string, string, string) error { return nil },
				testRunnerFunc(func(_ context.Context, input coordinator.RunInput) (coordinator.Result, error) {
					if string(input.Spec.SecretData["PGPASSWORD"]) != "bench-password" {
						t.Error("execution credentials were changed by redaction preparation")
					}
					return coordinator.Result{Output: output + "password=bench-password uri=" + uri}, nil
				}), NewRunStore(), context.Background(), coordinator.Resources{})
			body := strings.Replace(modernRunBody, `"database":"bench"`, `"database":"`+database+`"`, 1)
			run := awaitAcceptedRun(t, a, submitRun(a, body))
			want := output + "password=[REDACTED] uri=[REDACTED]"
			if run.Status != RunStatusSucceeded || run.Output != want {
				t.Fatalf("stored output = %q; want %q", run.Output, want)
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
			if w.Code != http.StatusOK || response.Output != want {
				t.Fatalf("GET output = %q; want %q (status %d)", response.Output, want, w.Code)
			}
		})
	}
}

func TestRedactionPrefersFullRepresentationsAndDoesNotRewriteReplacement(t *testing.T) {
	values := redactionValues(driver.RunnerSpec{SecretData: map[string][]byte{"short": []byte("pass"), "empty": nil}, SensitiveValues: []string{"scheme:pass", "pass", "", "REDACTED"}})
	got := sanitizeRunnerOutput("scheme:pass pass", values...)
	if got != "[REDACTED] [REDACTED]" || len(values) != 3 {
		t.Fatalf("redaction = %q; candidates=%d", got, len(values))
	}
}

type fakeWorkloadDriver struct {
	spec driver.RunnerSpec
	err  error
}

func (fakeWorkloadDriver) ID() string { return "fake-tool" }
func (fakeWorkloadDriver) Supports(target driver.TargetInfo) bool {
	return target.DatabaseType == "arbitrary-engine"
}
func (d fakeWorkloadDriver) Prepare(driver.PrepareInput) (driver.RunnerSpec, error) {
	return d.spec, d.err
}

func TestInvalidDriverPreparationCannotAdmitRun(t *testing.T) {
	for _, test := range []struct {
		name           string
		implementation fakeWorkloadDriver
	}{
		{"invalid workload", fakeWorkloadDriver{spec: driver.RunnerSpec{Env: map[string]string{"invalid/name": "credential-sentinel"}}}},
		{"internal driver error", fakeWorkloadDriver{err: errors.New("credential-sentinel")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry, err := driver.NewRegistry(driver.Registration{Driver: test.implementation, Runner: driver.RunnerConfig{Image: "tool:v1", ImagePullPolicy: "Never"}})
			if err != nil {
				t.Fatal(err)
			}
			var called atomic.Bool
			a := NewAPI(func(context.Context, string, string, string, string) (*everest.Credentials, error) {
				return &everest.Credentials{Type: "arbitrary-engine"}, nil
			}, nil,
				testRunnerFunc(func(context.Context, coordinator.RunInput) (coordinator.Result, error) {
					called.Store(true)
					return coordinator.Result{}, nil
				}),
				NewRunStore(), context.Background(), coordinator.Resources{}, registry)
			w := submitRun(a, strings.Replace(modernRunBody, `"pgbench"`, `"fake-tool"`, 1))
			if w.Code != http.StatusInternalServerError || called.Load() || len(a.store.runs) != 0 || len(a.runSlots) != 0 || strings.Contains(w.Body.String(), "credential-sentinel") {
				t.Fatalf("invalid preparation caused admission or unsafe error: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestPreparedDriverWorkloadExecutesThroughSharedCoordinator(t *testing.T) {
	testPreparedDriverExecution(t, "", nil)
}

func TestPreparedDriverTerminationMetricsReachAuthorizedResponse(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "runner-result-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, message string
		want          []runnerresult.Metric
	}{
		{"shared version-1 record", string(fixture), []runnerresult.Metric{
			{Name: "tps", Unit: "tx/s", Value: 1234.5}, {Name: "latency_avg", Unit: "ms", Value: 3.2},
		}},
		{"invalid record remains successful", "private-token malformed record", nil},
		{"secret metadata is filtered", `{"version":1,"metrics":[{"name":"private-token","unit":"count","value":1},{"name":"tps","unit":"tx/s","value":5432.1}]}`,
			[]runnerresult.Metric{{Name: "tps", Unit: "tx/s", Value: 5432.1}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			testPreparedDriverExecution(t, test.message, test.want)
		})
	}
}

func testPreparedDriverExecution(t *testing.T, message string, wantMetrics []runnerresult.Metric) {
	t.Helper()
	spec := driver.RunnerSpec{Command: []string{"arbitrary-tool"}, Args: []string{"--measure"},
		Env: map[string]string{"TOOL_MODE": "measure"}, SecretData: map[string][]byte{"token": []byte("private-token")},
		SecretEnv: map[string]string{"TOOL_AUTH": "token"}, Snapshot: driver.OptionSnapshot{Database: "app", Options: []byte(`{"mode":"measure"}`)}}
	registry, err := driver.NewRegistry(driver.Registration{Driver: fakeWorkloadDriver{spec: spec}, Runner: driver.RunnerConfig{Image: "example.com/arbitrary-tool:v2", ImagePullPolicy: "Never"}})
	if err != nil {
		t.Fatal(err)
	}
	client := fake.NewSimpleClientset()
	var jobName, image, containerName string
	client.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		secret := action.(ktesting.CreateAction).GetObject().(*corev1.Secret)
		if len(secret.Data) != 1 || string(secret.Data["token"]) != "private-token" {
			t.Error("generic Secret data changed")
		}
		secret.UID = "secret-uid"
		return false, nil, nil
	})
	client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		job := action.(ktesting.CreateAction).GetObject().(*batchv1.Job)
		job.UID = "job-uid"
		jobName = job.Name
		container := job.Spec.Template.Spec.Containers[0]
		containerName = container.Name
		image = container.Image
		if container.Resources.Requests.Cpu().String() != "0" || container.Resources.Limits.Memory().String() != "0" {
			t.Error("inherited zero defaults changed")
		}
		if len(container.Command) != 1 || container.Command[0] != "arbitrary-tool" || container.Args[0] != "--measure" || container.ImagePullPolicy != corev1.PullNever || len(container.Env) != 2 {
			t.Error("generic workload changed in Job")
		}
		job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
		return false, nil, nil
	})
	controller := true
	client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.PodList{Items: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Name: "runner", Labels: map[string]string{batchv1.JobNameLabel: jobName},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: jobName, UID: "job-uid", Controller: &controller}}},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
				{Name: "sidecar", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					Message: `{"version":1,"metrics":[{"name":"sidecar_metric","unit":"count","value":999}]}`,
				}}},
				{Name: containerName, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: message}}},
			}},
		}}}, nil
	})
	defaults := coordinator.Resources{CPURequest: "0", MemoryLimit: "0"}
	runner, err := coordinator.New(coordinator.Config{WorkloadNamespace: "workloads", RunnerServiceAccount: "runner", ExecutionTimeout: time.Minute, Resources: defaults}, client)
	if err != nil {
		t.Fatal(err)
	}
	accessChecked := false
	a := NewAPI(func(context.Context, string, string, string, string) (*everest.Credentials, error) {
		return &everest.Credentials{Type: "arbitrary-engine"}, nil
	}, func(_ context.Context, token, cluster, namespace, instance string) error {
		accessChecked = true
		if token != "test-token" || cluster != "local" || namespace != "dbs" || instance != "postgres-1" {
			t.Error("metrics lookup did not authorize the stored target")
		}
		return nil
	}, runner, NewRunStore(), context.Background(), defaults, registry)
	body := strings.Replace(modernRunBody, `"pgbench"`, `"fake-tool"`, 1)
	run := awaitAcceptedRun(t, a, submitRun(a, body))
	if run.Status != RunStatusSucceeded || run.Snapshot.Driver != "fake-tool" || run.Snapshot.RunnerImage != image || image != "example.com/arbitrary-tool:v2" || run.Output != "fake logs" {
		t.Fatalf("prepared execution failed: %+v", run)
	}
	if run.Resources != defaults {
		t.Fatal("resource snapshot did not preserve inherited zero defaults")
	}
	if !reflect.DeepEqual(run.Metrics, wantMetrics) {
		t.Fatalf("stored metrics = %+v; want %+v", run.Metrics, wantMetrics)
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
	if w.Code != http.StatusOK || !accessChecked || response.Status != RunStatusSucceeded ||
		response.Output != "fake logs" || !reflect.DeepEqual(response.Metrics, wantMetrics) {
		t.Fatalf("authorized result response is incorrect: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "private-token") || strings.Contains(w.Body.String(), `"metrics":`) != (len(wantMetrics) > 0) {
		t.Fatal("GET exposed a secret or returned unavailable metrics")
	}
	jobDeleted, secretDeleted := false, false
	for _, action := range client.Actions() {
		jobDeleted = jobDeleted || action.Matches("delete", "jobs")
		secretDeleted = secretDeleted || action.Matches("delete", "secrets")
	}
	if !jobDeleted || !secretDeleted || len(a.runSlots) != 0 {
		t.Fatal("execution did not clean up and release capacity")
	}
}
