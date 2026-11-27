package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	"github.com/openeverest/plugin-bench/backend/internal/driver"
	"github.com/openeverest/plugin-bench/backend/internal/driver/valkey"
	"github.com/openeverest/plugin-bench/backend/internal/everest"
	"github.com/openeverest/plugin-bench/backend/internal/runnerresult"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const valkeyRunnerImage = "example.com/plugin-bench-valkey:test"

func newValkeyAPI(t *testing.T, credentials everest.Credentials, runner BenchmarkRunner) *API {
	t.Helper()
	registry, err := driver.NewRegistry(driver.Registration{
		Driver: valkey.New(),
		Runner: driver.RunnerConfig{Image: valkeyRunnerImage, ImagePullPolicy: "Never", ExecutionTimeout: 12 * time.Minute},
	})
	if err != nil {
		t.Fatal(err)
	}
	checkTarget := func(token, cluster, namespace, instance string) {
		if token != "test-token" || cluster != "local" || namespace != "dbs" || instance != "cache" {
			t.Error("OpenEverest lookup did not use the caller's token and Valkey target")
		}
	}
	return NewAPI(func(_ context.Context, token, cluster, namespace, instance string) (*everest.Credentials, error) {
		checkTarget(token, cluster, namespace, instance)
		return &credentials, nil
	}, func(_ context.Context, token, cluster, namespace, instance string) error {
		checkTarget(token, cluster, namespace, instance)
		return nil
	}, runner, NewRunStore(), context.Background(), coordinator.Resources{}, registry, nil)
}

func valkeyAPICredentials() everest.Credentials {
	return everest.Credentials{
		Type: "valkey", Provider: "valkey", Host: "cache-primary.dbs.svc", Port: "6379",
		Username: "default", Password: "valkey-credential-sentinel",
		URI: "redis://default:valkey-credential-sentinel@cache-primary.dbs.svc:6379",
	}
}

func TestValkeyAPIRunAndHistory(t *testing.T) {
	credentials := valkeyAPICredentials()
	const wantOptions = `{"duration":5,"threads":2,"clientsPerThread":10,"keyCount":100,"valueSize":256}`
	const terminationMessage = `{"version":1,"metrics":[{"name":"ops_per_second","unit":"ops/s","value":1000},{"name":"get_ops_per_second","unit":"ops/s","value":900},{"name":"set_ops_per_second","unit":"ops/s","value":100},{"name":"latency_p99","unit":"ms","value":1.5}]}`
	var record struct{ Metrics []runnerresult.Metric }
	if err := json.Unmarshal([]byte(terminationMessage), &record); err != nil {
		t.Fatal(err)
	}

	client := fake.NewSimpleClientset()
	var secretName string
	var job *batchv1.Job
	client.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		secret := action.(ktesting.CreateAction).GetObject().(*corev1.Secret)
		if len(secret.Data) != 1 || string(secret.Data["VALKEY_PASSWORD"]) != credentials.Password {
			t.Error("Valkey credentials were not written to the runner Secret")
		}
		secretName = secret.Name
		secret.UID = "valkey-secret-uid"
		return false, nil, nil
	})
	client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		created := action.(ktesting.CreateAction).GetObject().(*batchv1.Job)
		created.UID = "valkey-job-uid"
		created.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
		job = created.DeepCopy()
		return false, nil, nil
	})
	client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		controller := true
		return true, &corev1.PodList{Items: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Name: "valkey-runner", Labels: map[string]string{batchv1.JobNameLabel: job.Name},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: &controller}}},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				Name:  job.Spec.Template.Spec.Containers[0].Name,
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: terminationMessage}},
			}}},
		}}}, nil
	})
	runner, err := coordinator.New(coordinator.Config{
		InstallationID: "plugin-bench", WorkloadNamespace: "workloads", RunnerServiceAccount: "runner",
		ExecutionTimeout: 5 * time.Minute,
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	api := newValkeyAPI(t, credentials, runner)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)
	request := httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(
		`{"target":{"k8sCluster":"local","namespace":"dbs","instance":"cache"},"driver":"valkey","options":{"duration":5,"keyCount":100}}`))
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	run := awaitAcceptedRun(t, api, response)

	if job == nil || job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != 720 {
		t.Fatal("Valkey Job did not receive the 720-second deadline")
	}
	container := job.Spec.Template.Spec.Containers[0]
	if container.Image != valkeyRunnerImage || container.ImagePullPolicy != corev1.PullNever {
		t.Fatal("Valkey Job did not use the configured image and pull policy")
	}
	env := make(map[string]corev1.EnvVar)
	for _, variable := range container.Env {
		env[variable.Name] = variable
	}
	for name, value := range map[string]string{
		"VALKEY_HOST": credentials.Host, "VALKEY_PORT": "6379", "VALKEY_USERNAME": "default", "VALKEY_TLS": "false",
		"BENCH_DURATION": "5", "BENCH_KEY_COUNT": "100", "BENCH_THREADS": "2", "BENCH_CLIENTS_PER_THREAD": "10", "BENCH_VALUE_SIZE": "256",
	} {
		if env[name].Value != value {
			t.Errorf("Valkey Job has incorrect %s", name)
		}
	}
	password := env["VALKEY_PASSWORD"]
	if secretName == "" || password.Value != "" || password.ValueFrom == nil || password.ValueFrom.SecretKeyRef == nil ||
		password.ValueFrom.SecretKeyRef.Name != secretName || password.ValueFrom.SecretKeyRef.Key != "VALKEY_PASSWORD" {
		t.Fatal("Valkey Job password did not use its per-run Secret reference")
	}

	if run.Status != RunStatusSucceeded || run.Snapshot.Driver != "valkey" || run.Snapshot.DatabaseType != "valkey" ||
		run.Snapshot.RunnerImage != valkeyRunnerImage || run.Snapshot.Database != "0" ||
		string(run.Snapshot.Options) != wantOptions || !reflect.DeepEqual(run.Metrics, record.Metrics) {
		t.Fatalf("unexpected saved Valkey result: %+v", run)
	}
	get := func(path string, target any) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s returned %d", path, response.Code)
		}
		if strings.Contains(response.Body.String(), credentials.Password) {
			t.Fatalf("GET %s exposed Valkey credentials", path)
		}
		if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
			t.Fatal(err)
		}
	}
	var result RunStatusResponse
	get("/api/runs/"+run.ID, &result)
	if result.Status != RunStatusSucceeded || result.Database != "0" || !reflect.DeepEqual(result.Metrics, record.Metrics) {
		t.Fatalf("unexpected Valkey result response: %+v", result)
	}
	var history struct{ Runs []RunSummaryResponse }
	get("/api/runs?k8sCluster=local&namespace=dbs&instance=cache", &history)
	if len(history.Runs) != 1 || history.Runs[0].ID != run.ID || history.Runs[0].Status != RunStatusSucceeded ||
		history.Runs[0].Driver != "valkey" || !reflect.DeepEqual(history.Runs[0].Metrics, record.Metrics) {
		t.Fatalf("unexpected Valkey history response: %+v", history.Runs)
	}
}

func TestValkeyAPIRejectsInvalidRequestsBeforeRunner(t *testing.T) {
	for _, test := range []struct {
		name, fields string
		change       func(*everest.Credentials)
		status       int
	}{
		{"nonzero database", `"database":"1","options":{}`, nil, http.StatusBadRequest},
		{"invalid duration", `"options":{"duration":301}`, nil, http.StatusBadRequest},
		{"combined client limit", `"options":{"threads":8,"clientsPerThread":17}`, nil, http.StatusBadRequest},
		{"unsupported provider", `"options":{}`, func(c *everest.Credentials) { c.Provider = "other" }, http.StatusUnprocessableEntity},
		{"TLS without provider CA", `"options":{}`, func(c *everest.Credentials) { c.URI = strings.Replace(c.URI, "redis:", "rediss:", 1) }, http.StatusBadGateway},
	} {
		t.Run(test.name, func(t *testing.T) {
			credentials := valkeyAPICredentials()
			if test.change != nil {
				test.change(&credentials)
			}
			calls := make(chan struct{}, 1)
			api := newValkeyAPI(t, credentials, testRunnerFunc(func(context.Context, coordinator.RunInput) (coordinator.Result, error) {
				calls <- struct{}{}
				return coordinator.Result{}, nil
			}))
			response := submitRun(api, `{"target":{"k8sCluster":"local","namespace":"dbs","instance":"cache"},"driver":"valkey",`+test.fields+`}`)
			if response.Code != test.status || storedRunCount(api) != 0 || len(calls) != 0 {
				t.Fatalf("invalid Valkey request returned %d, want %d, with zero admitted runs", response.Code, test.status)
			}
			if strings.Contains(response.Body.String(), credentials.Password) {
				t.Fatal("error response exposed credentials")
			}
		})
	}
}
