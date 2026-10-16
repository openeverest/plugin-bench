package coordinator

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// PrepareNodeAffinity validates per-run scheduling rules and returns a deep copy.
// Nil and empty input mean no affinity. Invalid rules are never silently dropped.
// It validates syntax, not whether any available node can satisfy the rules.
func PrepareNodeAffinity(affinity *corev1.NodeAffinity) (*corev1.NodeAffinity, error) {
	if affinity == nil || (affinity.RequiredDuringSchedulingIgnoredDuringExecution == nil &&
		len(affinity.PreferredDuringSchedulingIgnoredDuringExecution) == 0) {
		return nil, nil
	}

	path := field.NewPath("nodeAffinity")
	if required := affinity.RequiredDuringSchedulingIgnoredDuringExecution; required != nil {
		termsPath := path.Child("requiredDuringSchedulingIgnoredDuringExecution", "nodeSelectorTerms")
		if len(required.NodeSelectorTerms) == 0 {
			return nil, field.Required(termsPath, "must contain at least one nonempty term")
		}
		for i, term := range required.NodeSelectorTerms {
			if err := validateNodeAffinityTerm(term, termsPath.Index(i)); err != nil {
				return nil, err
			}
		}
	}
	preferredPath := path.Child("preferredDuringSchedulingIgnoredDuringExecution")
	for i, term := range affinity.PreferredDuringSchedulingIgnoredDuringExecution {
		termPath := preferredPath.Index(i)
		if term.Weight < 1 || term.Weight > 100 {
			return nil, field.Invalid(termPath.Child("weight"), term.Weight, "must be between 1 and 100")
		}
		if err := validateNodeAffinityTerm(term.Preference, termPath.Child("preference")); err != nil {
			return nil, err
		}
	}
	return affinity.DeepCopy(), nil
}

func validateNodeAffinityTerm(term corev1.NodeSelectorTerm, path *field.Path) error {
	// Kubernetes treats empty required terms as matching no nodes. Reject them
	// (and ineffective empty preferences) to catch accidental empty user input.
	if len(term.MatchExpressions) == 0 && len(term.MatchFields) == 0 {
		return field.Required(path, "must contain at least one matchExpression or matchField")
	}
	for i, requirement := range term.MatchExpressions {
		if err := validateNodeLabelRequirement(requirement, path.Child("matchExpressions").Index(i)); err != nil {
			return err
		}
	}
	for i, requirement := range term.MatchFields {
		if err := validateNodeFieldRequirement(requirement, path.Child("matchFields").Index(i)); err != nil {
			return err
		}
	}
	return nil
}

func validateNodeLabelRequirement(requirement corev1.NodeSelectorRequirement, path *field.Path) error {
	var operator selection.Operator
	switch requirement.Operator {
	case corev1.NodeSelectorOpIn:
		operator = selection.In
	case corev1.NodeSelectorOpNotIn:
		operator = selection.NotIn
	case corev1.NodeSelectorOpExists:
		operator = selection.Exists
	case corev1.NodeSelectorOpDoesNotExist:
		operator = selection.DoesNotExist
	case corev1.NodeSelectorOpGt:
		operator = selection.GreaterThan
	case corev1.NodeSelectorOpLt:
		operator = selection.LessThan
	default:
		return field.NotSupported(path.Child("operator"), requirement.Operator,
			[]string{"In", "NotIn", "Exists", "DoesNotExist", "Gt", "Lt"})
	}
	// Reuse Kubernetes label validation for keys, values, cardinality, and
	// signed 64-bit integer thresholds rather than maintaining parallel rules.
	_, err := labels.NewRequirement(requirement.Key, operator, requirement.Values, field.WithPath(path))
	return err
}

func validateNodeFieldRequirement(requirement corev1.NodeSelectorRequirement, path *field.Path) error {
	// Node field selectors support only metadata.name, with In/NotIn and
	// exactly one valid node name. Label selector operators do not all apply.
	if requirement.Key != "metadata.name" {
		return field.NotSupported(path.Child("key"), requirement.Key, []string{"metadata.name"})
	}
	if requirement.Operator != corev1.NodeSelectorOpIn && requirement.Operator != corev1.NodeSelectorOpNotIn {
		return field.NotSupported(path.Child("operator"), requirement.Operator, []string{"In", "NotIn"})
	}
	if len(requirement.Values) != 1 {
		return field.Required(path.Child("values"), "must contain exactly one node name")
	}
	if problems := validation.IsDNS1123Subdomain(requirement.Values[0]); len(problems) > 0 {
		return field.Invalid(path.Child("values").Index(0), requirement.Values[0], problems[0])
	}
	return nil
}
