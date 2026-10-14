package coordinator

import (
	"strings"
	"testing"
)

func TestResolveResourcesInheritance(t *testing.T) {
	defaults := Resources{CPURequest: "100m", CPULimit: "1", MemoryRequest: "128Mi", MemoryLimit: "512Mi"}
	for _, test := range []struct {
		name      string
		defaults  Resources
		overrides Resources
		want      Resources
	}{
		{"no overrides", defaults, Resources{}, defaults},
		{"cpu request", defaults, Resources{CPURequest: "250m"}, Resources{CPURequest: "250m", CPULimit: "1", MemoryRequest: "128Mi", MemoryLimit: "512Mi"}},
		{"cpu limit", defaults, Resources{CPULimit: "2"}, Resources{CPURequest: "100m", CPULimit: "2", MemoryRequest: "128Mi", MemoryLimit: "512Mi"}},
		{"memory request", defaults, Resources{MemoryRequest: "256Mi"}, Resources{CPURequest: "100m", CPULimit: "1", MemoryRequest: "256Mi", MemoryLimit: "512Mi"}},
		{"memory limit", defaults, Resources{MemoryLimit: "1Gi"}, Resources{CPURequest: "100m", CPULimit: "1", MemoryRequest: "128Mi", MemoryLimit: "1Gi"}},
		{"whitespace inherits", defaults, Resources{CPURequest: " \t", CPULimit: "\n", MemoryRequest: " ", MemoryLimit: "\t"}, defaults},
		{"trim defaults and overrides", Resources{CPURequest: " 100m ", CPULimit: " 1 ", MemoryRequest: " 128Mi ", MemoryLimit: " 512Mi "}, Resources{CPURequest: " 250m ", MemoryLimit: " 1Gi "}, Resources{CPURequest: "250m", CPULimit: "1", MemoryRequest: "128Mi", MemoryLimit: "1Gi"}},
		{"unspecified defaults", Resources{}, Resources{}, Resources{}},
		{"partial unspecified defaults", Resources{}, Resources{CPURequest: "100m"}, Resources{CPURequest: "100m"}},
		{"inherited zero remains valid", Resources{CPURequest: "0", MemoryLimit: "0"}, Resources{}, Resources{CPURequest: "0", MemoryLimit: "0"}},
		{"equal across units", defaults, Resources{CPURequest: "1000m", MemoryRequest: "1024Mi", MemoryLimit: "1Gi"}, Resources{CPURequest: "1000m", CPULimit: "1", MemoryRequest: "1024Mi", MemoryLimit: "1Gi"}},
		{"smallest cpu increment", Resources{}, Resources{CPURequest: "0.001"}, Resources{CPURequest: "0.001"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			beforeDefaults, beforeOverrides := test.defaults, test.overrides
			got, err := ResolveResources(test.defaults, test.overrides)
			if err != nil || got != test.want {
				t.Fatalf("ResolveResources() = %+v, %v; want %+v", got, err, test.want)
			}
			if test.defaults != beforeDefaults || test.overrides != beforeOverrides {
				t.Fatal("resolution mutated inputs")
			}
		})
	}
}

func TestResolveResourcesRejectsInvalidValues(t *testing.T) {
	defaults := Resources{CPURequest: "100m", CPULimit: "1", MemoryRequest: "128Mi", MemoryLimit: "512Mi"}
	for _, test := range []struct {
		name      string
		defaults  Resources
		overrides Resources
		wantError string
	}{
		{"malformed cpu request", defaults, Resources{CPURequest: "invalid"}, "request cpu"},
		{"malformed cpu limit", defaults, Resources{CPULimit: "invalid"}, "limit cpu"},
		{"malformed memory request", defaults, Resources{MemoryRequest: "invalid"}, "request memory"},
		{"malformed memory limit", defaults, Resources{MemoryLimit: "invalid"}, "limit memory"},
		{"negative cpu request", defaults, Resources{CPURequest: "-1m"}, "request cpu"},
		{"negative cpu limit", defaults, Resources{CPULimit: "-1"}, "limit cpu"},
		{"negative memory request", defaults, Resources{MemoryRequest: "-1Mi"}, "request memory"},
		{"negative memory limit", defaults, Resources{MemoryLimit: "-1Gi"}, "limit memory"},
		{"zero cpu request", defaults, Resources{CPURequest: "0m"}, "request cpu resource override must be positive"},
		{"zero cpu limit", defaults, Resources{CPULimit: "0"}, "limit cpu resource override must be positive"},
		{"zero memory request", defaults, Resources{MemoryRequest: "0Mi"}, "request memory resource override must be positive"},
		{"zero memory limit", defaults, Resources{MemoryLimit: "0Gi"}, "limit memory resource override must be positive"},
		{"sub milli cpu request", defaults, Resources{CPURequest: "0.1m"}, "increments of 1m"},
		{"sub milli cpu limit", defaults, Resources{CPULimit: "1.0001"}, "increments of 1m"},
		{"cpu request exceeds inherited limit", defaults, Resources{CPURequest: "1001m"}, "cpu request cannot exceed limit"},
		{"cpu limit below inherited request", defaults, Resources{CPULimit: "99m"}, "cpu request cannot exceed limit"},
		{"memory request exceeds inherited limit", defaults, Resources{MemoryRequest: "1Gi"}, "memory request cannot exceed limit"},
		{"memory limit below inherited request", defaults, Resources{MemoryLimit: "64Mi"}, "memory request cannot exceed limit"},
		{"explicit cpu pair invalid", defaults, Resources{CPURequest: "2", CPULimit: "1000m"}, "cpu request cannot exceed limit"},
		{"explicit memory pair invalid", defaults, Resources{MemoryRequest: "1025Mi", MemoryLimit: "1Gi"}, "memory request cannot exceed limit"},
		{"invalid inherited quantity", Resources{MemoryLimit: "invalid"}, Resources{}, "limit memory"},
		{"invalid inherited pair", Resources{CPURequest: "2", CPULimit: "1"}, Resources{}, "cpu request cannot exceed limit"},
		{"negative inherited value", Resources{MemoryRequest: "-1Mi"}, Resources{}, "request memory"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveResources(test.defaults, test.overrides)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("ResolveResources() error = %v; want %q", err, test.wantError)
			}
			if got != (Resources{}) {
				t.Fatalf("failed resolution returned resources: %+v", got)
			}
		})
	}
}

func TestResolveResourcesKeepsRunsIndependent(t *testing.T) {
	defaults := Resources{CPURequest: "100m", CPULimit: "1", MemoryRequest: "128Mi", MemoryLimit: "512Mi"}
	first, err := ResolveResources(defaults, Resources{MemoryLimit: "1Gi"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := ResolveResources(defaults, Resources{CPURequest: "500m"})
	if err != nil {
		t.Fatal(err)
	}
	first.CPURequest = "2"
	if defaults.CPURequest != "100m" || defaults.MemoryLimit != "512Mi" || second.CPURequest != "500m" || second.MemoryLimit != "512Mi" {
		t.Fatalf("per-run resources leaked into defaults or another run: defaults=%+v second=%+v", defaults, second)
	}
}
