package coordinator

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// validateRunnerPlacement checks admin-defined placement at startup so that a
// typo fails the deployment instead of every benchmark Job creation.
func validateRunnerPlacement(nodeSelector map[string]string, tolerations []corev1.Toleration) error {
	selectorPath := field.NewPath("runner", "nodeSelector")
	for key, value := range nodeSelector {
		if problems := validation.IsQualifiedName(key); len(problems) > 0 {
			return field.Invalid(selectorPath.Key(key), key, problems[0])
		}
		if problems := validation.IsValidLabelValue(value); len(problems) > 0 {
			return field.Invalid(selectorPath.Key(key), value, problems[0])
		}
	}

	tolerationsPath := field.NewPath("runner", "tolerations")
	for i, toleration := range tolerations {
		if err := validateToleration(toleration, tolerationsPath.Index(i)); err != nil {
			return err
		}
	}
	return nil
}

func validateToleration(toleration corev1.Toleration, path *field.Path) error {
	if toleration.Key != "" {
		if problems := validation.IsQualifiedName(toleration.Key); len(problems) > 0 {
			return field.Invalid(path.Child("key"), toleration.Key, problems[0])
		}
	}
	switch toleration.Operator {
	case corev1.TolerationOpEqual, "":
		if toleration.Key == "" {
			return field.Invalid(path.Child("operator"), toleration.Operator, "must be Exists when key is empty")
		}
		if problems := validation.IsValidLabelValue(toleration.Value); len(problems) > 0 {
			return field.Invalid(path.Child("value"), toleration.Value, problems[0])
		}
	case corev1.TolerationOpExists:
		if toleration.Value != "" {
			return field.Invalid(path.Child("value"), toleration.Value, "must be empty when operator is Exists")
		}
	default:
		return field.NotSupported(path.Child("operator"), toleration.Operator, []string{"Equal", "Exists"})
	}
	switch toleration.Effect {
	case "", corev1.TaintEffectNoSchedule, corev1.TaintEffectPreferNoSchedule, corev1.TaintEffectNoExecute:
	default:
		return field.NotSupported(path.Child("effect"), toleration.Effect,
			[]string{"NoSchedule", "PreferNoSchedule", "NoExecute"})
	}
	if toleration.TolerationSeconds != nil && toleration.Effect != corev1.TaintEffectNoExecute {
		return field.Invalid(path.Child("tolerationSeconds"), *toleration.TolerationSeconds,
			"requires effect NoExecute")
	}
	return nil
}

func copyTolerations(tolerations []corev1.Toleration) []corev1.Toleration {
	if len(tolerations) == 0 {
		return nil
	}
	result := make([]corev1.Toleration, len(tolerations))
	for i := range tolerations {
		tolerations[i].DeepCopyInto(&result[i])
	}
	return result
}
