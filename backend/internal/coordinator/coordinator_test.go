package coordinator

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"
)

func validConfig() Config {
	return Config{
		WorkloadNamespace:    "plugin-bench-workloads",
		RunnerImage:          "ghcr.io/openeverest/plugin-bench-pgbench:dev",
		ImagePullPolicy:      "IfNotPresent",
		RunnerServiceAccount: "plugin-bench-runner",
		ExecutionTimeout:     time.Minute,
	}
}

func TestNewInjectsKubernetesClient(t *testing.T) {
	client := fake.NewSimpleClientset()

	coordinator, err := New(validConfig(), client)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if coordinator == nil {
		t.Fatal("New() returned a nil coordinator")
	}
	if coordinator.kubeClient != client {
		t.Fatal("New() did not retain the injected Kubernetes client")
	}
}

func TestNewRequiresKubernetesClient(t *testing.T) {
	coordinator, err := New(validConfig(), nil)
	if coordinator != nil {
		t.Fatal("New() returned a coordinator without a Kubernetes client")
	}
	if !errors.Is(err, ErrKubernetesClientRequired) {
		t.Fatalf("New() error = %v, want ErrKubernetesClientRequired", err)
	}
}

func TestConfigAllowedTolerationKeys(t *testing.T) {
	for _, test := range []struct {
		name      string
		keys      []string
		wantError bool
	}{
		{"unset", nil, false},
		{"empty", []string{}, false},
		{"valid", []string{"dedicated", "example.com/benchmark"}, false},
		{"duplicates", []string{"dedicated", "dedicated"}, false},
		{"empty key", []string{""}, true},
		{"whitespace", []string{" dedicated "}, true},
		{"wildcard", []string{"*"}, true},
		{"invalid prefix", []string{"EXAMPLE.COM/benchmark"}, true},
		{"invalid key", []string{"invalid key"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig()
			config.AllowedTolerationKeys = test.keys
			client := fake.NewSimpleClientset()
			c, err := New(config, client)
			if test.wantError {
				if err == nil || c != nil || !strings.Contains(err.Error(), "allowedTolerationKeys") {
					t.Fatalf("New() = %v, %v; want allowlist error", c, err)
				}
			} else if err != nil || c == nil {
				t.Fatalf("New() = %v, %v; want valid config", c, err)
			}
			if _, err := PrepareTolerations(nil, test.keys); (err != nil) != test.wantError {
				t.Fatalf("preparation allowlist validation disagrees with config: %v", err)
			}
			if len(client.Actions()) != 0 {
				t.Fatal("allowlist validation made Kubernetes API calls")
			}
		})
	}
}

func TestNewCopiesAllowedTolerationKeys(t *testing.T) {
	config := validConfig()
	config.AllowedTolerationKeys = []string{"dedicated"}
	c, err := New(config, fake.NewSimpleClientset())
	if err != nil {
		t.Fatal(err)
	}
	config.AllowedTolerationKeys[0] = "production"
	if !reflect.DeepEqual(c.config.AllowedTolerationKeys, []string{"dedicated"}) {
		t.Fatal("coordinator shares caller's allowlist")
	}
	c.config.AllowedTolerationKeys[0] = "other"
	if config.AllowedTolerationKeys[0] != "production" {
		t.Fatal("coordinator mutation changed caller's allowlist")
	}
}

func TestConfigSchedulingGracePeriod(t *testing.T) {
	for _, test := range []struct {
		name      string
		period    time.Duration
		wantError bool
	}{
		{"disabled", 0, false},
		{"custom", 90 * time.Second, false},
		{"negative", -time.Second, true},
		{"fractional", 1500 * time.Millisecond, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig()
			config.SchedulingGracePeriod = test.period
			err := config.Validate()
			if (err != nil) != test.wantError {
				t.Fatalf("Validate() = %v, want error %t", err, test.wantError)
			}
		})
	}
}

func TestNewValidatesConfigBeforeClient(t *testing.T) {
	invalid := validConfig()
	invalid.WorkloadNamespace = ""

	coordinator, err := New(invalid, nil)
	if coordinator != nil {
		t.Fatal("New() returned a coordinator for invalid configuration")
	}
	if err == nil || err.Error() != "workload namespace is required" {
		t.Fatalf("New() error = %v, want workload namespace validation error", err)
	}
}

func TestNewValidatesRunnerResources(t *testing.T) {
	for _, test := range []struct {
		name      string
		resources Resources
		wantError bool
	}{
		{"unspecified", Resources{}, false},
		{"partial", Resources{CPURequest: "100m", MemoryLimit: "512Mi"}, false},
		{"valid", Resources{CPURequest: "100m", CPULimit: "1", MemoryRequest: "128Mi", MemoryLimit: "512Mi"}, false},
		{"malformed cpu", Resources{CPURequest: "invalid"}, true},
		{"malformed memory", Resources{MemoryLimit: "invalid"}, true},
		{"negative memory", Resources{MemoryRequest: "-1Mi"}, true},
		{"cpu request exceeds limit", Resources{CPURequest: "2", CPULimit: "1"}, true},
		{"memory request exceeds limit", Resources{MemoryRequest: "1Gi", MemoryLimit: "512Mi"}, true},
		{"unsupported cpu precision", Resources{CPURequest: "0.0001"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig()
			config.Resources = test.resources
			client := fake.NewSimpleClientset()
			c, err := New(config, client)
			if test.wantError {
				if c != nil || err == nil || !strings.Contains(err.Error(), "invalid runner resources") {
					t.Fatalf("New() = %v, %v; want resource validation error and no coordinator", c, err)
				}
			} else if err != nil || c == nil {
				t.Fatalf("New() = %v, %v; want a coordinator", c, err)
			}
			if len(client.Actions()) != 0 {
				t.Fatal("configuration validation made Kubernetes API calls")
			}
		})
	}
}
