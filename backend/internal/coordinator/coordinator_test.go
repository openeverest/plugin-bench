package coordinator

import (
	"errors"
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
