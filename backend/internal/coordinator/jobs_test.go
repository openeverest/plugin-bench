package coordinator

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestNewBenchmarkJobBuildsRunnerManifest(t *testing.T) {
	labels := map[string]string{"plugin-bench/run": "run-1"}
	options := DefaultOptions()
	options.Clients = 2
	options.Threads = 2

	config := validConfig()
	config.Resources = Resources{CPURequest: "100m", CPULimit: "1", MemoryRequest: "64Mi", MemoryLimit: "128Mi"}
	job, err := newBenchmarkJob(config, "pgbench-run-1", "pgbench-secret-1", options, labels)
	if err != nil {
		t.Fatalf("newBenchmarkJob() error = %v", err)
	}
	if job.Namespace != "plugin-bench-workloads" || job.Name != "pgbench-run-1" {
		t.Fatalf("job identity = %s/%s", job.Namespace, job.Name)
	}
	if job.Spec.Template.Spec.ServiceAccountName != "plugin-bench-runner" {
		t.Fatalf("service account = %q", job.Spec.Template.Spec.ServiceAccountName)
	}
	if job.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatalf("restart policy = %q", job.Spec.Template.Spec.RestartPolicy)
	}
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 {
		t.Fatal("Job must disable retries")
	}
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != 60 {
		t.Fatal("Job must use the configured execution deadline")
	}
	if token := job.Spec.Template.Spec.AutomountServiceAccountToken; token == nil || *token {
		t.Fatal("runner must disable ServiceAccount token mounting")
	}
	if len(job.Spec.Template.Spec.Containers) != 1 {
		t.Fatal("Job must contain exactly one runner container")
	}
	container := job.Spec.Template.Spec.Containers[0]
	if container.Image != validConfig().RunnerImage {
		t.Fatalf("runner image = %q", container.Image)
	}
	if len(container.Env) != 10 {
		t.Fatalf("environment variable count = %d, want 10", len(container.Env))
	}
	env := make(map[string]corev1.EnvVar)
	for _, variable := range container.Env {
		if _, exists := env[variable.Name]; exists {
			t.Fatalf("duplicate environment variable %s", variable.Name)
		}
		env[variable.Name] = variable
	}
	for _, name := range []string{"PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD"} {
		variable := env[name]
		if variable.Value != "" || variable.ValueFrom == nil || variable.ValueFrom.SecretKeyRef == nil {
			t.Fatalf("%s must use a Secret reference without a literal value", name)
		}
		ref := variable.ValueFrom.SecretKeyRef
		if ref.Name != "pgbench-secret-1" || ref.Key != name || (ref.Optional != nil && *ref.Optional) {
			t.Fatalf("%s has an incorrect or optional Secret reference", name)
		}
	}
	for name, want := range map[string]string{
		"BENCH_DURATION": "60", "BENCH_CLIENTS": "2", "BENCH_THREADS": "2",
		"BENCH_SCALE": "1", "BENCH_INITIALIZE": "false",
	} {
		if variable, ok := env[name]; !ok || variable.Value != want || variable.ValueFrom != nil {
			t.Fatalf("incorrect benchmark environment variable %s", name)
		}
	}
	for _, check := range []struct {
		values corev1.ResourceList
		name   corev1.ResourceName
		want   string
	}{
		{container.Resources.Requests, corev1.ResourceCPU, "100m"},
		{container.Resources.Limits, corev1.ResourceCPU, "1"},
		{container.Resources.Requests, corev1.ResourceMemory, "64Mi"},
		{container.Resources.Limits, corev1.ResourceMemory, "128Mi"},
	} {
		got, ok := check.values[check.name]
		if !ok || got.Cmp(resource.MustParse(check.want)) != 0 {
			t.Fatalf("incorrect %s resource value, want %s", check.name, check.want)
		}
	}
	if job.Labels["plugin-bench/run"] != "run-1" || job.Spec.Template.Labels["plugin-bench/run"] != "run-1" {
		t.Fatal("run labels were not copied to Job and Pod")
	}
}

func TestResourceRequirementsValidation(t *testing.T) {
	for _, test := range []struct {
		name      string
		resources Resources
		wantError bool
	}{
		{"empty", Resources{}, false},
		{"zero", Resources{CPURequest: "0", MemoryLimit: "0"}, false},
		{"request only", Resources{CPURequest: "100m"}, false},
		{"limit only", Resources{MemoryLimit: "1Gi"}, false},
		{"equal across units", Resources{CPURequest: "1000m", CPULimit: "1", MemoryRequest: "1024Mi", MemoryLimit: "1Gi"}, false},
		{"negative cpu request", Resources{CPURequest: "-1"}, true},
		{"negative cpu limit", Resources{CPULimit: "-100m"}, true},
		{"negative memory request", Resources{MemoryRequest: "-1Mi"}, true},
		{"negative memory limit", Resources{MemoryLimit: "-1Gi"}, true},
		{"cpu request above limit", Resources{CPURequest: "2", CPULimit: "1"}, true},
		{"memory request above limit", Resources{MemoryRequest: "1Gi", MemoryLimit: "512Mi"}, true},
		{"sub milli request", Resources{CPURequest: "0.1m"}, true},
		{"sub milli limit", Resources{CPULimit: "1.0001"}, true},
		{"milli boundary", Resources{CPURequest: "0.001"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := resourceRequirements(test.resources)
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v, wantError = %v", err, test.wantError)
			}
		})
	}
}

func TestNewBenchmarkJobRejectsInvalidResourceQuantity(t *testing.T) {
	config := validConfig()
	config.Resources.CPURequest = "not-a-quantity"

	if _, err := newBenchmarkJob(config, "pgbench-run-1", "pgbench-secret-1", DefaultOptions(), nil); err == nil {
		t.Fatal("newBenchmarkJob() error = nil, want invalid resource quantity error")
	}
}
