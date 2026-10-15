package coordinator

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	ktesting "k8s.io/client-go/testing"
)

type diagnosticCore struct {
	typedcorev1.CoreV1Interface
	check func(context.Context)
}

func (c diagnosticCore) Pods(namespace string) typedcorev1.PodInterface {
	return diagnosticPods{PodInterface: c.CoreV1Interface.Pods(namespace), check: c.check}
}

type diagnosticPods struct {
	typedcorev1.PodInterface
	check func(context.Context)
}

func (p diagnosticPods) List(ctx context.Context, options metav1.ListOptions) (*corev1.PodList, error) {
	p.check(ctx)
	return p.PodInterface.List(ctx, options)
}

type diagnosticClient struct {
	*fake.Clientset
	check func(context.Context)
}

func TestRunRejectsNodeAffinityBeforeAPIWrites(t *testing.T) {
	client := fake.NewSimpleClientset()
	c, err := New(validConfig(), client)
	if err != nil {
		t.Fatal(err)
	}
	options := DefaultOptions()
	options.NodeAffinity = &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{}}
	result, err := c.Run(context.Background(), validConnection(), options)
	if err == nil || !strings.Contains(err.Error(), "nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms") {
		t.Fatalf("want required term validation error, got %v", err)
	}
	if result.JobName != "" || len(client.Actions()) != 0 {
		t.Fatal("invalid affinity created resources or started execution")
	}
}

func TestRunRejectsTolerationsBeforeAPIWrites(t *testing.T) {
	for _, keys := range [][]string{nil, {"dedicated"}} {
		config := validConfig()
		config.AllowedTolerationKeys = keys
		client := fake.NewSimpleClientset()
		c, err := New(config, client)
		if err != nil {
			t.Fatal(err)
		}
		options := DefaultOptions()
		options.Tolerations = []corev1.Toleration{{Key: "production"}}
		result, err := c.Run(context.Background(), validConnection(), options)
		if err == nil || !strings.Contains(err.Error(), "tolerations[0].key") {
			t.Fatalf("want policy rejection, got %v", err)
		}
		if result.JobName != "" || len(client.Actions()) != 0 {
			t.Fatal("forbidden tolerations created resources or started execution")
		}
	}
}

func TestRunAppliesPlacementAndCleansUp(t *testing.T) {
	client := fake.NewSimpleClientset()
	config := validConfig()
	config.AllowedTolerationKeys = []string{"dedicated"}
	config.Resources = Resources{CPURequest: "100m", CPULimit: "1", MemoryRequest: "128Mi", MemoryLimit: "512Mi"}
	input := validNodeAffinity()
	want := input.DeepCopy()
	var jobName string
	client.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		action.(ktesting.CreateAction).GetObject().(*corev1.Secret).UID = "secret-uid"
		return false, nil, nil
	})
	client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		job := action.(ktesting.CreateAction).GetObject().(*batchv1.Job)
		if job.Spec.Template.Spec.Affinity == nil || !reflect.DeepEqual(job.Spec.Template.Spec.Affinity.NodeAffinity, want) {
			t.Fatal("Run did not pass node affinity into the created Job")
		}
		if !reflect.DeepEqual(job.Spec.Template.Spec.Tolerations, validTolerations()) {
			t.Fatal("Run did not pass tolerations into the created Job")
		}
		resources := job.Spec.Template.Spec.Containers[0].Resources
		if resources.Requests.Cpu().String() != "500m" || resources.Limits.Cpu().String() != "1" ||
			resources.Requests.Memory().String() != "128Mi" || resources.Limits.Memory().String() != "1Gi" {
			t.Fatalf("Run did not apply resource overrides and defaults alongside affinity: %+v", resources)
		}
		job.UID = "job-uid"
		jobName = job.Name
		job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
		return false, nil, nil
	})
	client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.PodList{Items: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{
			Name: "runner-pod", Labels: map[string]string{batchv1.JobNameLabel: jobName},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: jobName, UID: "job-uid", Controller: boolPtr(true)}},
		}}}}, nil
	})
	c, err := New(config, client)
	if err != nil {
		t.Fatal(err)
	}
	options := DefaultOptions()
	options.NodeAffinity = input
	options.Tolerations = validTolerations()
	options.Resources = Resources{CPURequest: "500m", MemoryLimit: "1Gi"}
	result, err := c.Run(context.Background(), validConnection(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.JobName != jobName || result.Output != "fake logs" {
		t.Fatalf("unexpected run result: %+v", result)
	}
	if !reflect.DeepEqual(input, want) {
		t.Fatal("Run modified submitted affinity")
	}
	if !reflect.DeepEqual(options.Tolerations, validTolerations()) {
		t.Fatal("Run modified submitted tolerations")
	}
	jobDeleted, secretDeleted := false, false
	for _, action := range client.Actions() {
		jobDeleted = jobDeleted || action.Matches("delete", "jobs")
		secretDeleted = secretDeleted || action.Matches("delete", "secrets")
	}
	if !jobDeleted || !secretDeleted {
		t.Fatal("completed run did not clean up its Job and Secret")
	}
}

func TestRunRejectsInvalidResourceOverridesBeforeKubernetesWrites(t *testing.T) {
	config := validConfig()
	config.Resources = Resources{CPURequest: "100m", CPULimit: "1"}
	client := fake.NewSimpleClientset()
	c, err := New(config, client)
	if err != nil {
		t.Fatal(err)
	}
	options := DefaultOptions()
	options.Resources = Resources{CPURequest: "2"}
	_, err = c.Run(context.Background(), validConnection(), options)
	if err == nil || !strings.Contains(err.Error(), "cpu request cannot exceed limit") {
		t.Fatalf("Run() error = %v; want resource validation error", err)
	}
	if len(client.Actions()) != 0 {
		t.Fatalf("invalid resources caused Kubernetes API calls: %v", client.Actions())
	}
}

func (c diagnosticClient) CoreV1() typedcorev1.CoreV1Interface {
	return diagnosticCore{CoreV1Interface: c.Clientset.CoreV1(), check: c.check}
}

func TestRunCollectsLogsBeforeCleanupOnFailure(t *testing.T) {
	for _, scenario := range []string{"failed job", "cancelled", "deadline", "missing pod", "execution timeout", "caller deadline"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			config := validConfig()
			// Exercise monitoring alongside the final diagnostic lookup.
			config.SchedulingGracePeriod = time.Minute
			if scenario == "execution timeout" || scenario == "caller deadline" {
				config.ExecutionTimeout = time.Second
			}
			var callerDeadline time.Time
			if scenario == "caller deadline" {
				callerDeadline = time.Now().Add(100 * time.Millisecond)
				var cancelDeadline context.CancelFunc
				ctx, cancelDeadline = context.WithDeadline(ctx, callerDeadline)
				defer cancelDeadline()
			}
			client := fake.NewSimpleClientset()
			var jobName string
			client.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
				action.(ktesting.CreateAction).GetObject().(*corev1.Secret).UID = "secret-uid"
				return false, nil, nil
			})
			client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
				job := action.(ktesting.CreateAction).GetObject().(*batchv1.Job)
				job.UID = "job-uid"
				jobName = job.Name
				return false, nil, nil
			})
			var originalErr error
			jobDeleted := false
			client.PrependReactor("delete", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
				jobDeleted = true
				return false, nil, nil
			})
			client.PrependReactor("get", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
				if jobDeleted {
					return false, nil, nil
				}
				if scenario == "execution timeout" || scenario == "caller deadline" {
					originalErr = context.DeadlineExceeded
					// Leave the Job pending so Run must stop itself via its context.
					return true, &batchv1.Job{ObjectMeta: metav1.ObjectMeta{UID: "job-uid"}}, nil
				}
				if scenario == "cancelled" {
					cancel()
					originalErr = context.Canceled
				} else if scenario == "deadline" {
					originalErr = context.DeadlineExceeded
				}
				if originalErr != nil {
					return true, nil, originalErr
				}
				return true, &batchv1.Job{
					ObjectMeta: metav1.ObjectMeta{Name: action.(ktesting.GetAction).GetName(), UID: "job-uid"},
					Status:     batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}},
				}, nil
			})
			client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
				if scenario == "missing pod" {
					return true, &corev1.PodList{}, nil
				}
				return true, &corev1.PodList{Items: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{batchv1.JobNameLabel: jobName},
					Name:   "runner-pod", OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: jobName, UID: "job-uid", Controller: boolPtr(true)}},
				}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
					Name: runnerContainerName, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}},
				}}}}}}, nil
			})
			diagnosticLookups := 0
			c, err := New(config, diagnosticClient{Clientset: client, check: func(lookupCtx context.Context) {
				// Monitoring also lists Pods; only the diagnostic lookup gets a fresh failureLogTimeout budget.
				deadline, ok := lookupCtx.Deadline()
				remaining := time.Until(deadline)
				if lookupCtx.Err() == nil && ok && remaining > failureLogTimeout-time.Second && remaining <= failureLogTimeout {
					diagnosticLookups++
				}
			}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := c.Run(ctx, validConnection(), DefaultOptions())
			if scenario == "execution timeout" && ctx.Err() != nil {
				t.Fatal("execution timeout cancelled the caller context")
			}
			if scenario == "caller deadline" && time.Since(callerDeadline) > 500*time.Millisecond {
				t.Fatal("Run did not honor the earlier caller deadline")
			}
			if err == nil || diagnosticLookups != 1 {
				t.Fatalf("error = %v, diagnostic lookups = %d, want 1", err, diagnosticLookups)
			}
			if originalErr != nil && !errors.Is(err, originalErr) {
				t.Fatalf("lost original error: %v", err)
			}
			if originalErr == nil && !strings.Contains(err.Error(), "failed") {
				t.Fatalf("lost Job failure: %v", err)
			}
			if scenario == "missing pod" {
				if !strings.Contains(err.Error(), "no Pod found") {
					t.Fatalf("lost discovery error: %v", err)
				}
			} else if result.Output != "fake logs" {
				t.Fatalf("output = %q", result.Output)
			}
			logged, deletedJob, deletedSecret := false, false, false
			for _, action := range client.Actions() {
				if action.GetSubresource() == "log" {
					logged = true
				}
				if action.Matches("delete", "jobs") {
					deletedJob = true
					if scenario != "missing pod" && !logged {
						t.Fatal("Job deleted before collecting logs")
					}
				}
				if action.Matches("delete", "secrets") {
					deletedSecret = true
				}
			}
			if !deletedJob || !deletedSecret {
				t.Fatal("resources were not cleaned up")
			}
		})
	}
}
