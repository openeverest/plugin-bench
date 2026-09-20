package coordinator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	ktesting "k8s.io/client-go/testing"
)

// The fake client does not expose request contexts through its reactors.
type cleanupContextClient struct {
	kubernetes.Interface
	check func(context.Context)
}

func (c cleanupContextClient) CoreV1() typedcorev1.CoreV1Interface {
	return cleanupContextCore{CoreV1Interface: c.Interface.CoreV1(), check: c.check}
}

type cleanupContextCore struct {
	typedcorev1.CoreV1Interface
	check func(context.Context)
}

func (c cleanupContextCore) Secrets(namespace string) typedcorev1.SecretInterface {
	return cleanupContextSecrets{SecretInterface: c.CoreV1Interface.Secrets(namespace), check: c.check}
}

type cleanupContextSecrets struct {
	typedcorev1.SecretInterface
	check func(context.Context)
}

func (s cleanupContextSecrets) Delete(ctx context.Context, name string, options metav1.DeleteOptions) error {
	s.check(ctx)
	return s.SecretInterface.Delete(ctx, name, options)
}

func TestResourceCleanupSurvivesCancellationWithDeadline(t *testing.T) {
	client := fake.NewSimpleClientset()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		action.(ktesting.CreateAction).GetObject().(*corev1.Secret).UID = "secret-uid"
		cancel()
		return false, nil, nil
	})
	called := false
	c, err := New(validConfig(), cleanupContextClient{Interface: client, check: func(cleanupCtx context.Context) {
		called = true
		if cleanupCtx.Err() != nil {
			t.Fatal("cleanup inherited caller cancellation")
		}
		deadline, ok := cleanupCtx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > credentialCleanupTimeout {
			t.Fatal("cleanup must have a bounded future deadline")
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.createBenchmarkResources(ctx, validConnection(), DefaultOptions(), nil)
	if !errors.Is(err, context.Canceled) || !called {
		t.Fatalf("error = %v, cleanup called = %v", err, called)
	}
	for _, action := range client.Actions() {
		if action.Matches("create", "jobs") {
			t.Fatal("created Job after cancellation")
		}
	}
	secrets, err := client.CoreV1().Secrets(validConfig().WorkloadNamespace).List(context.Background(), metav1.ListOptions{})
	if err != nil || len(secrets.Items) != 0 {
		t.Fatal("credential Secret was not cleaned up")
	}
}

func TestResourceCreationRejectsInvalidResourcesBeforeAPIWrite(t *testing.T) {
	for _, resources := range []Resources{
		{CPURequest: "invalid"},
		{MemoryRequest: "-1Mi"},
		{CPURequest: "2", CPULimit: "1"},
		{CPURequest: "0.0001"},
	} {
		client := fake.NewSimpleClientset()
		config := validConfig()
		config.Resources = resources
		c, err := New(config, client)
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.createBenchmarkResources(context.Background(), validConnection(), DefaultOptions(), nil)
		if err == nil || len(client.Actions()) != 0 {
			t.Fatalf("invalid resources must fail before API calls: error=%v actions=%v", err, client.Actions())
		}
	}
}

func TestResourceCreationPreservesJobAndCleanupErrors(t *testing.T) {
	client := fake.NewSimpleClientset()
	jobErr, cleanupErr := errors.New("job failed"), errors.New("cleanup failed")
	client.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		action.(ktesting.CreateAction).GetObject().(*corev1.Secret).UID = "secret-uid"
		return false, nil, nil
	})
	client.PrependReactor("create", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, jobErr
	})
	client.PrependReactor("delete", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, cleanupErr
	})
	c, err := New(validConfig(), client)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.createBenchmarkResources(context.Background(), validConnection(), DefaultOptions(), nil)
	if !errors.Is(err, jobErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("error = %v, want both causes", err)
	}
}

func TestSecretCreationFailurePreventsJobCreation(t *testing.T) {
	client := fake.NewSimpleClientset()
	cause := errors.New("secret creation failed")
	client.PrependReactor("create", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, cause
	})
	c, err := New(validConfig(), client)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.createBenchmarkResources(context.Background(), validConnection(), DefaultOptions(), nil)
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want Secret failure", err)
	}
	if actions := client.Actions(); len(actions) != 1 || !actions[0].Matches("create", "secrets") {
		t.Fatal("Secret creation failure must prevent further resource operations")
	}
}

func TestCreateBenchmarkJob(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		job := action.(ktesting.CreateAction).GetObject().(*batchv1.Job)
		job.UID = types.UID("job-uid-1")
		return false, nil, nil
	})
	coordinator, err := New(validConfig(), client)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	jobRef, err := coordinator.createBenchmarkJob(context.Background(), SecretRef{Name: "plugin-bench-credentials-run-1"}, DefaultOptions(), nil)
	if err != nil {
		t.Fatalf("createBenchmarkJob() error = %v", err)
	}
	if !strings.HasPrefix(jobRef.Name, benchmarkJobNamePrefix) || jobRef.UID == "" {
		t.Fatalf("JobRef = %+v, want generated name and UID", jobRef)
	}
	job, err := client.BatchV1().Jobs("plugin-bench-workloads").Get(context.Background(), jobRef.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	container := job.Spec.Template.Spec.Containers[0]
	for _, variable := range container.Env[:5] {
		if variable.ValueFrom == nil || variable.ValueFrom.SecretKeyRef == nil || variable.ValueFrom.SecretKeyRef.Name != "plugin-bench-credentials-run-1" {
			t.Fatalf("%s does not reference the created Secret", variable.Name)
		}
	}
}

func TestCreateBenchmarkResourcesCreatesSecretBeforeJob(t *testing.T) {
	client := fake.NewSimpleClientset()
	var actions []string
	client.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		actions = append(actions, "secret")
		secret := action.(ktesting.CreateAction).GetObject().(*corev1.Secret)
		secret.UID = types.UID("secret-uid-1")
		return false, nil, nil
	})
	client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		actions = append(actions, "job")
		job := action.(ktesting.CreateAction).GetObject().(*batchv1.Job)
		job.UID = types.UID("job-uid-1")
		return false, nil, nil
	})
	coordinator, err := New(validConfig(), client)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	resources, err := coordinator.createBenchmarkResources(context.Background(), validConnection(), DefaultOptions(), nil)
	if err != nil {
		t.Fatalf("createBenchmarkResources() error = %v", err)
	}
	if !strings.HasPrefix(resources.secret.Name, credentialSecretNamePrefix) || resources.secret.UID == "" {
		t.Fatalf("SecretRef = %+v, want generated name and UID", resources.secret)
	}
	if !strings.HasPrefix(resources.job.Name, benchmarkJobNamePrefix) || resources.job.UID == "" {
		t.Fatalf("JobRef = %+v, want generated name and UID", resources.job)
	}
	if strings.Join(actions, ",") != "secret,job" {
		t.Fatalf("resource creation order = %v, want [secret job]", actions)
	}
	job, err := client.BatchV1().Jobs(validConfig().WorkloadNamespace).Get(context.Background(), resources.job.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get(Job) error = %v", err)
	}
	if got := job.Spec.Template.Spec.Containers[0].Env[0].ValueFrom.SecretKeyRef.Name; got != resources.secret.Name {
		t.Fatalf("Job references Secret %q, want %q", got, resources.secret.Name)
	}
}

func TestCreateBenchmarkResourcesDeletesSecretWhenJobCreationFails(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		secret := action.(ktesting.CreateAction).GetObject().(*corev1.Secret)
		secret.UID = types.UID("secret-uid-1")
		return false, nil, nil
	})
	client.PrependReactor("create", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("Job API unavailable")
	})
	coordinator, err := New(validConfig(), client)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = coordinator.createBenchmarkResources(context.Background(), validConnection(), DefaultOptions(), nil)
	creationErr := err
	if creationErr == nil || !strings.Contains(creationErr.Error(), "Job API unavailable") {
		t.Fatalf("error = %v, want Job API error", creationErr)
	}
	secrets, err := client.CoreV1().Secrets(validConfig().WorkloadNamespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("List(Secret) error = %v", err)
	}
	if len(secrets.Items) != 0 {
		t.Fatalf("created %d Secret(s), want cleanup after Job failure", len(secrets.Items))
	}
	if strings.Contains(creationErr.Error(), validConnection().Password) {
		t.Fatal("resource creation error exposed database credentials")
	}
}

func TestCreateBenchmarkResourcesRejectsInvalidInputBeforeCreatingResources(t *testing.T) {
	client := fake.NewSimpleClientset()
	coordinator, err := New(validConfig(), client)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	connection := validConnection()
	connection.Database = ""

	_, err = coordinator.createBenchmarkResources(context.Background(), connection, DefaultOptions(), nil)
	if err == nil || !strings.Contains(err.Error(), "database name is required") {
		t.Fatalf("error = %v, want validation error", err)
	}
	secrets, err := client.CoreV1().Secrets(validConfig().WorkloadNamespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("List(Secret) error = %v", err)
	}
	jobs, err := client.BatchV1().Jobs(validConfig().WorkloadNamespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("List(Job) error = %v", err)
	}
	if len(secrets.Items) != 0 || len(jobs.Items) != 0 {
		t.Fatalf("invalid input created resources: %d Secret(s), %d Job(s)", len(secrets.Items), len(jobs.Items))
	}
}

func TestCreateBenchmarkJobRejectsCanceledContext(t *testing.T) {
	client := fake.NewSimpleClientset()
	coordinator, err := New(validConfig(), client)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = coordinator.createBenchmarkJob(ctx, SecretRef{Name: "credentials-run-1"}, DefaultOptions(), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestCreateBenchmarkJobDoesNotExposeCredentialsOnAPIError(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("Kubernetes API unavailable")
	})
	coordinator, err := New(validConfig(), client)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = coordinator.createBenchmarkJob(context.Background(), SecretRef{Name: "credentials-run-1"}, DefaultOptions(), nil)
	if err == nil || !strings.Contains(err.Error(), "Kubernetes API unavailable") {
		t.Fatalf("error = %v, want API error", err)
	}
	if strings.Contains(err.Error(), "sensitive-test-password") {
		t.Fatal("API error exposed database credentials")
	}
}

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
