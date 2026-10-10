package coordinator

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// This limits continuously observed scheduler rejection, not benchmark runtime
// or image downloading. The overall execution timeout remains the final bound.
const schedulingGracePeriod = time.Minute

// SchedulingError contains the scheduler's placement diagnostic, not runner
// logs or connection details. The API may expose it instead of a generic error.
type SchedulingError struct {
	PodName     string
	Message     string
	GracePeriod time.Duration
}

func (e *SchedulingError) Error() string {
	if e.GracePeriod > 0 {
		return fmt.Sprintf("benchmark runner Pod %q remained unschedulable for %s (Unschedulable): %s",
			e.PodName, e.GracePeriod, e.Message)
	}
	return fmt.Sprintf("benchmark runner Pod %q is unschedulable (Unschedulable): %s", e.PodName, e.Message)
}

// schedulingTracker is local to one wait loop. Use observation time rather
// than the server's transition timestamp, which may predate our observation.
type schedulingTracker struct {
	podUID types.UID
	since  time.Time
}

func (s *schedulingTracker) observe(pod *corev1.Pod, now time.Time) error {
	condition := unschedulableCondition(pod)
	if condition == nil || pod.UID == "" {
		*s = schedulingTracker{}
		return nil
	}
	if s.since.IsZero() || s.podUID != pod.UID {
		s.podUID, s.since = pod.UID, now
	}
	if now.Sub(s.since) < schedulingGracePeriod {
		return nil
	}
	return &SchedulingError{PodName: pod.Name, Message: condition.Message, GracePeriod: schedulingGracePeriod}
}

func unschedulableCondition(pod *corev1.Pod) *corev1.PodCondition {
	// Ignore stale conditions on already-bound or terminating Pods.
	if pod == nil || pod.Spec.NodeName != "" || pod.DeletionTimestamp != nil {
		return nil
	}
	for i := range pod.Status.Conditions {
		condition := &pod.Status.Conditions[i]
		if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse &&
			condition.Reason == corev1.PodReasonUnschedulable {
			return condition
		}
	}
	return nil
}

func runnerHasStarted(pod *corev1.Pod) bool {
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == runnerContainerName {
			return status.State.Running != nil || status.State.Terminated != nil ||
				status.LastTerminationState.Terminated != nil
		}
	}
	return false
}
