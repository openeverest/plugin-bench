package coordinator

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestPrepareNodeAffinityEmpty(t *testing.T) {
	for _, input := range []string{"null", "{}", `{"preferredDuringSchedulingIgnoredDuringExecution":[]}`} {
		t.Run(input, func(t *testing.T) {
			var affinity *corev1.NodeAffinity
			if err := json.Unmarshal([]byte(input), &affinity); err != nil {
				t.Fatal(err)
			}
			got, err := PrepareNodeAffinity(affinity)
			if err != nil || got != nil {
				t.Fatalf("PrepareNodeAffinity(%s) = %v, %v; want nil, nil", input, got, err)
			}
		})
	}
}

func TestPrepareNodeAffinityPreservesRules(t *testing.T) {
	combined := validNodeAffinity()
	for name, input := range map[string]*corev1.NodeAffinity{
		"required only":  {RequiredDuringSchedulingIgnoredDuringExecution: combined.RequiredDuringSchedulingIgnoredDuringExecution},
		"preferred only": {PreferredDuringSchedulingIgnoredDuringExecution: combined.PreferredDuringSchedulingIgnoredDuringExecution},
		"combined":       combined,
	} {
		t.Run(name, func(t *testing.T) {
			before := input.DeepCopy()
			got, err := PrepareNodeAffinity(input)
			if err != nil {
				t.Fatal(err)
			}
			if got == input || !reflect.DeepEqual(got, before) || !reflect.DeepEqual(input, before) {
				t.Fatal("rules must be preserved without modifying or returning the original input")
			}
		})
	}
}

func TestPrepareNodeAffinityLabelOperators(t *testing.T) {
	for _, test := range []struct {
		operator corev1.NodeSelectorOperator
		values   []string
	}{
		{corev1.NodeSelectorOpIn, []string{"benchmark", ""}},
		{corev1.NodeSelectorOpNotIn, []string{"database", "other"}},
		{corev1.NodeSelectorOpExists, nil},
		{corev1.NodeSelectorOpDoesNotExist, []string{}},
		{corev1.NodeSelectorOpGt, []string{"0"}},
		{corev1.NodeSelectorOpLt, []string{"9223372036854775807"}},
	} {
		t.Run(string(test.operator), func(t *testing.T) {
			input := validNodeAffinity()
			input.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions[0] = corev1.NodeSelectorRequirement{
				Key: "example.com/workload", Operator: test.operator, Values: test.values,
			}
			if _, err := PrepareNodeAffinity(input); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPrepareNodeAffinityRejectsInvalidRules(t *testing.T) {
	const required = "nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms"
	const expression = required + "[0].matchExpressions[0]"
	const preferred = "nodeAffinity.preferredDuringSchedulingIgnoredDuringExecution[0]"
	const matchField = required + "[1].matchFields[0]"
	type testCase struct {
		name   string
		change func(*corev1.NodeAffinity)
		path   string
	}
	tests := []testCase{
		{"missing required terms", func(a *corev1.NodeAffinity) { a.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms = nil }, required},
		{"empty required terms", func(a *corev1.NodeAffinity) {
			a.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms = []corev1.NodeSelectorTerm{}
		}, required},
		{"empty required term", func(a *corev1.NodeAffinity) {
			a.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[1] = corev1.NodeSelectorTerm{}
		}, required + "[1]"},
		{"empty preferred term", func(a *corev1.NodeAffinity) {
			a.PreferredDuringSchedulingIgnoredDuringExecution[0].Preference = corev1.NodeSelectorTerm{}
		}, preferred + ".preference"},
		{"zero weight", func(a *corev1.NodeAffinity) { a.PreferredDuringSchedulingIgnoredDuringExecution[0].Weight = 0 }, preferred + ".weight"},
		{"negative weight", func(a *corev1.NodeAffinity) { a.PreferredDuringSchedulingIgnoredDuringExecution[0].Weight = -1 }, preferred + ".weight"},
		{"excessive weight", func(a *corev1.NodeAffinity) { a.PreferredDuringSchedulingIgnoredDuringExecution[0].Weight = 101 }, preferred + ".weight"},
		{"unknown field key", func(a *corev1.NodeAffinity) {
			a.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[1].MatchFields[0].Key = "metadata.namespace"
		}, matchField + ".key"},
		{"field exists operator", func(a *corev1.NodeAffinity) {
			a.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[1].MatchFields[0].Operator = corev1.NodeSelectorOpExists
		}, matchField + ".operator"},
		{"field missing value", func(a *corev1.NodeAffinity) {
			a.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[1].MatchFields[0].Values = nil
		}, matchField + ".values"},
		{"field multiple values", func(a *corev1.NodeAffinity) {
			a.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[1].MatchFields[0].Values = []string{"node-1", "node-2"}
		}, matchField + ".values"},
		{"field invalid node name", func(a *corev1.NodeAffinity) {
			a.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[1].MatchFields[0].Values = []string{"Node 1"}
		}, matchField + ".values[0]"},
		{"preferred invalid expression", func(a *corev1.NodeAffinity) {
			a.PreferredDuringSchedulingIgnoredDuringExecution[0].Preference.MatchExpressions[0].Key = "bad key"
		}, preferred + ".preference.matchExpressions[0].key"},
		{"preferred invalid field", func(a *corev1.NodeAffinity) {
			a.PreferredDuringSchedulingIgnoredDuringExecution[0].Preference.MatchFields[0].Values = []string{""}
		}, preferred + ".preference.matchFields[0].values[0]"},
	}
	for _, test := range []struct {
		name     string
		key      string
		operator corev1.NodeSelectorOperator
		values   []string
		field    string
	}{
		{"missing key", "", corev1.NodeSelectorOpIn, []string{"benchmark"}, "key"},
		{"invalid key", "bad key", corev1.NodeSelectorOpIn, []string{"benchmark"}, "key"},
		{"invalid key prefix", "BAD.example/workload", corev1.NodeSelectorOpExists, nil, "key"},
		{"invalid value", "workload", corev1.NodeSelectorOpIn, []string{"bad value"}, "values[0]"},
		{"oversized value", "workload", corev1.NodeSelectorOpIn, []string{strings.Repeat("x", 64)}, "values[0]"},
		{"unknown operator", "workload", "Equals", []string{"benchmark"}, "operator"},
		{"missing operator", "workload", "", nil, "operator"},
		{"in without values", "workload", corev1.NodeSelectorOpIn, nil, "values"},
		{"notin without values", "workload", corev1.NodeSelectorOpNotIn, []string{}, "values"},
		{"exists with values", "workload", corev1.NodeSelectorOpExists, []string{"benchmark"}, "values"},
		{"doesnotexist with values", "workload", corev1.NodeSelectorOpDoesNotExist, []string{"benchmark"}, "values"},
		{"gt without values", "cores", corev1.NodeSelectorOpGt, nil, "values"},
		{"lt multiple values", "cores", corev1.NodeSelectorOpLt, []string{"1", "2"}, "values"},
		{"gt noninteger", "cores", corev1.NodeSelectorOpGt, []string{"many"}, "values[0]"},
		{"lt fractional", "cores", corev1.NodeSelectorOpLt, []string{"1.5"}, "values[0]"},
		{"gt overflow", "cores", corev1.NodeSelectorOpGt, []string{"9223372036854775808"}, "values[0]"},
	} {
		tests = append(tests, testCase{test.name, func(a *corev1.NodeAffinity) {
			a.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions[0] = corev1.NodeSelectorRequirement{
				Key: test.key, Operator: test.operator, Values: test.values,
			}
		}, expression + "." + test.field})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validNodeAffinity()
			test.change(input)
			before := input.DeepCopy()
			got, err := PrepareNodeAffinity(input)
			if got != nil || err == nil || !strings.Contains(err.Error(), test.path) {
				t.Fatalf("got %v, %v; want nil and error containing %q", got, err, test.path)
			}
			if !reflect.DeepEqual(input, before) {
				t.Fatal("validation modified invalid input")
			}
		})
	}
}

func TestPrepareNodeAffinityDeepCopy(t *testing.T) {
	input := validNodeAffinity()
	before := input.DeepCopy()
	first, err := PrepareNodeAffinity(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PrepareNodeAffinity(input)
	if err != nil {
		t.Fatal(err)
	}
	first.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions[0].Key = "changed"
	first.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions[0].Values[0] = "changed"
	first.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[1].MatchFields[0].Values[0] = "changed"
	first.PreferredDuringSchedulingIgnoredDuringExecution[0].Weight = 1
	first.PreferredDuringSchedulingIgnoredDuringExecution[0].Preference.MatchExpressions[0].Values[0] = "changed"
	first.PreferredDuringSchedulingIgnoredDuringExecution[0].Preference.MatchFields[0].Values[0] = "changed"
	if !reflect.DeepEqual(input, before) || !reflect.DeepEqual(second, before) {
		t.Fatal("prepared affinity shares nested mutable state with input or another prepared run")
	}
	input.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions[0].Values[0] = "input changed"
	input.PreferredDuringSchedulingIgnoredDuringExecution[0].Weight = 2
	if !reflect.DeepEqual(second, before) {
		t.Fatal("changing input modified prepared affinity")
	}
}

func validNodeAffinity() *corev1.NodeAffinity {
	return &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{MatchExpressions: []corev1.NodeSelectorRequirement{
					{Key: "workload", Operator: corev1.NodeSelectorOpIn, Values: []string{"benchmark", "general"}},
					{Key: "dedicated", Operator: corev1.NodeSelectorOpExists},
				}},
				{MatchFields: []corev1.NodeSelectorRequirement{
					{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{"node-1.example.com"}},
				}},
			},
		},
		PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{
			{Weight: 100, Preference: corev1.NodeSelectorTerm{
				MatchExpressions: []corev1.NodeSelectorRequirement{
					{Key: "disk-type", Operator: corev1.NodeSelectorOpIn, Values: []string{"ssd"}},
				},
				MatchFields: []corev1.NodeSelectorRequirement{
					{Key: "metadata.name", Operator: corev1.NodeSelectorOpNotIn, Values: []string{"node-2"}},
				},
			}},
			{Weight: 1, Preference: corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
				{Key: "network", Operator: corev1.NodeSelectorOpIn, Values: []string{"fast"}},
			}}},
		},
	}
}
