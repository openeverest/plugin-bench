package coordinator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func schedulingTestPod(ref JobRef) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "runner-pod", Namespace: validConfig().WorkloadNamespace, UID: "pod-uid",
			Labels: map[string]string{batchv1.JobNameLabel: ref.Name},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job",
				Name: ref.Name, UID: ref.UID, Controller: boolPtr(true)}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
			Reason: corev1.PodReasonUnschedulable, Message: "0/2 nodes match the required node affinity",
		}}},
	}
}

func TestRunnerHasStarted(t *testing.T) {
	for _, test := range []struct {
		name   string
		status corev1.ContainerStatus
		want   bool
	}{
		{"running", corev1.ContainerStatus{Name: runnerContainerName, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}, true},
		{"terminated", corev1.ContainerStatus{Name: runnerContainerName, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}}, true},
		{"restarting", corev1.ContainerStatus{Name: runnerContainerName, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}}, true},
		{"waiting only", corev1.ContainerStatus{Name: runnerContainerName, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}}, false},
		{"another container", corev1.ContainerStatus{Name: "other", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}, false},
		{"no container status", corev1.ContainerStatus{}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			pod := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{test.status}}}
			if got := runnerHasStarted(pod); got != test.want {
				t.Fatalf("runnerHasStarted = %t, want %t", got, test.want)
			}
		})
	}
}

func TestSchedulingTrackerGraceAndResets(t *testing.T) {
	start := time.Unix(100, 0)
	for _, test := range []struct {
		name   string
		mutate func(*corev1.Pod) *corev1.Pod
	}{
		{"missing pod", func(*corev1.Pod) *corev1.Pod { return nil }},
		{"missing UID", func(p *corev1.Pod) *corev1.Pod { p.UID = ""; return p }},
		{"replacement pod", func(p *corev1.Pod) *corev1.Pod { p.UID = "replacement"; return p }},
		{"no condition", func(p *corev1.Pod) *corev1.Pod { p.Status.Conditions = nil; return p }},
		{"unknown condition", func(p *corev1.Pod) *corev1.Pod { p.Status.Conditions[0].Status = corev1.ConditionUnknown; return p }},
		{"scheduled condition", func(p *corev1.Pod) *corev1.Pod { p.Status.Conditions[0].Status = corev1.ConditionTrue; return p }},
		{"different reason", func(p *corev1.Pod) *corev1.Pod { p.Status.Conditions[0].Reason = "SchedulingGated"; return p }},
		{"bound with stale condition", func(p *corev1.Pod) *corev1.Pod { p.Spec.NodeName = "node"; return p }},
		{"terminating pod", func(p *corev1.Pod) *corev1.Pod { now := metav1.NewTime(start); p.DeletionTimestamp = &now; return p }},
	} {
		t.Run(test.name, func(t *testing.T) {
			pod := schedulingTestPod(JobRef{Name: "job", UID: "job-uid"})
			var tracker schedulingTracker
			if err := tracker.observe(pod, start); err != nil {
				t.Fatal(err)
			}
			if err := tracker.observe(test.mutate(pod.DeepCopy()), start.Add(50*time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := tracker.observe(pod, start.Add(60*time.Second)); err != nil {
				t.Fatalf("timer was not reset: %v", err)
			}
		})
	}

	var tracker schedulingTracker
	pod := schedulingTestPod(JobRef{Name: "job", UID: "job-uid"})
	// Server timestamps must not make the grace period expire immediately.
	pod.Status.Conditions[0].LastTransitionTime = metav1.NewTime(start.Add(-time.Hour))
	if err := tracker.observe(pod, start); err != nil {
		t.Fatal(err)
	}
	if err := tracker.observe(pod, start.Add(schedulingGracePeriod-time.Nanosecond)); err != nil {
		t.Fatalf("failed before the grace boundary: %v", err)
	}
	pod.Status.Conditions[0].Message = "latest scheduling diagnosis"
	err := tracker.observe(pod, start.Add(schedulingGracePeriod))
	var schedulingErr *SchedulingError
	if !errors.As(err, &schedulingErr) || schedulingErr.Message != pod.Status.Conditions[0].Message {
		t.Fatalf("want scheduling failure with latest message, got %v", err)
	}
}

// synctest advances Go's clock and timers without a real one-minute wait.
func TestWaitForJobScheduling(t *testing.T) {
	for _, test := range []struct {
		name     string
		wantTime time.Duration
		want     string
	}{
		{"persistent", 60 * time.Second, "scheduling"},
		{"delayed pod creation", 80 * time.Second, "scheduling"},
		{"replaced pod", 100 * time.Second, "scheduling"},
		{"missing condition gap", 105 * time.Second, "scheduling"},
		{"API failure gap", 105 * time.Second, "scheduling"},
		{"temporary then scheduled", 90 * time.Second, "success"},
		{"image pull pending", 90 * time.Second, "deadline"},
		{"foreign owner", 90 * time.Second, "deadline"},
		{"multiple owned pods", 90 * time.Second, "deadline"},
		{"missing pod", 90 * time.Second, "deadline"},
		{"terminal job wins", 60 * time.Second, "success"},
		{"failed job wins", 30 * time.Second, "job failure"},
		{"cancellation", 20 * time.Second, "cancelled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				ref := JobRef{Name: "job", UID: "job-uid"}
				deadline := 120 * time.Second
				if test.want == "deadline" {
					deadline = test.wantTime
				}
				ctx, cancel := context.WithTimeout(context.Background(), deadline)
				defer cancel()
				client := fake.NewSimpleClientset()
				client.PrependReactor("get", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
					job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: ref.Name, UID: ref.UID}}
					elapsed := time.Since(start)
					if test.want == "success" && elapsed >= test.wantTime {
						job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
					}
					if test.want == "job failure" && elapsed >= test.wantTime {
						job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
					}
					if test.want == "cancelled" && elapsed >= test.wantTime {
						cancel()
					}
					return true, job, nil
				})
				client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
					elapsed := time.Since(start)
					pod := schedulingTestPod(ref)
					pod.Status.Conditions[0].Message = fmt.Sprintf("latest diagnosis at %s", elapsed)
					switch test.name {
					case "delayed pod creation":
						if elapsed < 20*time.Second {
							return true, &corev1.PodList{}, nil
						}
					case "replaced pod":
						if elapsed >= 40*time.Second {
							pod.UID = "replacement"
						}
					case "missing condition gap":
						if elapsed >= 40*time.Second && elapsed < 45*time.Second {
							pod.Status.Conditions = nil
						}
					case "API failure gap":
						if elapsed >= 40*time.Second && elapsed < 45*time.Second {
							return true, nil, errors.New("temporary API failure")
						}
					case "temporary then scheduled", "image pull pending":
						if test.name == "image pull pending" || elapsed >= 30*time.Second {
							pod.Spec.NodeName = "node"
							pod.Status.Conditions[0].Status = corev1.ConditionTrue
							pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: runnerContainerName,
								State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}}}
						}
					case "foreign owner":
						pod.OwnerReferences[0].UID = "another-job"
					case "missing pod":
						return true, &corev1.PodList{}, nil
					case "multiple owned pods":
						other := pod.DeepCopy()
						other.Name, other.UID = "other", "other-uid"
						return true, &corev1.PodList{Items: []corev1.Pod{*pod, *other}}, nil
					}
					return true, &corev1.PodList{Items: []corev1.Pod{*pod}}, nil
				})
				c, err := New(validConfig(), client)
				if err != nil {
					t.Fatal(err)
				}
				err = c.waitForJob(ctx, ref)
				var schedulingErr *SchedulingError
				switch test.want {
				case "scheduling":
					if !errors.As(err, &schedulingErr) || schedulingErr.Message != fmt.Sprintf("latest diagnosis at %s", test.wantTime) {
						t.Fatalf("want latest scheduling diagnostic, got %v", err)
					}
				case "success":
					if err != nil {
						t.Fatal(err)
					}
				case "deadline":
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("want deadline, got %v", err)
					}
				case "cancelled":
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("want cancellation, got %v", err)
					}
				case "job failure":
					if err == nil || !strings.Contains(err.Error(), "failed") {
						t.Fatalf("want Job failure, got %v", err)
					}
				}
				if elapsed := time.Since(start); elapsed != test.wantTime {
					t.Fatalf("elapsed = %s, want %s", elapsed, test.wantTime)
				}
			})
		})
	}
}

func TestRunSchedulingFailureCleansUpWithoutLogs(t *testing.T) {
	for _, scenario := range []string{"early failure", "overall timeout", "diagnostic lookup failure"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client := fake.NewSimpleClientset()
				config := validConfig()
				config.ExecutionTimeout = 5 * time.Minute
				if scenario != "early failure" {
					config.ExecutionTimeout = 30 * time.Second
				}
				var ref JobRef
				start := time.Now()
				client.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
					action.(ktesting.CreateAction).GetObject().(*corev1.Secret).UID = "secret-uid"
					return false, nil, nil
				})
				client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
					job := action.(ktesting.CreateAction).GetObject().(*batchv1.Job)
					job.UID = "job-uid"
					ref = JobRef{Name: job.Name, UID: job.UID}
					return false, nil, nil
				})
				client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
					if scenario == "diagnostic lookup failure" && time.Since(start) >= config.ExecutionTimeout {
						return true, nil, errors.New("diagnostic API unavailable")
					}
					return true, &corev1.PodList{Items: []corev1.Pod{*schedulingTestPod(ref)}}, nil
				})
				c, err := New(config, client)
				if err != nil {
					t.Fatal(err)
				}
				result, err := c.Run(context.Background(), validConnection(), DefaultOptions())
				var schedulingErr *SchedulingError
				if scenario == "diagnostic lookup failure" {
					if !strings.Contains(err.Error(), "diagnostic API unavailable") {
						t.Fatalf("lost diagnostic error: %v", err)
					}
				} else if !errors.As(err, &schedulingErr) || !strings.Contains(err.Error(), "required node affinity") {
					t.Fatalf("want scheduler explanation, got %v", err)
				}
				if scenario != "early failure" && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("lost original timeout: %v", err)
				}
				if result.JobName != ref.Name || result.Output != "" {
					t.Fatalf("unexpected result: %+v", result)
				}
				wantTime := config.ExecutionTimeout
				if scenario == "early failure" {
					wantTime = schedulingGracePeriod
				}
				if elapsed := time.Since(start); elapsed != wantTime {
					t.Fatalf("elapsed = %s, want %s", elapsed, wantTime)
				}
				jobDeleted, secretDeleted := false, false
				for _, action := range client.Actions() {
					if action.GetSubresource() == "log" {
						t.Fatal("requested logs for a runner that never started")
					}
					jobDeleted = jobDeleted || action.Matches("delete", "jobs")
					secretDeleted = secretDeleted || action.Matches("delete", "secrets")
				}
				if !jobDeleted || !secretDeleted {
					t.Fatal("failed run did not clean up both resources")
				}
			})
		})
	}
}
