package main

import (
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

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
