package coordinator

import (
	"github.com/openeverest/plugin-bench/backend/internal/runnerresult"
	corev1 "k8s.io/api/core/v1"
)

// runnerMetrics reads only the current runner container's termination record.
// Missing or invalid metrics are optional diagnostics, not execution failures.
// Never log the message: malformed metadata may contain credentials.
func runnerMetrics(pod *corev1.Pod) []runnerresult.Metric {
	if pod == nil {
		return nil
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name != runnerContainerName {
			continue
		}
		if status.State.Terminated == nil {
			return nil
		}
		metrics, err := runnerresult.Decode(status.State.Terminated.Message)
		if err != nil {
			return nil
		}
		return metrics
	}
	return nil
}
