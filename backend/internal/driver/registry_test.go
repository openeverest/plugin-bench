package driver

import (
	"errors"
	"strings"
	"testing"
)

type stubDriver struct {
	id           string
	databaseType string
}

func (d stubDriver) ID() string { return d.id }
func (d stubDriver) Supports(target TargetInfo) bool {
	return target.DatabaseType == d.databaseType
}
func (d stubDriver) Prepare(PrepareInput) (RunnerSpec, error) {
	panic("registry lookup must not prepare a workload")
}

func registration(id string) Registration {
	return Registration{
		Driver: stubDriver{id: id, databaseType: "postgresql"},
		Runner: RunnerConfig{Image: "registry.example/runner:1", ImagePullPolicy: "IfNotPresent"},
	}
}

func TestNewRegistryRejectsInvalidRegistrations(t *testing.T) {
	for _, test := range []struct {
		name          string
		registrations []Registration
	}{
		{"empty", nil},
		{"nil driver", []Registration{{Runner: registration("pgbench").Runner}}},
		{"blank ID", []Registration{registration(" \t")}},
		{"surrounding whitespace in ID", []Registration{registration(" pgbench ")}},
		{"duplicate ID", []Registration{registration("pgbench"), registration("pgbench")}},
		{"missing image", []Registration{{Driver: stubDriver{id: "pgbench"}, Runner: RunnerConfig{ImagePullPolicy: "Never"}}}},
		{"blank image", []Registration{{Driver: stubDriver{id: "pgbench"}, Runner: RunnerConfig{Image: " \t", ImagePullPolicy: "Never"}}}},
		{"invalid pull policy", []Registration{{Driver: stubDriver{id: "pgbench"}, Runner: RunnerConfig{Image: "runner:1", ImagePullPolicy: "sometimes"}}}},
		{"missing pull policy", []Registration{{Driver: stubDriver{id: "pgbench"}, Runner: RunnerConfig{Image: "runner:1"}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := NewRegistry(test.registrations...)
			if err == nil || got != nil {
				t.Fatalf("NewRegistry() = %v, %v; want nil registry and error", got, err)
			}
		})
	}
}

func TestRegistryResolvesExplicitDriver(t *testing.T) {
	pgbench, alternative := registration("pgbench"), registration("alternative")
	alternative.Runner.Image = "registry.example/alternative:2"
	registrations := []Registration{pgbench, alternative}
	registry, err := NewRegistry(registrations...)
	if err != nil {
		t.Fatal(err)
	}
	// Registry construction owns the registration values, rather than the input slice.
	registrations[0] = alternative

	for _, want := range []Registration{pgbench, alternative} {
		got, err := registry.Resolve(want.Driver.ID(), TargetInfo{DatabaseType: "postgresql", Provider: "unfamiliar-provider"})
		if err != nil || got.Driver != want.Driver || got.Runner != want.Runner {
			t.Fatalf("Resolve(%q) = %+v, %v; want %+v", want.Driver.ID(), got, err, want)
		}
		got.Runner.Image = "changed"
		again, err := registry.Resolve(want.Driver.ID(), TargetInfo{DatabaseType: "postgresql"})
		if err != nil || again.Runner != want.Runner {
			t.Fatal("changing a returned registration changed the registry")
		}
	}
}

func TestRegistryRejectsInvalidSelection(t *testing.T) {
	registry, err := NewRegistry(registration("pgbench"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, id, databaseType string
		registry               *Registry
		kind                   ErrorKind
	}{
		{"missing ID", "", "postgresql", registry, DriverSelection},
		{"blank ID", " \t", "postgresql", registry, DriverSelection},
		{"unknown ID", "credential-value", "postgresql", registry, DriverSelection},
		{"no implicit trimming", " pgbench ", "postgresql", registry, DriverSelection},
		{"incompatible database", "pgbench", "credential-value", registry, UnsupportedConnection},
		{"nil registry", "pgbench", "postgresql", nil, DriverSelection},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.registry.Resolve(test.id, TargetInfo{DatabaseType: test.databaseType, Provider: "credential-value"})
			var driverErr *Error
			if !errors.As(err, &driverErr) || driverErr.Kind != test.kind {
				t.Fatalf("Resolve() error = %v; want driver error kind %q", err, test.kind)
			}
			if got.Driver != nil || got.Runner != (RunnerConfig{}) {
				t.Fatal("failed lookup returned a usable registration")
			}
			if strings.Contains(err.Error(), "credential-value") {
				t.Fatal("lookup error echoed input")
			}
		})
	}
}

func TestRunnerConfigValidPullPolicies(t *testing.T) {
	for _, policy := range []string{"Always", "IfNotPresent", "Never"} {
		if err := (RunnerConfig{Image: "registry.example/runner@sha256:abcdef", ImagePullPolicy: policy}).Validate(); err != nil {
			t.Fatalf("pull policy %q: %v", policy, err)
		}
	}
}
