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
	"github.com/openeverest/plugin-bench/backend/internal/everest"
	corev1 "k8s.io/api/core/v1"
)

func testTolerations() []corev1.Toleration {
	seconds := int64(120)
	return []corev1.Toleration{
		{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "benchmark", Effect: corev1.TaintEffectNoSchedule},
		{Key: "dedicated", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &seconds},
	}
}

func mutateTestTolerations(input []corev1.Toleration) {
	input[0].Key = "changed"
	*input[1].TolerationSeconds = 1
}

func bodyWithTolerations(value string) string {
	if value == "" {
		return validCreateRunBody
	}
	return strings.TrimSuffix(validCreateRunBody, "}") + `,"tolerations":` + value + "}"
}

func TestCreateRunPreparesTolerationsAndKeepsStoredSnapshot(t *testing.T) {
	zero := int64(0)
	for _, test := range []struct {
		name, value string
		keys        []string
		want        []corev1.Toleration
	}{
		{"omitted", "", nil, []corev1.Toleration{}},
		{"null", "null", nil, []corev1.Toleration{}},
		{"empty", "[]", nil, []corev1.Toleration{}},
		{"default operator", `[{"key":"dedicated","value":"benchmark"}]`, []string{"dedicated"}, []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "benchmark"}}},
		{"indefinite", `[{"key":"dedicated","effect":"NoExecute"}]`, []string{"dedicated"}, []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpEqual, Effect: corev1.TaintEffectNoExecute}}},
		{"zero seconds", `[{"key":"dedicated","effect":"NoExecute","tolerationSeconds":0}]`, []string{"dedicated"}, []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpEqual, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &zero}}},
		{"all fields", `[{"key":"dedicated","operator":"Equal","value":"benchmark","effect":"NoSchedule"},{"key":"dedicated","operator":"Exists","effect":"NoExecute","tolerationSeconds":120}]`, []string{"dedicated"}, testTolerations()},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewRunStore()
			calls := make(chan coordinator.Options, 1)
			release := make(chan struct{})
			keys := append([]string(nil), test.keys...)
			a := NewAPI(func(context.Context, string, string, string, string) (*everest.Credentials, error) {
				return testCredentials(), nil
			}, nil, testRunnerFunc(func(_ context.Context, _ coordinator.Connection, options coordinator.Options) (coordinator.Result, error) {
				observed := options
				observed.Tolerations = (&corev1.PodSpec{Tolerations: options.Tolerations}).DeepCopy().Tolerations
				// A runner may modify its input; it must not rewrite run history.
				if len(options.Tolerations) > 0 {
					options.Tolerations[0].Key = "changed-by-runner"
				}
				if len(options.Tolerations) > 1 {
					*options.Tolerations[1].TolerationSeconds = 1
				}
				calls <- observed
				<-release
				return coordinator.Result{}, nil
			}), store, context.Background(), coordinator.Resources{}, keys)
			t.Cleanup(func() {
				close(release)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := a.Shutdown(ctx); err != nil {
					t.Error(err)
				}
			})
			if len(keys) > 0 {
				keys[0] = "production"
			}
			body := bodyWithTolerations(test.value)
			if test.name == "all fields" {
				affinityJSON, err := json.Marshal(testNodeAffinity())
				if err != nil {
					t.Fatal(err)
				}
				body = strings.TrimSuffix(body, "}") + `,"nodeAffinity":` + string(affinityJSON) + `,"resources":{"cpuRequest":"500m"}}`
			}
			r := httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer test-token")
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			a.createRun(w, r)
			if w.Code != http.StatusAccepted {
				t.Fatalf("status=%d, body=%s", w.Code, w.Body.String())
			}
			var accepted map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil {
				t.Fatal(err)
			}
			if len(accepted) != 2 || accepted["id"] == "" || accepted["status"] != "running" {
				t.Fatalf("acceptance contract changed: %v", accepted)
			}
			select {
			case options := <-calls:
				if !reflect.DeepEqual(options.Tolerations, test.want) {
					t.Fatalf("prepared tolerations = %+v, want %+v", options.Tolerations, test.want)
				}
				if test.name == "all fields" && (!reflect.DeepEqual(options.NodeAffinity, testNodeAffinity()) || options.Resources.CPURequest != "500m") {
					t.Fatal("tolerations did not coexist with affinity and resource overrides")
				}
			case <-time.After(time.Second):
				t.Fatal("runner was not called")
			}
			run, err := store.Get(accepted["id"])
			if err != nil || run.Status != RunStatusRunning || !reflect.DeepEqual(run.Tolerations, test.want) {
				t.Fatalf("runner changed stored snapshot: %+v, %v", run, err)
			}
			if test.name == "default operator" && run.Request.Tolerations[0].Operator != "" {
				t.Fatal("prepared defaults overwrote the original request")
			}
		})
	}
}

func TestCreateRunRejectsTolerationsBeforeAdmission(t *testing.T) {
	for _, test := range []struct {
		name, value, errorPath string
		keys                   []string
	}{
		{"empty allowlist", `[{"key":"dedicated"}]`, "tolerations[0].key", nil},
		{"forbidden key", `[{"key":"production"}]`, "tolerations[0].key", []string{"dedicated"}},
		{"empty exists key", `[{"operator":"Exists"}]`, "tolerations[0].key", []string{"dedicated"}},
		{"invalid operator", `[{"key":"dedicated","operator":"In"}]`, "tolerations[0].operator", []string{"dedicated"}},
		{"invalid value", `[{"key":"dedicated","value":"invalid value"}]`, "tolerations[0].value", []string{"dedicated"}},
		{"exists value", `[{"key":"dedicated","operator":"Exists","value":"benchmark"}]`, "tolerations[0].value", []string{"dedicated"}},
		{"invalid effect", `[{"key":"dedicated","effect":"Schedule"}]`, "tolerations[0].effect", []string{"dedicated"}},
		{"negative seconds", `[{"key":"dedicated","effect":"NoExecute","tolerationSeconds":-1}]`, "tolerations[0].tolerationSeconds", []string{"dedicated"}},
		{"seconds wrong effect", `[{"key":"dedicated","effect":"NoSchedule","tolerationSeconds":0}]`, "tolerations[0].tolerationSeconds", []string{"dedicated"}},
		{"unknown field", `[{"key":"dedicated","unknown":true}]`, "", []string{"dedicated"}},
		{"wrong collection type", `{}`, "", []string{"dedicated"}},
		{"null entry", `[null]`, "tolerations[0].key", []string{"dedicated"}},
		{"wrong key type", `[{"key":42}]`, "", []string{"dedicated"}},
		{"wrong value type", `[{"key":"dedicated","value":true}]`, "", []string{"dedicated"}},
		{"fractional seconds", `[{"key":"dedicated","effect":"NoExecute","tolerationSeconds":1.5}]`, "", []string{"dedicated"}},
		{"overflow seconds", `[{"key":"dedicated","effect":"NoExecute","tolerationSeconds":9223372036854775808}]`, "", []string{"dedicated"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var credentialsCalled, runnerCalled atomic.Bool
			store := NewRunStore()
			a := NewAPI(func(context.Context, string, string, string, string) (*everest.Credentials, error) {
				credentialsCalled.Store(true)
				return testCredentials(), nil
			}, nil, testRunnerFunc(func(context.Context, coordinator.Connection, coordinator.Options) (coordinator.Result, error) {
				runnerCalled.Store(true)
				return coordinator.Result{}, nil
			}), store, context.Background(), coordinator.Resources{}, test.keys)
			r := httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(bodyWithTolerations(test.value)))
			r.Header.Set("Authorization", "Bearer test-token")
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			a.createRun(w, r)
			if err := a.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), test.errorPath) {
				t.Fatalf("status=%d, body=%s; want 400 and %q", w.Code, w.Body.String(), test.errorPath)
			}
			if credentialsCalled.Load() || runnerCalled.Load() || len(a.runSlots) != 0 || len(store.runs) != 0 {
				t.Fatal("invalid tolerations caused credential lookup, execution, or admission")
			}
		})
	}
}

func TestGetRunReturnsStoredTolerationsForEveryStatus(t *testing.T) {
	for _, status := range []RunStatus{RunStatusRunning, RunStatusSucceeded, RunStatusFailed} {
		for _, custom := range []bool{false, true} {
			name := string(status) + "/empty"
			var snapshot []corev1.Toleration
			if custom {
				name = string(status) + "/custom"
				snapshot = testTolerations()
			}
			t.Run(name, func(t *testing.T) {
				store := NewRunStore()
				request := CreateRunRequest{Target: Target{K8sCluster: "local", Namespace: "dbs", Instance: "postgres-1"}}
				// A deliberately different request proves status uses the saved snapshot.
				request.Tolerations = []corev1.Toleration{{Key: "original"}}
				if _, err := store.Create("run-1", request, coordinator.Resources{}, nil, snapshot); err != nil {
					t.Fatal(err)
				}
				if status != RunStatusRunning {
					message := ""
					if status == RunStatusFailed {
						message = "benchmark failed"
					}
					if err := store.Complete("run-1", status, coordinator.Result{}, message); err != nil {
						t.Fatal(err)
					}
				}
				// Current policy forbids custom tolerations; historic records must remain readable.
				a := NewAPI(nil, func(context.Context, string, string, string, string) error { return nil }, nil, store, context.Background(), coordinator.Resources{}, nil)
				mux := http.NewServeMux()
				a.RegisterRoutes(mux)
				for _, authorized := range []bool{false, true} {
					r := httptest.NewRequest(http.MethodGet, "/api/runs/run-1", nil)
					if authorized {
						r.Header.Set("Authorization", "Bearer test-token")
					}
					w := httptest.NewRecorder()
					mux.ServeHTTP(w, r)
					if !authorized {
						if w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), "tolerations") {
							t.Fatalf("unauthorized response leaked placement: %d %s", w.Code, w.Body.String())
						}
						continue
					}
					if w.Code != http.StatusOK {
						t.Fatalf("status=%d, body=%s", w.Code, w.Body.String())
					}
					var fields map[string]json.RawMessage
					if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
						t.Fatal(err)
					}
					if !custom {
						if string(fields["tolerations"]) != "[]" {
							t.Fatalf("empty tolerations must be [], got %s", fields["tolerations"])
						}
					} else {
						var got []corev1.Toleration
						if err := json.Unmarshal(fields["tolerations"], &got); err != nil || !reflect.DeepEqual(got, snapshot) {
							t.Fatalf("stored snapshot not returned: %+v, %v", got, err)
						}
					}
				}
			})
		}
	}
}
