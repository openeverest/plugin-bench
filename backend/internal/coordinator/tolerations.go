package coordinator

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func validateAllowedTolerationKeys(keys []string) error {
	path := field.NewPath("allowedTolerationKeys")
	for i, key := range keys {
		if problems := validation.IsQualifiedName(key); len(problems) > 0 {
			return field.Invalid(path.Index(i), key, problems[0])
		}
	}
	return nil
}

// PrepareTolerations validates native tolerations against an exact-key allowlist
// and returns independent copies. Empty allowedKeys permits no custom tolerations.
// A permitted key allows all valid values and effects for that key.
func PrepareTolerations(tolerations []corev1.Toleration, allowedKeys []string) ([]corev1.Toleration, error) {
	if err := validateAllowedTolerationKeys(allowedKeys); err != nil {
		return nil, err
	}
	allowed := sets.New[string](allowedKeys...)
	prepared := make([]corev1.Toleration, len(tolerations))
	for i, toleration := range tolerations {
		path := field.NewPath("tolerations").Index(i)
		if problems := validation.IsQualifiedName(toleration.Key); len(problems) > 0 {
			return nil, field.Invalid(path.Child("key"), toleration.Key, problems[0])
		}
		if !allowed.Has(toleration.Key) {
			return nil, field.Forbidden(path.Child("key"), "key is not allowed by the benchmark administrator")
		}
		if toleration.Operator == "" {
			toleration.Operator = corev1.TolerationOpEqual
		}
		switch toleration.Operator {
		case corev1.TolerationOpEqual:
			if problems := validation.IsValidLabelValue(toleration.Value); len(problems) > 0 {
				return nil, field.Invalid(path.Child("value"), toleration.Value, problems[0])
			}
		case corev1.TolerationOpExists:
			if toleration.Value != "" {
				return nil, field.Invalid(path.Child("value"), toleration.Value, "must be empty when operator is Exists")
			}
		default:
			return nil, field.NotSupported(path.Child("operator"), toleration.Operator, []string{"Equal", "Exists"})
		}
		switch toleration.Effect {
		case "", corev1.TaintEffectNoSchedule, corev1.TaintEffectPreferNoSchedule, corev1.TaintEffectNoExecute:
		default:
			return nil, field.NotSupported(path.Child("effect"), toleration.Effect,
				[]string{"", "NoSchedule", "PreferNoSchedule", "NoExecute"})
		}
		if toleration.TolerationSeconds != nil {
			if toleration.Effect != corev1.TaintEffectNoExecute {
				return nil, field.Forbidden(path.Child("tolerationSeconds"), "requires effect NoExecute")
			}
			if *toleration.TolerationSeconds < 0 {
				return nil, field.Invalid(path.Child("tolerationSeconds"), *toleration.TolerationSeconds, "must be nonnegative")
			}
		}
		prepared[i] = *toleration.DeepCopy()
	}
	return prepared, nil
}
