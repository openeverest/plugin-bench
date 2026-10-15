package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCoordinatorConfigAllowedTolerationKeysFromEnv(t *testing.T) {
	t.Setenv("BENCHMARK_EXECUTION_TIMEOUT", "5m")
	t.Setenv("BENCHMARK_SCHEDULING_GRACE_PERIOD", "0")
	t.Setenv("WORKLOAD_NAMESPACE", "workloads")
	t.Setenv("RUNNER_IMAGE", "runner:dev")
	t.Setenv("RUNNER_IMAGE_PULL_POLICY", "IfNotPresent")
	t.Setenv("RUNNER_SERVICE_ACCOUNT_NAME", "runner")
	for _, key := range []string{"RUNNER_CPU_REQUEST", "RUNNER_CPU_LIMIT", "RUNNER_MEMORY_REQUEST", "RUNNER_MEMORY_LIMIT"} {
		t.Setenv(key, "")
	}
	for _, test := range []struct {
		name, value                 string
		want                        []string
		parseError, validationError bool
	}{
		{name: "unset"},
		{name: "whitespace", value: "   "},
		{name: "empty array", value: "[]", want: []string{}},
		{name: "valid", value: `["dedicated","example.com/benchmark"]`, want: []string{"dedicated", "example.com/benchmark"}},
		{name: "malformed", value: `["dedicated"`, parseError: true},
		{name: "csv", value: "dedicated,other", parseError: true},
		{name: "string", value: `"dedicated"`, parseError: true},
		{name: "object", value: `{}`, parseError: true},
		{name: "null", value: `null`, parseError: true},
		{name: "number", value: `1`, parseError: true},
		{name: "numeric entry", value: `[1]`, parseError: true},
		{name: "boolean entry", value: `[true]`, parseError: true},
		{name: "trailing input", value: `[] []`, parseError: true},
		{name: "null entry", value: `[null]`, validationError: true},
		{name: "empty key", value: `[""]`, validationError: true},
		{name: "wildcard", value: `["*"]`, validationError: true},
		{name: "invalid key", value: `["bad key"]`, validationError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("BENCHMARK_ALLOWED_TOLERATION_KEYS", test.value)
			config, err := coordinatorConfigFromEnv()
			if test.parseError {
				if err == nil || !strings.Contains(err.Error(), "BENCHMARK_ALLOWED_TOLERATION_KEYS") {
					t.Fatalf("parse error = %v; want named environment error", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := config.Validate(); test.validationError {
				if err == nil || !strings.Contains(err.Error(), "allowedTolerationKeys") {
					t.Fatalf("validation = %v; want invalid key error", err)
				}
			} else if err != nil || !reflect.DeepEqual(config.AllowedTolerationKeys, test.want) {
				t.Fatalf("keys = %v, validation = %v; want %v", config.AllowedTolerationKeys, err, test.want)
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
		{"default", "", 0, false},
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
