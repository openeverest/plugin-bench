package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

func TestDriverConfigurationOverridesLegacyEnvironment(t *testing.T) {
	for _, test := range []struct {
		name, payload, image, policy string
	}{
		{"explicit settings", `{"pgbench":{"enabled":true,"image":"example.com/runner:v2","imagePullPolicy":"Always"}}`, "example.com/runner:v2", "Always"},
		{"default pull policy", `{"pgbench":{"enabled":true,"image":"example.com/runner:v2"}}`, "example.com/runner:v2", "IfNotPresent"},
		{"never pull", `{"pgbench":{"enabled":true,"image":"runner:local","imagePullPolicy":"Never"}}`, "runner:local", "Never"},
		{"digest image", `{"pgbench":{"enabled":true,"image":"example.com/runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`, "example.com/runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "IfNotPresent"},
		{"disabled unknown driver", `{"future-tool":{"enabled":false},"pgbench":{"enabled":true,"image":"runner:v2"}}`, "runner:v2", "IfNotPresent"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("BENCHMARK_DRIVERS", test.payload)
			// Neither legacy image nor legacy policy may affect a present payload.
			t.Setenv("RUNNER_IMAGE", "legacy:old")
			t.Setenv("RUNNER_IMAGE_PULL_POLICY", "invalid-legacy-policy")
			registry, err := benchmarkRegistryFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			got, err := registry.Resolve("pgbench", driver.TargetInfo{DatabaseType: "postgresql"})
			if err != nil || got.Runner.Image != test.image || got.Runner.ImagePullPolicy != test.policy {
				t.Fatalf("registration = %+v, error = %v", got.Runner, err)
			}
			if _, err := registry.Resolve("future-tool", driver.TargetInfo{DatabaseType: "postgresql"}); err == nil {
				t.Fatal("disabled driver was registered")
			}
		})
	}
}

func TestDriverConfigurationRejectsInvalidPayloadWithoutLegacyFallback(t *testing.T) {
	for _, test := range []struct{ name, payload, message string }{
		{"empty variable", "", "parse BENCHMARK_DRIVERS"},
		{"whitespace", " \n ", "parse BENCHMARK_DRIVERS"},
		{"malformed JSON", "{", "parse BENCHMARK_DRIVERS"},
		{"array", "[]", "parse BENCHMARK_DRIVERS"},
		{"string", `"pgbench"`, "parse BENCHMARK_DRIVERS"},
		{"null object", "null", "at least one enabled driver"},
		{"empty object", "{}", "at least one enabled driver"},
		{"trailing object", `{"pgbench":{"enabled":true,"image":"runner:v2"}} {}`, "unexpected data"},
		{"trailing garbage", `{"pgbench":{"enabled":true,"image":"runner:v2"}} garbage`, "unexpected data"},
		{"trailing null", `{"pgbench":{"enabled":true,"image":"runner:v2"}} null`, "unexpected data"},
		{"null entry", `{"pgbench":null}`, "explicit enabled boolean"},
		{"missing enabled", `{"pgbench":{"image":"runner:v2"}}`, "explicit enabled boolean"},
		{"null enabled", `{"pgbench":{"enabled":null,"image":"runner:v2"}}`, "explicit enabled boolean"},
		{"wrong enabled type", `{"pgbench":{"enabled":"true","image":"runner:v2"}}`, "parse BENCHMARK_DRIVERS"},
		{"unknown field", `{"pgbench":{"enabled":true,"image":"runner:v2","pullPolicy":"Always"}}`, "parse BENCHMARK_DRIVERS"},
		{"unknown enabled driver", `{"future-tool":{"enabled":true,"image":"runner:v2"}}`, "not compiled into the backend"},
		{"disabled pgbench", `{"pgbench":{"enabled":false}}`, "at least one enabled driver"},
		{"only unknown disabled driver", `{"future-tool":{"enabled":false}}`, "at least one enabled driver"},
		{"disabled sysbench", `{"sysbench":{"enabled":false}}`, "at least one enabled driver"},
		{"missing sysbench image", `{"sysbench":{"enabled":true}}`, "runner image is required"},
		{"invalid sysbench pull policy", `{"sysbench":{"enabled":true,"image":"runner:v2","imagePullPolicy":"Sometimes"}}`, "image pull policy"},
		{"missing image", `{"pgbench":{"enabled":true}}`, "runner image is required"},
		{"blank image", `{"pgbench":{"enabled":true,"image":"  "}}`, "runner image is required"},
		{"invalid pull policy", `{"pgbench":{"enabled":true,"image":"runner:v2","imagePullPolicy":"Sometimes"}}`, "image pull policy"},
		{"empty ID", `{"":{"enabled":true,"image":"runner:v2"}}`, "driver IDs"},
		{"whitespace ID", `{" pgbench":{"enabled":true,"image":"runner:v2"}}`, "driver IDs"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("BENCHMARK_DRIVERS", test.payload)
			t.Setenv("RUNNER_IMAGE", "usable-legacy:latest")
			t.Setenv("RUNNER_IMAGE_PULL_POLICY", "IfNotPresent")
			registry, err := benchmarkRegistryFromEnv()
			if err == nil || registry != nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("registry = %v, error = %v; want error containing %q", registry, err, test.message)
			}
		})
	}
}

func TestDriverConfigurationSupportsSysbench(t *testing.T) {
	for _, test := range []struct {
		name, payload string
		want          map[string]driver.RunnerConfig
	}{
		{
			name: "both drivers",
			payload: `{"pgbench":{"enabled":true,"image":"example.com/pgbench:v1","imagePullPolicy":"Always"},` +
				`"sysbench":{"enabled":true,"image":"example.com/sysbench:v2","imagePullPolicy":"Never"}}`,
			want: map[string]driver.RunnerConfig{
				"pgbench":  {Image: "example.com/pgbench:v1", ImagePullPolicy: "Always"},
				"sysbench": {Image: "example.com/sysbench:v2", ImagePullPolicy: "Never"},
			},
		},
		{
			name: "sysbench only", payload: `{"sysbench":{"enabled":true,"image":"example.com/sysbench:v2"}}`,
			want: map[string]driver.RunnerConfig{"sysbench": {Image: "example.com/sysbench:v2", ImagePullPolicy: "IfNotPresent"}},
		},
		{
			name: "pgbench explicitly disabled", payload: `{"pgbench":{"enabled":false},"sysbench":{"enabled":true,"image":"example.com/sysbench:v2"}}`,
			want: map[string]driver.RunnerConfig{"sysbench": {Image: "example.com/sysbench:v2", ImagePullPolicy: "IfNotPresent"}},
		},
		{
			name: "sysbench explicitly disabled", payload: `{"pgbench":{"enabled":true,"image":"example.com/pgbench:v1"},"sysbench":{"enabled":false}}`,
			want: map[string]driver.RunnerConfig{"pgbench": {Image: "example.com/pgbench:v1", ImagePullPolicy: "IfNotPresent"}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("BENCHMARK_DRIVERS", test.payload)
			// A present payload must not inherit any legacy pgbench image settings.
			t.Setenv("RUNNER_IMAGE", "legacy:old")
			t.Setenv("RUNNER_IMAGE_PULL_POLICY", "invalid-legacy-policy")
			registry, err := benchmarkRegistryFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			for id, databaseType := range map[string]string{"pgbench": "postgresql", "sysbench": "mysql"} {
				registration, err := registry.Resolve(id, driver.TargetInfo{DatabaseType: databaseType, Provider: "unfamiliar-provider"})
				want, enabled := test.want[id]
				if !enabled {
					var problem *driver.Error
					if !errors.As(err, &problem) || problem.Kind != driver.DriverSelection {
						t.Fatalf("unconfigured driver %s resolved: %v", id, err)
					}
					continue
				}
				if err != nil || registration.Runner != want || registration.Driver.ID() != id {
					t.Fatalf("driver %s configuration = %+v, error = %v; want %+v", id, registration.Runner, err, want)
				}
				wrongType := "mysql"
				if databaseType == "mysql" {
					wrongType = "postgresql"
				}
				_, err = registry.Resolve(id, driver.TargetInfo{DatabaseType: wrongType})
				var problem *driver.Error
				if !errors.As(err, &problem) || problem.Kind != driver.UnsupportedConnection {
					t.Fatalf("driver %s accepted an unsupported database type: %v", id, err)
				}
			}
		})
	}
}
