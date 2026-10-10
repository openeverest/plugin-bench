package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	"github.com/openeverest/plugin-bench/backend/internal/everest"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestExecuteRunExposesOnlySchedulingDiagnostics(t *testing.T) {
	for _, scenario := range []string{"generic", "scheduling", "timeout with scheduling"} {
		t.Run(scenario, func(t *testing.T) {
			store := NewRunStore()
			if _, err := store.Create("run", CreateRunRequest{Target: Target{Instance: "postgres"}}, nil); err != nil {
				t.Fatal(err)
			}
			var runErr error = errors.New("private backend failure: secret-password")
			if scenario != "generic" {
				runErr = errors.Join(runErr, fmt.Errorf("wrapped: %w", &coordinator.SchedulingError{
					PodName: "runner", Message: "no node matches secret-password", GracePeriod: time.Minute,
				}))
			}
			if scenario == "timeout with scheduling" {
				runErr = errors.Join(runErr, context.DeadlineExceeded)
			}
			api := NewAPI(nil, func(context.Context, string, string, string, string) error { return nil },
				testRunnerFunc(func(context.Context, coordinator.Connection, coordinator.Options) (coordinator.Result, error) {
					return coordinator.Result{JobName: "job", Output: "secret-password"}, runErr
				}), store, context.Background())
			api.runSlots <- struct{}{}
			api.runs.Add(1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			api.executeRun(ctx, cancel, "run", coordinator.Connection{Password: "secret-password"}, coordinator.DefaultOptions())
			r := httptest.NewRequest(http.MethodGet, "/api/runs/run", nil)
			r.SetPathValue("id", "run")
			r.Header.Set("Authorization", "Bearer test-token")
			w := httptest.NewRecorder()
			api.getRun(w, r)
			var response RunStatusResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusOK || response.Status != RunStatusFailed || response.Output != "[REDACTED]" {
				t.Fatalf("unexpected status response: %s", w.Body.String())
			}
			if strings.Contains(w.Body.String(), "secret-password") || strings.Contains(response.Error, "private backend failure") {
				t.Fatal("status response exposed an unrelated error or credential")
			}
			if scenario != "generic" {
				if !strings.Contains(response.Error, "unschedulable for 1m0s") || !strings.Contains(response.Error, "no node matches [REDACTED]") {
					t.Fatalf("missing scheduling diagnostic: %q", response.Error)
				}
				if scenario == "timeout with scheduling" && !strings.Contains(response.Error, "execution timed out") {
					t.Fatalf("timeout context missing: %q", response.Error)
				}
			} else if response.Error != "benchmark execution failed" {
				t.Fatalf("unexpected generic error: %q", response.Error)
			}
			if len(api.runSlots) != 0 || ctx.Err() == nil {
				t.Fatal("execution did not release its slot and context")
			}
			api.runs.Wait()
		})
	}
}

func TestUnschedulableRunsReleaseCapacity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := fake.NewSimpleClientset()
		client.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
			action.(ktesting.CreateAction).GetObject().(*corev1.Secret).UID = "secret-uid"
			return false, nil, nil
		})
		client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
			job := action.(ktesting.CreateAction).GetObject().(*batchv1.Job)
			job.UID = types.UID("uid-" + job.Name)
			return false, nil, nil
		})
		client.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
			name, found := action.(ktesting.ListAction).GetListRestrictions().Labels.RequiresExactMatch(batchv1.JobNameLabel)
			if !found {
				t.Fatal("missing Job label selector")
			}
			controller := true
			return true, &corev1.PodList{Items: []corev1.Pod{{
				ObjectMeta: metav1.ObjectMeta{Name: name + "-pod", Namespace: "workloads", UID: types.UID("pod-" + name),
					Labels:          map[string]string{batchv1.JobNameLabel: name},
					OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: name, UID: types.UID("uid-" + name), Controller: &controller}},
				},
				Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled,
					Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable, Message: "no nodes match required affinity"}}},
			}}}, nil
		})
		runner, err := coordinator.New(coordinator.Config{WorkloadNamespace: "workloads", RunnerImage: "runner:test",
			ImagePullPolicy: "IfNotPresent", RunnerServiceAccount: "runner", ExecutionTimeout: 5 * time.Minute}, client)
		if err != nil {
			t.Fatal(err)
		}
		store := NewRunStore()
		lifecycleCtx, stop := context.WithCancel(context.Background())
		api := NewAPI(func(context.Context, string, string, string, string) (*everest.Credentials, error) {
			return testCredentials(), nil
		}, nil, runner, store, lifecycleCtx)
		defer func() { stop(); api.runs.Wait() }()
		submit := func() *httptest.ResponseRecorder {
			r := httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(validCreateRunBody))
			r.Header.Set("Authorization", "Bearer test-token")
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			api.createRun(w, r)
			return w
		}
		var ids []string
		for i := 0; i < maxConcurrentRuns; i++ {
			w := submit()
			if w.Code != http.StatusAccepted {
				t.Fatalf("submission failed: %s", w.Body.String())
			}
			var response struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, response.ID)
		}
		synctest.Wait()
		if w := submit(); w.Code != http.StatusTooManyRequests {
			t.Fatalf("expected full capacity, got %d", w.Code)
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		for _, id := range ids {
			run, err := store.Get(id)
			if err != nil || run.Status != RunStatusFailed || !strings.Contains(run.Error, "no nodes match required affinity") {
				t.Fatalf("unexpected failed run: %+v, %v", run, err)
			}
		}
		if len(api.runSlots) != 0 {
			t.Fatal("scheduling failures did not free run capacity")
		}
		if jobs, err := client.BatchV1().Jobs("workloads").List(context.Background(), metav1.ListOptions{}); err != nil || len(jobs.Items) != 0 {
			t.Fatal("failed Jobs were not deleted")
		}
		if secrets, err := client.CoreV1().Secrets("workloads").List(context.Background(), metav1.ListOptions{}); err != nil || len(secrets.Items) != 0 {
			t.Fatal("credential Secrets were not deleted")
		}
		if w := submit(); w.Code != http.StatusAccepted {
			t.Fatalf("capacity was not reusable: %s", w.Body.String())
		}
		// Finish the final test run before leaving the fake-clock bubble.
		time.Sleep(time.Minute)
		synctest.Wait()
		api.runs.Wait()
	})
}
