package driver

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Registration binds a compiled-in driver to its configured runner image.
// Driver implementations must retain a stable ID after registration.
type Registration struct {
	Driver Driver
	Runner RunnerConfig
}

// Registry is constructed at startup and supports concurrent lookups. It owns
// registration values; driver implementations are shared and must be immutable
// or synchronize their own state.
type Registry struct {
	registrations map[string]Registration
}

// NewRegistry accepts only enabled registrations from trusted startup config.
// There must be at least one usable driver before the service starts.
func NewRegistry(registrations ...Registration) (*Registry, error) {
	if len(registrations) == 0 {
		return nil, errors.New("at least one enabled driver is required")
	}
	registry := &Registry{registrations: make(map[string]Registration, len(registrations))}
	for _, registration := range registrations {
		if registration.Driver == nil {
			return nil, errors.New("registered driver is required")
		}
		id := registration.Driver.ID()
		if strings.TrimSpace(id) == "" || strings.TrimSpace(id) != id {
			return nil, errors.New("driver ID must be nonempty without surrounding whitespace")
		}
		if _, exists := registry.registrations[id]; exists {
			return nil, errors.New("driver IDs must be unique")
		}
		if err := registration.Runner.Validate(); err != nil {
			return nil, fmt.Errorf("invalid registered runner: %w", err)
		}
		registry.registrations[id] = registration
	}
	return registry, nil
}

// IDs returns the enabled driver IDs in sorted order. The caller owns the slice.
func (r *Registry) IDs() []string {
	ids := make([]string, 0, len(r.registrations))
	for id := range r.registrations {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// Resolve selects exactly the requested driver; it never infers a driver or
// prepares a workload. Errors do not echo client input or connection details.
func (r *Registry) Resolve(id string, target TargetInfo) (Registration, error) {
	if strings.TrimSpace(id) == "" {
		return Registration{}, &Error{Kind: DriverSelection, Message: "driver is required"}
	}
	if r == nil {
		return Registration{}, &Error{Kind: DriverSelection, Message: "driver is not available"}
	}
	registration, exists := r.registrations[id]
	if !exists {
		return Registration{}, &Error{Kind: DriverSelection, Message: "driver is not available"}
	}
	if !registration.Driver.Supports(target) {
		return Registration{}, &Error{Kind: UnsupportedConnection, Message: "driver does not support the target database type"}
	}
	return registration, nil
}
