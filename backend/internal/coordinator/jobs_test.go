package coordinator

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

type failingLogStream struct {
	err    error
	closed bool
}

func (s *failingLogStream) Read([]byte) (int, error) {
	return 0, s.err
}

func (s *failingLogStream) Close() error {
	s.closed = true
	return nil
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
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > resourceCleanupTimeout {
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

func TestWaitForJobReturnsWhenComplete(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "job-1", Namespace: "plugin-bench-workloads", UID: "job-uid"},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
			Type:   batchv1.JobComplete,
			Status: corev1.ConditionTrue,
		}}},
	}
	client := fake.NewSimpleClientset(job)
	coordinator, err := New(validConfig(), client)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := coordinator.waitForJobAtInterval(context.Background(), JobRef{Name: job.Name, UID: job.UID}, time.Millisecond); err != nil {
		t.Fatalf("waitForJob() error = %v", err)
	}
}

func TestWaitForJobReturnsFailure(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "job-1", Namespace: "plugin-bench-workloads", UID: "job-uid"},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
			Type:   batchv1.JobFailed,
			Status: corev1.ConditionTrue,
		}}},
	}
	client := fake.NewSimpleClientset(job)
	coordinator, err := New(validConfig(), client)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = coordinator.waitForJobAtInterval(context.Background(), JobRef{Name: job.Name, UID: job.UID}, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), `benchmark Job "job-1" failed`) {
		t.Fatalf("error = %v, want Job failure", err)
	}
}

func TestWaitForJobPollsUntilTerminalState(t *testing.T) {
	client := fake.NewSimpleClientset()
	getCount := 0
	client.PrependReactor("get", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		getCount++
		job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "job-1", Namespace: "plugin-bench-workloads", UID: "job-uid"}}
		if getCount == 1 {
			job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionFalse}}
		}
		if getCount == 2 {
			job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
		}
		return true, job, nil
	})
	coordinator, err := New(validConfig(), client)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := coordinator.waitForJobAtInterval(context.Background(), JobRef{Name: "job-1", UID: "job-uid"}, time.Millisecond); err != nil {
		t.Fatalf("waitForJob() error = %v", err)
	}
	if getCount != 2 {
		t.Fatalf("Job was fetched %d times, want 2", getCount)
	}
}

func TestWaitForJobPreservesCancellation(t *testing.T) {
	client := fake.NewSimpleClientset()
	ctx, cancel := context.WithCancel(context.Background())
	client.PrependReactor("get", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		cancel()
		return true, &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "job-1", Namespace: "plugin-bench-workloads", UID: "job-uid"}}, nil
	})
	coordinator, err := New(validConfig(), client)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = coordinator.waitForJobAtInterval(ctx, JobRef{Name: "job-1", UID: "job-uid"}, time.Millisecond)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestWaitForJobRejectsUIDMismatch(t *testing.T) {
	client := fake.NewSimpleClientset(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "job-1", Namespace: "plugin-bench-workloads", UID: "different-uid"},
	})
	coordinator, err := New(validConfig(), client)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = coordinator.waitForJobAtInterval(context.Background(), JobRef{Name: "job-1", UID: "created-uid"}, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "UID does not match") {
		t.Fatalf("error = %v, want UID mismatch", err)
	}
}

func TestDeleteBenchmarkJobUsesUIDAndForegroundPropagation(t *testing.T) {
	client := fake.NewSimpleClientset(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "job-1", Namespace: validConfig().WorkloadNamespace, UID: "job-uid"},
	})
	client.PrependReactor("delete", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		deleteAction := action.(ktesting.DeleteAction)
		options := deleteAction.GetDeleteOptions()
		if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != "job-uid" {
			t.Fatal("Job deletion must specify the recorded UID")
		}
		if options.PropagationPolicy == nil || *options.PropagationPolicy != metav1.DeletePropagationForeground {
			t.Fatal("Job deletion must use foreground propagation")
		}
		return false, nil, nil
	})
	coordinator, err := New(validConfig(), client)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := coordinator.deleteBenchmarkJob(context.Background(), JobRef{Name: "job-1", UID: "job-uid"}); err != nil {
		t.Fatalf("deleteBenchmarkJob() error = %v", err)
	}
	if _, err := client.BatchV1().Jobs(validConfig().WorkloadNamespace).Get(context.Background(), "job-1", metav1.GetOptions{}); err == nil {
		t.Fatal("Job still exists after deletion")
	}
}

func TestDeleteBenchmarkJobTreatsMissingJobAsSuccess(t *testing.T) {
	coordinator, err := New(validConfig(), fake.NewSimpleClientset())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := coordinator.deleteBenchmarkJob(context.Background(), JobRef{Name: "job-1", UID: "job-uid"}); err != nil {
		t.Fatalf("missing Job should be treated as cleaned up: %v", err)
	}
}

func TestCleanupBenchmarkResourcesDeletesJobBeforeSecret(t *testing.T) {
	client := fake.NewSimpleClientset(
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "job-1", Namespace: validConfig().WorkloadNamespace, UID: "job-uid"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "secret-1", Namespace: validConfig().WorkloadNamespace, UID: "secret-uid"}},
	)
	var actions []string
	client.PrependReactor("delete", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		actions = append(actions, "job")
		return false, nil, nil
	})
	client.PrependReactor("delete", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		actions = append(actions, "secret")
		return false, nil, nil
	})
	coordinator, err := New(validConfig(), client)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = coordinator.cleanupBenchmarkResources(context.Background(), benchmarkResources{
		job:    JobRef{Name: "job-1", UID: "job-uid"},
		secret: SecretRef{Name: "secret-1", UID: "secret-uid"},
	})
	if err != nil {
		t.Fatalf("cleanupBenchmarkResources() error = %v", err)
	}
	if strings.Join(actions, ",") != "job,secret" {
		t.Fatalf("cleanup order = %v, want [job secret]", actions)
	}
}

func TestCleanupBenchmarkResourcesPreservesDeleteErrors(t *testing.T) {
	client := fake.NewSimpleClientset()
	jobErr, secretErr := errors.New("Job deletion failed"), errors.New("Secret deletion failed")
	client.PrependReactor("delete", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, jobErr
	})
	client.PrependReactor("delete", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, secretErr
	})
	coordinator, err := New(validConfig(), client)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = coordinator.cleanupBenchmarkResources(context.Background(), benchmarkResources{
		job:    JobRef{Name: "job-1", UID: "job-uid"},
		secret: SecretRef{Name: "secret-1", UID: "secret-uid"},
	})
	if !errors.Is(err, jobErr) || !errors.Is(err, secretErr) {
		t.Fatalf("error = %v, want both cleanup errors", err)
	}
}

func TestCleanupWaitsForJobDeletionBeforeSecret(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("delete", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, nil // The API acknowledged deletion, but it is still pending.
	})
	reads := 0
	client.PrependReactor("get", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		reads++
		if reads == 1 {
			return true, &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "job", UID: "job-uid"}}, nil
		}
		return true, nil, apierrors.NewNotFound(batchv1.Resource("jobs"), "job")
	})
	secretDeleted := false
	client.PrependReactor("delete", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		if reads < 2 {
			t.Fatal("Secret deleted before Job deletion was confirmed")
		}
		secretDeleted = true
		return true, nil, nil
	})
	c, err := New(validConfig(), client)
	if err != nil {
		t.Fatal(err)
	}
	err = c.cleanupBenchmarkResources(context.Background(), benchmarkResources{
		job: JobRef{Name: "job", UID: "job-uid"}, secret: SecretRef{Name: "secret", UID: "secret-uid"},
	})
	if err != nil || !secretDeleted {
		t.Fatalf("error = %v, Secret deleted = %v", err, secretDeleted)
	}
}

func TestCleanupJobDeletionTimeoutStillAttemptsSecret(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("delete", "jobs", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, nil })
	client.PrependReactor("get", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &batchv1.Job{ObjectMeta: metav1.ObjectMeta{UID: "job-uid"}}, nil
	})
	secretDeleted := false
	client.PrependReactor("delete", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		secretDeleted = true
		return true, nil, nil
	})
	c, err := New(validConfig(), client)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = c.cleanupBenchmarkResourcesWithContext(ctx, benchmarkResources{
		job: JobRef{Name: "job", UID: "job-uid"}, secret: SecretRef{Name: "secret", UID: "secret-uid"},
	})
	if !errors.Is(err, context.DeadlineExceeded) || !secretDeleted {
		t.Fatalf("error = %v, Secret deleted = %v", err, secretDeleted)
	}
}

func TestFindJobPod(t *testing.T) {
	ref := JobRef{Name: "benchmark", UID: "job-uid"}
	newPod := func(name string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: validConfig().WorkloadNamespace,
			Labels: map[string]string{batchv1.JobNameLabel: ref.Name},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1", Kind: "Job", Name: ref.Name,
				UID: ref.UID, Controller: boolPtr(true),
			}},
		}}
	}
	for _, test := range []struct {
		name      string
		mutate    func(*corev1.Pod)
		wantMatch bool
	}{
		{"matching owner", func(*corev1.Pod) {}, true},
		{"old Job UID", func(p *corev1.Pod) { p.OwnerReferences[0].UID = "old" }, false},
		{"no owner", func(p *corev1.Pod) { p.OwnerReferences = nil }, false},
		{"not controller", func(p *corev1.Pod) { p.OwnerReferences[0].Controller = boolPtr(false) }, false},
		{"wrong kind", func(p *corev1.Pod) { p.OwnerReferences[0].Kind = "ReplicaSet" }, false},
		{"wrong owner name", func(p *corev1.Pod) { p.OwnerReferences[0].Name = "other" }, false},
		{"wrong API", func(p *corev1.Pod) { p.OwnerReferences[0].APIVersion = "apps/v1" }, false},
		{"wrong label", func(p *corev1.Pod) { p.Labels[batchv1.JobNameLabel] = "other" }, false},
		{"wrong namespace", func(p *corev1.Pod) { p.Namespace = "other" }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			pod := newPod("runner")
			test.mutate(pod)
			client := fake.NewSimpleClientset(pod)
			c, err := New(validConfig(), client)
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.findJobPod(context.Background(), ref)
			if test.wantMatch {
				if err != nil || got == nil || got.Name != pod.Name {
					t.Fatalf("Pod = %v, error = %v", got, err)
				}
			} else if err == nil || got != nil || !strings.Contains(err.Error(), "no Pod found") {
				t.Fatalf("expected no matching Pod, got %v, %v", got, err)
			}
		})
	}
	t.Run("multiple matches", func(t *testing.T) {
		c, err := New(validConfig(), fake.NewSimpleClientset(newPod("one"), newPod("two")))
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.findJobPod(context.Background(), ref)
		if got != nil || err == nil || !strings.Contains(err.Error(), "multiple Pods") {
			t.Fatalf("expected ambiguity error, got %v, %v", got, err)
		}
	})
	t.Run("empty list", func(t *testing.T) {
		c, err := New(validConfig(), fake.NewSimpleClientset())
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.findJobPod(context.Background(), ref)
		if got != nil || err == nil || !strings.Contains(err.Error(), "no Pod found") {
			t.Fatalf("expected missing Pod error, got %v, %v", got, err)
		}
	})
}

func TestFindJobPodErrors(t *testing.T) {
	for _, test := range []string{"API failure", "cancellation during list", "already canceled", "expired deadline", "missing UID"} {
		t.Run(test, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ref := JobRef{Name: "benchmark", UID: "job-uid"}
			cause := errors.New("API unavailable")
			switch test {
			case "already canceled":
				cancel()
				cause = context.Canceled
			case "expired deadline":
				var stop context.CancelFunc
				ctx, stop = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer stop()
				cause = context.DeadlineExceeded
			case "missing UID":
				ref.UID = ""
			case "cancellation during list":
				cause = context.Canceled
			}
			client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
				if test == "cancellation during list" {
					cancel()
				}
				return true, nil, cause
			})
			c, err := New(validConfig(), client)
			if err != nil {
				t.Fatal(err)
			}
			pod, err := c.findJobPod(ctx, ref)
			if pod != nil || err == nil {
				t.Fatal("expected error and no Pod")
			}
			if test != "missing UID" && !errors.Is(err, cause) {
				t.Fatalf("error = %v, want %v", err, cause)
			}
			if (test == "already canceled" || test == "expired deadline" || test == "missing UID") && len(client.Actions()) != 0 {
				t.Fatal("invalid input or expired context must prevent API calls")
			}
		})
	}
}

func TestCollectRunnerLogs(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "runner-pod"}}
	var requestedName string
	var requestedContainer string

	output, truncated, err := collectRunnerLogsWithReader(context.Background(), pod, func(_ context.Context, name string, options *corev1.PodLogOptions) (io.ReadCloser, error) {
		requestedName = name
		requestedContainer = options.Container
		return io.NopCloser(strings.NewReader("transaction output\n")), nil
	})
	if err != nil || truncated || output != "transaction output\n" {
		t.Fatalf("output = %q, truncated = %v, error = %v", output, truncated, err)
	}
	if requestedName != pod.Name || requestedContainer != runnerContainerName {
		t.Fatalf("requested Pod/container = %q/%q", requestedName, requestedContainer)
	}
}

func TestCollectRunnerLogsUsesKubernetesClient(t *testing.T) {
	coordinator, err := New(validConfig(), fake.NewSimpleClientset())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	output, truncated, err := coordinator.collectRunnerLogs(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "runner-pod"}})
	if err != nil || truncated || output != "fake logs" {
		t.Fatalf("output = %q, truncated = %v, error = %v", output, truncated, err)
	}
}

func TestCollectRunnerLogsTruncatesOutput(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "runner-pod"}}
	largeOutput := strings.Repeat("x", int(maxRunnerLogBytes)+1)

	output, truncated, err := collectRunnerLogsWithReader(context.Background(), pod, func(context.Context, string, *corev1.PodLogOptions) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(largeOutput)), nil
	})
	if err != nil {
		t.Fatalf("collectRunnerLogsWithReader() error = %v", err)
	}
	if !truncated || len(output) != int(maxRunnerLogBytes) || output != largeOutput[:maxRunnerLogBytes] {
		t.Fatalf("output length = %d, truncated = %v", len(output), truncated)
	}
}

func TestCollectRunnerLogsClosesStreamAfterReadError(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "runner-pod"}}
	stream := &failingLogStream{err: errors.New("log stream failed")}
	output, truncated, err := collectRunnerLogsWithReader(context.Background(), pod, func(context.Context, string, *corev1.PodLogOptions) (io.ReadCloser, error) {
		return stream, nil
	})
	if err == nil || !strings.Contains(err.Error(), "log stream failed") || output != "" || truncated || !stream.closed {
		t.Fatalf("output = %q, truncated = %v, closed = %v, error = %v", output, truncated, stream.closed, err)
	}
}

func TestCollectRunnerLogsPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	output, truncated, err := collectRunnerLogsWithReader(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "runner-pod"}}, func(context.Context, string, *corev1.PodLogOptions) (io.ReadCloser, error) {
		called = true
		return nil, nil
	})
	if !errors.Is(err, context.Canceled) || output != "" || truncated || called {
		t.Fatalf("output = %q, truncated = %v, called = %v, error = %v", output, truncated, called, err)
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
