package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

type driverDeploymentConfig struct {
	Enabled         *bool  `json:"enabled"`
	Image           string `json:"image"`
	ImagePullPolicy string `json:"imagePullPolicy"`
}

// driverRegistrationsFromEnv consumes resolved image references, not Helm's
// repository/tag settings. Only constructors compiled into main are available.
// Presence of BENCHMARK_DRIVERS is authoritative, even when its value is empty.
func driverRegistrationsFromEnv(constructors map[string]func() driver.Driver) ([]driver.Registration, error) {
	payload, present := os.LookupEnv("BENCHMARK_DRIVERS")
	if !present {
		enabled := true
		return configuredDriverRegistrations(map[string]driverDeploymentConfig{
			"pgbench": {
				Enabled:         &enabled,
				Image:           os.Getenv("RUNNER_IMAGE"),
				ImagePullPolicy: envOrDefault("RUNNER_IMAGE_PULL_POLICY", "IfNotPresent"),
			},
		}, constructors)
	}
	var entries map[string]driverDeploymentConfig
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&entries); err != nil {
		return nil, errors.New("parse BENCHMARK_DRIVERS: expected a JSON object with valid driver settings")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("parse BENCHMARK_DRIVERS: unexpected data after JSON object")
	}
	return configuredDriverRegistrations(entries, constructors)
}

func configuredDriverRegistrations(entries map[string]driverDeploymentConfig, constructors map[string]func() driver.Driver) ([]driver.Registration, error) {
	// Stable ordering makes registration and startup failures reproducible.
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var registrations []driver.Registration
	for _, id := range ids {
		entry := entries[id]
		if strings.TrimSpace(id) == "" || strings.TrimSpace(id) != id {
			return nil, errors.New("BENCHMARK_DRIVERS: driver IDs must be nonempty without surrounding whitespace")
		}
		if entry.Enabled == nil {
			return nil, fmt.Errorf("BENCHMARK_DRIVERS: driver %q requires an explicit enabled boolean", id)
		}
		if !*entry.Enabled {
			continue
		}
		constructor := constructors[id]
		if constructor == nil {
			return nil, fmt.Errorf("BENCHMARK_DRIVERS: enabled driver %q is not compiled into the backend", id)
		}
		policy := entry.ImagePullPolicy
		if policy == "" {
			policy = "IfNotPresent"
		}
		runner := driver.RunnerConfig{Image: entry.Image, ImagePullPolicy: policy}
		if err := runner.Validate(); err != nil {
			return nil, fmt.Errorf("driver %q: %w", id, err)
		}
		registrations = append(registrations, driver.Registration{Driver: constructor(), Runner: runner})
	}
	if len(registrations) == 0 {
		return nil, errors.New("BENCHMARK_DRIVERS: at least one enabled driver is required")
	}
	return registrations, nil
}
