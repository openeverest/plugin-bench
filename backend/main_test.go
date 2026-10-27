package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
	corev1 "k8s.io/api/core/v1"
)

func TestHandleStatusReportsGenericReadiness(t *testing.T) {
	t.Setenv("BENCHMARK_DRIVERS", `{"pgbench":{"enabled":true,"image":"runner:test"}}`)
	registry, err := benchmarkRegistryFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handleStatus(registry)(w, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected status response: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["status"] != "ready" || response["runnerReady"] != true || response["storage"] != "ephemeral" ||
		response["message"] != "Benchmark runs execute as Kubernetes Jobs." {
		t.Fatal("status response lost backend readiness metadata")
	}
	if _, exists := response["database"]; exists {
		t.Fatal("generic backend readiness must not advertise a single database type")
	}
}

func TestHandleStatusReportsOnlyEnabledDrivers(t *testing.T) {
	for _, test := range []struct {
		name, payload string
		want          []string
	}{
		{
			name: "both enabled",
			payload: `{"sysbench":{"enabled":true,"image":"private.example/sysbench:test"},` +
				`"pgbench":{"enabled":true,"image":"private.example/pgbench:test"}}`,
			want: []string{"pgbench", "sysbench"},
		},
		{
			name: "sysbench disabled",
			payload: `{"pgbench":{"enabled":true,"image":"private.example/pgbench:test"},` +
				`"sysbench":{"enabled":false}}`,
			want: []string{"pgbench"},
		},
		{
			name: "pgbench disabled",
			payload: `{"sysbench":{"enabled":true,"image":"private.example/sysbench:test"},` +
				`"pgbench":{"enabled":false}}`,
			want: []string{"sysbench"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("BENCHMARK_DRIVERS", test.payload)
			registry, err := benchmarkRegistryFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/status", handleStatus(registry))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/status", nil))
			var response struct {
				Drivers []string `json:"drivers"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusOK || !reflect.DeepEqual(response.Drivers, test.want) {
				t.Fatalf("status response = %d %s; want drivers %v", w.Code, w.Body.String(), test.want)
			}
			if strings.Contains(w.Body.String(), "private.example") {
				t.Fatal("status response exposed configured runner image references")
			}
		})
	}
}

func TestBenchmarkRegistryUsesExistingImageConfiguration(t *testing.T) {
	t.Setenv("BENCHMARK_DRIVERS", "")
	if err := os.Unsetenv("BENCHMARK_DRIVERS"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RUNNER_IMAGE", "example.com/custom-pgbench:version")
	t.Setenv("RUNNER_IMAGE_PULL_POLICY", "")
	registry, err := benchmarkRegistryFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	registration, err := registry.Resolve("pgbench", driver.TargetInfo{DatabaseType: "postgresql", Provider: "new-provider"})
	if err != nil || registration.Runner.Image != "example.com/custom-pgbench:version" || registration.Runner.ImagePullPolicy != "IfNotPresent" {
		t.Fatal("registration did not use existing image configuration")
	}
	if _, err := registry.Resolve("sysbench", driver.TargetInfo{DatabaseType: "mysql"}); err == nil {
		t.Fatal("legacy pgbench image configuration implicitly enabled sysbench")
	}
	for _, test := range []struct{ image, policy string }{{"", "IfNotPresent"}, {"runner:test", "invalid"}} {
		t.Setenv("RUNNER_IMAGE", test.image)
		t.Setenv("RUNNER_IMAGE_PULL_POLICY", test.policy)
		if _, err := benchmarkRegistryFromEnv(); err == nil {
			t.Fatal("accepted invalid runner registration")
		}
	}
}

func TestCoordinatorConfigRunnerPlacementFromEnv(t *testing.T) {
	t.Setenv("RUNNER_NODE_SELECTOR", `{"pool":"benchmark"}`)
	t.Setenv("RUNNER_TOLERATIONS", `[{"key":"dedicated","operator":"Equal","value":"benchmark","effect":"NoSchedule"}]`)
	config, err := coordinatorConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	wantTolerations := []corev1.Toleration{
		{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "benchmark", Effect: corev1.TaintEffectNoSchedule},
	}
	if !reflect.DeepEqual(config.NodeSelector, map[string]string{"pool": "benchmark"}) ||
		!reflect.DeepEqual(config.Tolerations, wantTolerations) {
		t.Fatalf("placement = %v / %v", config.NodeSelector, config.Tolerations)
	}

	// Helm renders empty values as "{}" and "[]".
	t.Setenv("RUNNER_NODE_SELECTOR", "{}")
	t.Setenv("RUNNER_TOLERATIONS", "[]")
	config, err = coordinatorConfigFromEnv()
	if err != nil || len(config.NodeSelector) != 0 || len(config.Tolerations) != 0 {
		t.Fatalf("empty placement = %v / %v, error = %v", config.NodeSelector, config.Tolerations, err)
	}
}

func TestCoordinatorConfigRejectsMalformedRunnerPlacement(t *testing.T) {
	for _, test := range []struct{ key, value string }{
		{"RUNNER_NODE_SELECTOR", `["pool"]`},
		{"RUNNER_NODE_SELECTOR", `{"pool":"a"} {}`},
		{"RUNNER_TOLERATIONS", `[{"key":"dedicated","efect":"NoSchedule"}]`},
	} {
		t.Run(test.value, func(t *testing.T) {
			t.Setenv(test.key, test.value)
			if _, err := coordinatorConfigFromEnv(); err == nil || !strings.Contains(err.Error(), test.key) {
				t.Fatalf("want %s parsing error, got %v", test.key, err)
			}
		})
	}
}

func TestCoordinatorConfigSchedulingGracePeriodFromEnv(t *testing.T) {
	t.Setenv("BENCHMARK_EXECUTION_TIMEOUT", "5m")
	for _, test := range []struct {
		name, value string
		want        time.Duration
		wantError   bool
	}{
		{"default", "", 2 * time.Minute, false},
		{"disabled", "0", 0, false},
		{"custom", "90s", 90 * time.Second, false},
		{"malformed", "invalid", 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("BENCHMARK_SCHEDULING_GRACE_PERIOD", test.value)
			config, err := coordinatorConfigFromEnv()
			if test.wantError {
				if err == nil || !strings.Contains(err.Error(), "BENCHMARK_SCHEDULING_GRACE_PERIOD") {
					t.Fatalf("want grace period parsing error, got %v", err)
				}
				return
			}
			if err != nil || config.SchedulingGracePeriod != test.want || config.ExecutionTimeout != 5*time.Minute {
				t.Fatalf("config = %+v, error = %v; want grace %s and execution timeout 5m", config, err, test.want)
			}
		})
	}
}
