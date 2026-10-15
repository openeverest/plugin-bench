package coordinator

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestConfigValidatesRunnerPlacement(t *testing.T) {
	seconds := int64(30)
	for _, test := range []struct {
		name         string
		nodeSelector map[string]string
		tolerations  []corev1.Toleration
		wantError    bool
	}{
		{name: "empty"},
		{name: "dedicated pool",
			nodeSelector: map[string]string{"node-role.example.com/benchmark": "true"},
			tolerations: []corev1.Toleration{
				{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "benchmark", Effect: corev1.TaintEffectNoSchedule},
				{Operator: corev1.TolerationOpExists},
				{Key: "node.kubernetes.io/unreachable", Operator: corev1.TolerationOpExists,
					Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &seconds},
			}},
		{name: "invalid selector key", nodeSelector: map[string]string{"bad key": "x"}, wantError: true},
		{name: "invalid selector value", nodeSelector: map[string]string{"pool": "bad value"}, wantError: true},
		{name: "equal without key", tolerations: []corev1.Toleration{{Operator: corev1.TolerationOpEqual, Value: "x"}}, wantError: true},
		{name: "exists with value", tolerations: []corev1.Toleration{{Key: "k", Operator: corev1.TolerationOpExists, Value: "x"}}, wantError: true},
		{name: "unknown operator", tolerations: []corev1.Toleration{{Key: "k", Operator: "In"}}, wantError: true},
		{name: "unknown effect", tolerations: []corev1.Toleration{{Key: "k", Effect: "NoRun"}}, wantError: true},
		{name: "seconds without NoExecute", tolerations: []corev1.Toleration{
			{Key: "k", Effect: corev1.TaintEffectNoSchedule, TolerationSeconds: &seconds}}, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig()
			config.NodeSelector = test.nodeSelector
			config.Tolerations = test.tolerations
			if err := config.Validate(); (err != nil) != test.wantError {
				t.Fatalf("Validate() = %v, want error %t", err, test.wantError)
			}
		})
	}
}

func TestNewBenchmarkJobAppliesAdminPlacementAlongsideRunAffinity(t *testing.T) {
	config := validConfig()
	config.NodeSelector = map[string]string{"pool": "benchmark"}
	config.Tolerations = []corev1.Toleration{
		{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "benchmark", Effect: corev1.TaintEffectNoSchedule},
	}
	wantSelector := map[string]string{"pool": "benchmark"}
	wantTolerations := []corev1.Toleration{
		{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "benchmark", Effect: corev1.TaintEffectNoSchedule},
	}
	options := DefaultOptions()
	options.NodeAffinity = &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
		NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{
			{Key: "disktype", Operator: corev1.NodeSelectorOpIn, Values: []string{"ssd"}},
		}}},
	}}

	job, err := newBenchmarkJob(config, "job", "secret", options, config.Resources, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := job.Spec.Template.Spec
	if !reflect.DeepEqual(spec.NodeSelector, wantSelector) || !reflect.DeepEqual(spec.Tolerations, wantTolerations) {
		t.Fatalf("placement = %v / %v, want %v / %v", spec.NodeSelector, spec.Tolerations, wantSelector, wantTolerations)
	}
	if spec.Affinity == nil || !reflect.DeepEqual(spec.Affinity.NodeAffinity, options.NodeAffinity) {
		t.Fatalf("per-run affinity was not applied: %+v", spec.Affinity)
	}

	spec.NodeSelector["pool"] = "changed"
	spec.Tolerations[0].Value = "changed"
	if !reflect.DeepEqual(config.NodeSelector, wantSelector) || !reflect.DeepEqual(config.Tolerations, wantTolerations) {
		t.Fatal("Job manifest shares placement state with the coordinator config")
	}
}

func TestNewBenchmarkJobOmitsEmptyAdminPlacement(t *testing.T) {
	job, err := newBenchmarkJob(validConfig(), "job", "secret", DefaultOptions(), validConfig().Resources, nil)
	if err != nil {
		t.Fatal(err)
	}
	if spec := job.Spec.Template.Spec; spec.NodeSelector != nil || spec.Tolerations != nil {
		t.Fatalf("unexpected placement: %v / %v", spec.NodeSelector, spec.Tolerations)
	}
}
