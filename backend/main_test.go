package main

import (
	"strings"
	"testing"
	"time"
)

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
