package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	"github.com/openeverest/plugin-bench/backend/internal/driver"
	"github.com/openeverest/plugin-bench/backend/internal/everest"
	"github.com/openeverest/plugin-bench/backend/internal/runnerresult"
)

// preparedRun is transient: only Snapshot is safe to persist or return.
type preparedRun struct {
	Input           coordinator.RunInput
	Snapshot        RunSnapshot
	SensitiveValues []string
}

// prepareSharedRequest validates deployment-independent HTTP fields before
// credential lookup. Keep overrides separate from the effective resource
// snapshot: the coordinator must still distinguish inherited zero defaults.
func (a *API) prepareSharedRequest(request CreateRunRequest) (CreateRunRequest, coordinator.Resources, error) {
	for _, target := range []struct{ name, value string }{
		{"k8sCluster", request.Target.K8sCluster}, {"namespace", request.Target.Namespace}, {"instance", request.Target.Instance},
	} {
		if strings.TrimSpace(target.value) == "" {
			return request, coordinator.Resources{}, errors.New("target." + target.name + " is required")
		}
	}
	if strings.TrimSpace(request.Driver) == "" {
		return request, coordinator.Resources{}, errors.New("driver is required")
	}
	resources, err := coordinator.ResolveResources(a.resourceDefaults, request.Resources)
	if err != nil {
		return request, coordinator.Resources{}, err
	}
	request.NodeAffinity, err = coordinator.PrepareNodeAffinity(request.NodeAffinity)
	return request, resources, err
}

// prepareRun consumes a request whose shared fields were prepared above. Driver
// selection uses authorized target metadata, never a client-supplied engine.
func (a *API) prepareRun(request CreateRunRequest, credentials *everest.Credentials) (preparedRun, error) {
	if credentials == nil {
		return preparedRun{}, &driver.Error{Kind: driver.InvalidCredentials, Message: "OpenEverest returned no connection details"}
	}
	if strings.TrimSpace(credentials.Type) == "" {
		return preparedRun{}, &driver.Error{Kind: driver.InvalidCredentials, Message: "database type is missing"}
	}
	target := driver.TargetInfo{DatabaseType: credentials.Type, Provider: credentials.Provider}
	registration, err := a.registry.Resolve(request.Driver, target)
	if err != nil {
		return preparedRun{}, err
	}
	spec, err := registration.Driver.Prepare(driver.PrepareInput{
		Target: target,
		Connection: driver.ConnectionDetails{Host: credentials.Host, Port: credentials.Port, Username: credentials.Username,
			Password: credentials.Password, URI: credentials.URI, Database: credentials.Database},
		RequestedDatabase: request.Database,
		Options:           request.Options,
	})
	if err != nil {
		return preparedRun{}, err
	}
	spec = spec.Clone()
	input := coordinator.RunInput{Runner: registration.Runner, Spec: spec, Resources: request.Resources, NodeAffinity: request.NodeAffinity.DeepCopy()}
	if err := input.Validate(); err != nil {
		return preparedRun{}, errors.New("driver produced an invalid runner workload")
	}
	return preparedRun{
		Input: input,
		Snapshot: RunSnapshot{Driver: registration.Driver.ID(), RunnerImage: registration.Runner.Image,
			Database: spec.Snapshot.Database, Options: append(json.RawMessage(nil), spec.Snapshot.Options...)},
		SensitiveValues: redactionValues(spec),
	}, nil
}

func preparationError(err error) (int, string) {
	var problem *driver.Error
	if errors.As(err, &problem) {
		switch problem.Kind {
		case driver.InvalidOptions, driver.DriverSelection:
			return http.StatusBadRequest, problem.Error()
		case driver.UnsupportedConnection:
			return http.StatusUnprocessableEntity, problem.Error()
		case driver.InvalidCredentials:
			return http.StatusBadGateway, problem.Error()
		}
	}
	return http.StatusInternalServerError, "failed to prepare benchmark workload"
}

func redactionValues(spec driver.RunnerSpec) []string {
	seen := make(map[string]struct{})
	for _, value := range spec.SecretData {
		if len(value) != 0 {
			seen[string(value)] = struct{}{}
		}
	}
	for _, value := range spec.SensitiveValues {
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	values := make([]string, 0, len(seen))
	for value := range seen {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool {
		if len(values[i]) != len(values[j]) {
			return len(values[i]) > len(values[j])
		}
		return values[i] < values[j]
	})
	return values
}

// sanitizeRunnerMetrics drops secret-bearing names/units rather than masking
// them into different metric identities. Accepted values retain their order
// and numbers, and the returned slice is independent of the runner's result.
func sanitizeRunnerMetrics(metrics []runnerresult.Metric, sensitiveValues ...string) []runnerresult.Metric {
	var safe []runnerresult.Metric
	for _, metric := range metrics {
		sensitive := false
		for _, value := range sensitiveValues {
			if value != "" && (strings.Contains(metric.Name, value) || strings.Contains(metric.Unit, value)) {
				sensitive = true
				break
			}
		}
		if !sensitive {
			safe = append(safe, metric)
		}
	}
	return safe
}

func sanitizeRunnerOutput(output string, sensitiveValues ...string) string {
	// Replacer scans the original text once. Longest matches come first, and a
	// replacement can never be modified again by a shorter secret value.
	pairs := make([]string, 0, 2*len(sensitiveValues))
	for _, value := range sensitiveValues {
		if value != "" {
			pairs = append(pairs, value, "[REDACTED]")
		}
	}
	if len(pairs) == 0 {
		return output
	}
	return strings.NewReplacer(pairs...).Replace(output)
}
