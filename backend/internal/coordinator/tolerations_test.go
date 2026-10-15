package coordinator

import (
	"math"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestPrepareTolerations(t *testing.T) {
	seconds := func(value int64) *int64 { return &value }
	for _, test := range []struct {
		name       string
		toleration corev1.Toleration
		wantPath   string
	}{
		{name: "default operator", toleration: corev1.Toleration{Key: "dedicated", Value: "benchmark"}},
		{name: "equal empty value", toleration: corev1.Toleration{Key: "dedicated", Operator: corev1.TolerationOpEqual}},
		{name: "no schedule", toleration: corev1.Toleration{Key: "dedicated", Effect: corev1.TaintEffectNoSchedule}},
		{name: "prefer no schedule", toleration: corev1.Toleration{Key: "dedicated", Effect: corev1.TaintEffectPreferNoSchedule}},
		{name: "exists", toleration: corev1.Toleration{Key: "dedicated", Operator: corev1.TolerationOpExists}},
		{name: "qualified key", toleration: corev1.Toleration{Key: "example.com/benchmark", Value: "yes"}},
		{name: "no execute indefinite", toleration: corev1.Toleration{Key: "dedicated", Effect: corev1.TaintEffectNoExecute}},
		{name: "no execute zero", toleration: corev1.Toleration{Key: "dedicated", Effect: corev1.TaintEffectNoExecute, TolerationSeconds: seconds(0)}},
		{name: "no execute positive", toleration: corev1.Toleration{Key: "dedicated", Effect: corev1.TaintEffectNoExecute, TolerationSeconds: seconds(120)}},
		{name: "no execute int64", toleration: corev1.Toleration{Key: "dedicated", Effect: corev1.TaintEffectNoExecute, TolerationSeconds: seconds(math.MaxInt64)}},
		{name: "empty key exists", toleration: corev1.Toleration{Operator: corev1.TolerationOpExists}, wantPath: ".key"},
		{name: "wildcard key", toleration: corev1.Toleration{Key: "*"}, wantPath: ".key"},
		{name: "invalid key", toleration: corev1.Toleration{Key: "invalid key"}, wantPath: ".key"},
		{name: "forbidden key", toleration: corev1.Toleration{Key: "production"}, wantPath: ".key"},
		{name: "case sensitive key", toleration: corev1.Toleration{Key: "Dedicated"}, wantPath: ".key"},
		{name: "not prefix match", toleration: corev1.Toleration{Key: "dedicated-pool"}, wantPath: ".key"},
		{name: "invalid operator", toleration: corev1.Toleration{Key: "dedicated", Operator: "In"}, wantPath: ".operator"},
		{name: "exists with value", toleration: corev1.Toleration{Key: "dedicated", Operator: corev1.TolerationOpExists, Value: "benchmark"}, wantPath: ".value"},
		{name: "invalid value", toleration: corev1.Toleration{Key: "dedicated", Value: "not a label"}, wantPath: ".value"},
		{name: "long value", toleration: corev1.Toleration{Key: "dedicated", Value: strings.Repeat("a", 64)}, wantPath: ".value"},
		{name: "invalid effect", toleration: corev1.Toleration{Key: "dedicated", Effect: "Schedule"}, wantPath: ".effect"},
		{name: "negative seconds", toleration: corev1.Toleration{Key: "dedicated", Effect: corev1.TaintEffectNoExecute, TolerationSeconds: seconds(-1)}, wantPath: ".tolerationSeconds"},
		{name: "seconds all effects", toleration: corev1.Toleration{Key: "dedicated", TolerationSeconds: seconds(0)}, wantPath: ".tolerationSeconds"},
		{name: "seconds no schedule", toleration: corev1.Toleration{Key: "dedicated", Effect: corev1.TaintEffectNoSchedule, TolerationSeconds: seconds(1)}, wantPath: ".tolerationSeconds"},
		{name: "seconds prefer no schedule", toleration: corev1.Toleration{Key: "dedicated", Effect: corev1.TaintEffectPreferNoSchedule, TolerationSeconds: seconds(1)}, wantPath: ".tolerationSeconds"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := []corev1.Toleration{test.toleration}
			before := test.toleration.DeepCopy()
			got, err := PrepareTolerations(input, []string{"dedicated", "example.com/benchmark"})
			if !reflect.DeepEqual(input[0], *before) {
				t.Fatal("preparation changed the input")
			}
			if test.wantPath != "" {
				if err == nil || !strings.Contains(err.Error(), "tolerations[0]"+test.wantPath) || got != nil {
					t.Fatalf("PrepareTolerations() = %v, %v; want no output and error at %s", got, err, test.wantPath)
				}
				return
			}
			want := *before
			if want.Operator == "" {
				want.Operator = corev1.TolerationOpEqual
			}
			if err != nil || !reflect.DeepEqual(got, []corev1.Toleration{want}) {
				t.Fatalf("PrepareTolerations() = %v, %v; want %v", got, err, want)
			}
		})
	}
}

func TestPrepareTolerationsEmptyAllowlist(t *testing.T) {
	for _, keys := range [][]string{nil, {}} {
		for _, input := range [][]corev1.Toleration{nil, {}} {
			got, err := PrepareTolerations(input, keys)
			if err != nil || got == nil || len(got) != 0 {
				t.Fatalf("empty input = %v, %v; want empty slice", got, err)
			}
		}
		got, err := PrepareTolerations([]corev1.Toleration{{Key: "dedicated"}}, keys)
		if got != nil || err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("custom input = %v, %v; want policy rejection", got, err)
		}
	}
}

func TestPrepareTolerationsCopiesAndPreservesEntries(t *testing.T) {
	seconds := int64(120)
	input := []corev1.Toleration{
		{Key: "dedicated", Value: "benchmark", Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &seconds},
		{Key: "dedicated", Value: "production", Effect: corev1.TaintEffectNoSchedule},
		{Key: "dedicated", Value: "production", Effect: corev1.TaintEffectNoSchedule},
	}
	got, err := PrepareTolerations(input, []string{"dedicated", "dedicated"})
	if err != nil || len(got) != 3 || got[1].Value != "production" || !reflect.DeepEqual(got[1], got[2]) {
		t.Fatalf("entries were dropped, reordered, or restricted by value: %v, %v", got, err)
	}
	input[0].Value = "changed"
	seconds = 10
	if got[0].Value != "benchmark" || *got[0].TolerationSeconds != 120 {
		t.Fatal("prepared tolerations share input memory")
	}
	*got[0].TolerationSeconds = 30
	if seconds != 10 {
		t.Fatal("changing prepared seconds changed input")
	}
	got, err = PrepareTolerations(append(input, corev1.Toleration{Key: "forbidden"}), []string{"dedicated"})
	if got != nil || err == nil || !strings.Contains(err.Error(), "tolerations[3].key") {
		t.Fatalf("invalid later entry = %v, %v; want indexed error without partial output", got, err)
	}
}
