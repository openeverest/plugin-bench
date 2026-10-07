package coordinator

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Resources uses Kubernetes quantity strings; empty fields remain unspecified.
type Resources struct {
	CPURequest    string
	MemoryRequest string
	CPULimit      string
	MemoryLimit   string
}

// ResolveResources merges per-run overrides with deployment defaults by value.
// Blank overrides inherit defaults; explicit overrides must be positive.
// Inherited zero and unspecified quantities retain deployment compatibility.
func ResolveResources(defaults, overrides Resources) (Resources, error) {
	provided, err := resourceRequirements(overrides)
	if err != nil {
		return Resources{}, fmt.Errorf("invalid resource overrides: %w", err)
	}
	for _, group := range []struct {
		kind       string
		quantities corev1.ResourceList
	}{
		{"request", provided.Requests},
		{"limit", provided.Limits},
	} {
		for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
			if quantity, exists := group.quantities[name]; exists && quantity.Sign() == 0 {
				return Resources{}, fmt.Errorf("%s %s resource override must be positive", group.kind, name)
			}
		}
	}

	resolved := Resources{
		CPURequest:    inheritResource(defaults.CPURequest, overrides.CPURequest),
		MemoryRequest: inheritResource(defaults.MemoryRequest, overrides.MemoryRequest),
		CPULimit:      inheritResource(defaults.CPULimit, overrides.CPULimit),
		MemoryLimit:   inheritResource(defaults.MemoryLimit, overrides.MemoryLimit),
	}
	if _, err := resourceRequirements(resolved); err != nil {
		return Resources{}, fmt.Errorf("invalid resolved resources: %w", err)
	}
	return resolved, nil
}

func inheritResource(defaultValue, override string) string {
	if value := strings.TrimSpace(override); value != "" {
		return value
	}
	return strings.TrimSpace(defaultValue)
}

func resourceRequirements(resources Resources) (corev1.ResourceRequirements, error) {
	result := corev1.ResourceRequirements{}
	requests, err := parseQuantities("request", resources.CPURequest, resources.MemoryRequest)
	if err != nil {
		return result, err
	}
	limits, err := parseQuantities("limit", resources.CPULimit, resources.MemoryLimit)
	if err != nil {
		return result, err
	}
	result.Requests = requests
	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		request, hasRequest := requests[name]
		limit, hasLimit := limits[name]
		if hasRequest && hasLimit && request.Cmp(limit) > 0 {
			return corev1.ResourceRequirements{}, fmt.Errorf("%s request cannot exceed limit", name)
		}
	}
	result.Limits = limits
	return result, nil
}

func parseQuantities(kind, cpu, memory string) (corev1.ResourceList, error) {
	result := corev1.ResourceList{}
	for _, field := range []struct {
		name  corev1.ResourceName
		value string
	}{
		{corev1.ResourceCPU, cpu},
		{corev1.ResourceMemory, memory},
	} {
		value := strings.TrimSpace(field.value)
		if value == "" {
			continue
		}
		quantity, err := resource.ParseQuantity(value)
		if err != nil {
			return nil, fmt.Errorf("invalid %s %s resource quantity: %w", kind, field.name, err)
		}
		if quantity.Sign() < 0 {
			return nil, fmt.Errorf("%s %s resource quantity must be nonnegative", kind, field.name)
		}
		if field.name == corev1.ResourceCPU {
			// Round a copy so precision is checked without changing the value.
			rounded := quantity.DeepCopy()
			if !rounded.RoundUp(resource.Milli) {
				return nil, fmt.Errorf("%s cpu resource quantity must use increments of 1m", kind)
			}
		}
		result[field.name] = quantity
	}
	return result, nil
}
