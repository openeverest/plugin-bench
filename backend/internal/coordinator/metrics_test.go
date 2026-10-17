package coordinator

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/openeverest/plugin-bench/backend/internal/runnerresult"
	corev1 "k8s.io/api/core/v1"
)

func runnerMetricsFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "runner-result-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRunnerMetricsUsesCurrentRunnerTermination(t *testing.T) {
	message := runnerMetricsFixture(t)
	want := []runnerresult.Metric{{Name: "tps", Unit: "tx/s", Value: 1234.5}, {Name: "latency_avg", Unit: "ms", Value: 3.2}}
	terminated := corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: message}}
	runner := corev1.ContainerStatus{Name: runnerContainerName, State: terminated}
	sidecar := corev1.ContainerStatus{Name: "sidecar", State: corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{Message: `{"version":1,"metrics":[{"name":"wrong","unit":"count","value":999}]}`},
	}}
	for _, test := range []struct {
		name     string
		statuses []corev1.ContainerStatus
		want     []runnerresult.Metric
	}{
		{"no statuses", nil, nil},
		{"only sidecar", []corev1.ContainerStatus{sidecar}, nil},
		{"terminated runner", []corev1.ContainerStatus{runner}, want},
		{"sidecar first", []corev1.ContainerStatus{sidecar, runner}, want},
		{"failed runner", []corev1.ContainerStatus{{Name: runnerContainerName, State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{Message: message, ExitCode: 1},
		}}}, want},
		{"waiting with old metrics", []corev1.ContainerStatus{{Name: runnerContainerName,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{}}, LastTerminationState: terminated}}, nil},
		{"running with old metrics", []corev1.ContainerStatus{{Name: runnerContainerName,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}, LastTerminationState: terminated}}, nil},
		{"only old metrics", []corev1.ContainerStatus{{Name: runnerContainerName, LastTerminationState: terminated}}, nil},
		{"empty current message", []corev1.ContainerStatus{{Name: runnerContainerName,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}, LastTerminationState: terminated}}, nil},
		{"invalid current message", []corev1.ContainerStatus{{Name: runnerContainerName,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: "credential-sentinel"}}, LastTerminationState: terminated}}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			pod := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: test.statuses, Message: message}}
			if got := runnerMetrics(pod); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("runnerMetrics() = %+v; want %+v", got, test.want)
			}
		})
	}
	if got := runnerMetrics(nil); got != nil {
		t.Fatal("nil Pod returned metrics")
	}
}
