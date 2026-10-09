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

func bodyWithNodeAffinity(value string) string {
	if value == "" {
		return validCreateRunBody
	}
	return strings.TrimSuffix(validCreateRunBody, "}") + `,"nodeAffinity":` + value + "}"
}

func TestCreateRunPreparesNodeAffinityAndKeepsStoredSnapshot(t *testing.T) {
	combined := testNodeAffinity()
	for name, input := range map[string]*corev1.NodeAffinity{
		"omitted":   nil,
		"null":      nil,
		"empty":     {},
		"required":  {RequiredDuringSchedulingIgnoredDuringExecution: combined.RequiredDuringSchedulingIgnoredDuringExecution},
		"preferred": {PreferredDuringSchedulingIgnoredDuringExecution: combined.PreferredDuringSchedulingIgnoredDuringExecution},
		"combined":  combined,
	} {
		t.Run(name, func(t *testing.T) {
			value, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			body := bodyWithNodeAffinity(string(value))
			if name == "omitted" {
				body = validCreateRunBody
			}
			wantOptions := input.DeepCopy()
			wantSnapshot := corev1.NodeAffinity{}
			if name == "empty" {
				wantOptions = nil
			} else if input != nil {
				wantSnapshot = *input.DeepCopy()
			}
			calls := make(chan coordinator.Options, 1)
			release := make(chan struct{})
			store := NewRunStore()
			a := NewAPI(func(context.Context, string, string, string, string) (*everest.Credentials, error) {
				return testCredentials(), nil
			}, nil, testRunnerFunc(func(_ context.Context, _ coordinator.Connection, options coordinator.Options) (coordinator.Result, error) {
				calls <- options
				<-release
				return coordinator.Result{}, nil
			}), store, context.Background())
			t.Cleanup(func() {
				close(release)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := a.Shutdown(ctx); err != nil {
					t.Error(err)
				}
			})
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
			var options coordinator.Options
			select {
			case options = <-calls:
			case <-time.After(time.Second):
				t.Fatal("runner was not called")
			}
			if !reflect.DeepEqual(options.NodeAffinity, wantOptions) || options.Duration != 30 || options.Clients != 1 || options.Threads != 1 || options.Scale != 1 || options.Initialize {
				t.Fatalf("unexpected prepared runner options: %+v", options)
			}
			if name == "combined" {
				// A runner may mutate its own options. That must not alter history.
				mutateTestNodeAffinity(options.NodeAffinity)
			}
			run, err := store.Get(accepted["id"])
			if err != nil || run.Status != RunStatusRunning || !reflect.DeepEqual(run.NodeAffinity, wantSnapshot) || !reflect.DeepEqual(run.Request.NodeAffinity, input) {
				t.Fatalf("stored snapshots changed: run=%+v err=%v", run, err)
			}
		})
	}
}

func TestCreateRunRejectsInvalidNodeAffinityBeforeAdmission(t *testing.T) {
	oversized := testNodeAffinity()
	values := make([]string, maxCreateRunRequestBytes)
	for i := range values {
		values[i] = "benchmark"
	}
	oversized.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions[0].Values = values
	oversizedJSON, err := json.Marshal(oversized)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, value, errorPath string
		status                 int
	}{
		{"no required terms", `{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[]}}`, "nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms", http.StatusBadRequest},
		{"empty required term", `{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{}]}}`, "nodeSelectorTerms[0]", http.StatusBadRequest},
		{"invalid operator", `{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{"matchExpressions":[{"key":"workload","operator":"Equals","values":["benchmark"]}]}]}}`, "matchExpressions[0].operator", http.StatusBadRequest},
		{"missing values", `{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{"matchExpressions":[{"key":"workload","operator":"In"}]}]}}`, "matchExpressions[0].values", http.StatusBadRequest},
		{"invalid field", `{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{"matchFields":[{"key":"metadata.namespace","operator":"In","values":["default"]}]}]}}`, "matchFields[0].key", http.StatusBadRequest},
		{"invalid weight", `{"preferredDuringSchedulingIgnoredDuringExecution":[{"weight":101,"preference":{"matchExpressions":[{"key":"disk-type","operator":"Exists"}]}}]}`, "preferredDuringSchedulingIgnoredDuringExecution[0].weight", http.StatusBadRequest},
		{"empty preference", `{"preferredDuringSchedulingIgnoredDuringExecution":[{"weight":1,"preference":{}}]}`, "preferredDuringSchedulingIgnoredDuringExecution[0].preference", http.StatusBadRequest},
		{"unknown affinity field", `{"nodeSelector":{"workload":"benchmark"}}`, "", http.StatusBadRequest},
		{"unknown nested field", `{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{"matchExpressions":[{"key":"workload","operator":"Exists","extra":true}]}]}}`, "", http.StatusBadRequest},
		{"wrong affinity type", `[]`, "", http.StatusBadRequest},
		{"wrong selector type", `{"requiredDuringSchedulingIgnoredDuringExecution":true}`, "", http.StatusBadRequest},
		{"wrong values type", `{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{"matchExpressions":[{"key":"workload","operator":"In","values":"benchmark"}]}]}}`, "", http.StatusBadRequest},
		{"fractional weight", `{"preferredDuringSchedulingIgnoredDuringExecution":[{"weight":1.5,"preference":{}}]}`, "", http.StatusBadRequest},
		{"trailing JSON", `{} } {"extra":true`, "", http.StatusBadRequest},
		{"oversized affinity", string(oversizedJSON), "", http.StatusRequestEntityTooLarge},
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
			}), store, context.Background())
			r := httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(bodyWithNodeAffinity(test.value)))
			r.Header.Set("Authorization", "Bearer test-token")
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			a.createRun(w, r)
			if err := a.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
			if w.Code != test.status || !strings.Contains(w.Body.String(), test.errorPath) {
				t.Fatalf("status=%d, body=%s; want %d and path %q", w.Code, w.Body.String(), test.status, test.errorPath)
			}
			if credentialsCalled.Load() || runnerCalled.Load() || len(a.runSlots) != 0 || len(store.runs) != 0 {
				t.Fatal("invalid affinity caused credential lookup, execution, or run admission")
			}
		})
	}
}

func TestGetRunReturnsStoredNodeAffinityForEveryStatus(t *testing.T) {
	for _, status := range []RunStatus{RunStatusRunning, RunStatusSucceeded, RunStatusFailed} {
		for name, input := range map[string]*corev1.NodeAffinity{"none": nil, "custom": testNodeAffinity()} {
			t.Run(string(status)+"/"+name, func(t *testing.T) {
				store := NewRunStore()
				request := CreateRunRequest{Target: Target{K8sCluster: "local", Namespace: "dbs", Instance: "postgres-1"}, NodeAffinity: input.DeepCopy()}
				if request.NodeAffinity != nil {
					// Status must use the saved prepared snapshot, not recompute it
					// from the separately retained original request.
					request.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].Weight = 5
				}
				_, err := store.Create("run-1", request, input)
				if err != nil {
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
				a := NewAPI(nil, func(context.Context, string, string, string, string) error { return nil }, nil, store, context.Background())
				mux := http.NewServeMux()
				a.RegisterRoutes(mux)
				r := httptest.NewRequest(http.MethodGet, "/api/runs/run-1", nil)
				r.Header.Set("Authorization", "Bearer test-token")
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				if w.Code != http.StatusOK {
					t.Fatalf("status=%d, body=%s", w.Code, w.Body.String())
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
					t.Fatal(err)
				}
				if input == nil {
					if string(fields["nodeAffinity"]) != "{}" {
						t.Fatalf("unconstrained status must return {}, got %s", fields["nodeAffinity"])
					}
				} else {
					var got corev1.NodeAffinity
					if err := json.Unmarshal(fields["nodeAffinity"], &got); err != nil || !reflect.DeepEqual(&got, input) {
						t.Fatalf("stored affinity not returned: got=%+v err=%v", got, err)
					}
				}
			})
		}
	}
}
