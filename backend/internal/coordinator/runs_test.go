package coordinator

import (
	"context"
	"errors"
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

func (c diagnosticClient) CoreV1() typedcorev1.CoreV1Interface {
	return diagnosticCore{CoreV1Interface: c.Clientset.CoreV1(), check: c.check}
}

func TestRunCollectsLogsBeforeCleanupOnFailure(t *testing.T) {
	for _, scenario := range []string{"failed job", "cancelled", "deadline", "missing pod", "execution timeout", "caller deadline"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			config := validConfig()
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
				}}}}, nil
			})
			checked := false
			c, err := New(config, diagnosticClient{Clientset: client, check: func(logCtx context.Context) {
				checked = true
				deadline, ok := logCtx.Deadline()
				if logCtx.Err() != nil || !ok || time.Until(deadline) <= 0 || time.Until(deadline) > failureLogTimeout {
					t.Fatal("diagnostics need an active, bounded context")
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
			if err == nil || !checked {
				t.Fatalf("error = %v, diagnostics attempted = %v", err, checked)
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
