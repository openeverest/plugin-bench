package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
	appsv1 "k8s.io/api/apps/v1"
	kubeyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// These checks execute Helm rather than duplicating its image-resolution logic.
// Backend-only environments can run startup tests without installing Helm.
func renderDriverDeployment(t *testing.T, chart string, overrides ...string) appsv1.Deployment {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("Helm is required for chart rendering checks")
	}
	args := []string{"template", "driver-config-test", chart, "--show-only", "templates/deployment.yaml"}
	args = append(args, overrides...)
	output, err := exec.Command(helm, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, output)
	}
	data, err := kubeyaml.ToJSON(output)
	if err != nil {
		t.Fatal(err)
	}
	var deployment appsv1.Deployment
	if err := json.Unmarshal(data, &deployment); err != nil {
		t.Fatal(err)
	}
	return deployment
}

func chartAppVersion(t *testing.T, chart string) string {
	t.Helper()
	metadata, err := os.ReadFile(filepath.Join(chart, "Chart.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := kubeyaml.ToJSON(metadata)
	if err != nil {
		t.Fatal(err)
	}
	var version struct{ AppVersion string }
	if err := json.Unmarshal(data, &version); err != nil {
		t.Fatal(err)
	}
	return version.AppVersion
}

func TestChartInstanceDiscoveryPermissions(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("Helm is required for chart rendering checks")
	}
	output, err := exec.Command(helm, "template", "instance-discovery-test", filepath.Join("..", "charts", "plugin-bench"),
		"--show-only", "templates/plugin-cr.yaml").CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, output)
	}
	data, err := kubeyaml.ToJSON(output)
	if err != nil {
		t.Fatal(err)
	}
	var plugin struct {
		Kind string `json:"kind"`
		Spec struct {
			Permissions []struct {
				Verb     string `json:"verb"`
				Resource string `json:"resource"`
			} `json:"permissions"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(data, &plugin); err != nil {
		t.Fatal(err)
	}
	// Names must match OpenEverest RBAC: connection details are the
	// read-connection action on instances, not a separate resource.
	type permissionKey struct{ verb, resource string }
	want := map[permissionKey]bool{
		{"read", "namespaces"}:           false,
		{"read", "instances"}:            false,
		{"read-connection", "instances"}: false,
	}
	if plugin.Kind != "Plugin" || len(plugin.Spec.Permissions) != len(want) {
		t.Fatalf("unexpected Plugin permission shape: %s", output)
	}
	for _, permission := range plugin.Spec.Permissions {
		key := permissionKey{permission.Verb, permission.Resource}
		seen, exists := want[key]
		if !exists || seen {
			t.Fatalf("unexpected or duplicate permission: %+v", permission)
		}
		want[key] = true
	}
}

func TestChartDriverImageResolution(t *testing.T) {
	chart := filepath.Join("..", "charts", "plugin-bench")
	appVersion := chartAppVersion(t, chart)
	for _, test := range []struct {
		name, settings, image, policy string
	}{
		{"defaults", "", "ghcr.io/openeverest/plugin-bench-pgbench:" + appVersion, "IfNotPresent"},
		{"tag only", "drivers.pgbench.image.tag=2.0.0", "ghcr.io/openeverest/plugin-bench-pgbench:2.0.0", "IfNotPresent"},
		{"repository only", "drivers.pgbench.image.repository=example.com/custom", "example.com/custom:" + appVersion, "IfNotPresent"},
		{"legacy repository", "runner.image.repository=example.com/legacy", "example.com/legacy:" + appVersion, "IfNotPresent"},
		{"legacy tag and policy", "runner.image.tag=old,runner.image.pullPolicy=Never", "ghcr.io/openeverest/plugin-bench-pgbench:old", "Never"},
		{"new settings win", "runner.image.repository=old,runner.image.tag=old,runner.image.pullPolicy=Never,drivers.pgbench.image.repository=new,drivers.pgbench.image.tag=new,drivers.pgbench.image.pullPolicy=Always", "new:new", "Always"},
		{"field inheritance", "runner.image.repository=example.com/legacy,runner.image.tag=old,runner.image.pullPolicy=Never,drivers.pgbench.image.tag=new", "example.com/legacy:new", "Never"},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"--set", "runner.nodeSelector.pool=benchmark", "--set-json", `runner.tolerations=[{"key":"dedicated","operator":"Exists"}]`}
			if test.settings != "" {
				args = append(args, "--set", test.settings)
			}
			deployment := renderDriverDeployment(t, chart, args...)
			pod := deployment.Spec.Template.Spec
			if len(pod.NodeSelector) != 0 || len(pod.Tolerations) != 0 || pod.Affinity != nil {
				t.Fatal("runner placement was applied to the backend Pod")
			}
			env := make(map[string]string)
			for _, variable := range pod.Containers[0].Env {
				env[variable.Name] = variable.Value
			}
			if env["RUNNER_NODE_SELECTOR"] != `{"pool":"benchmark"}` || env["RUNNER_TOLERATIONS"] != `[{"key":"dedicated","operator":"Exists"}]` {
				t.Fatal("shared runner placement no longer reaches the backend environment")
			}
			if _, exists := env["RUNNER_IMAGE"]; exists {
				t.Fatal("chart still emits the legacy image environment variable")
			}
			t.Setenv("BENCHMARK_DRIVERS", env["BENCHMARK_DRIVERS"])
			t.Setenv("RUNNER_IMAGE", "unused:legacy")
			registry, err := benchmarkRegistryFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			registration, err := registry.Resolve("pgbench", driver.TargetInfo{DatabaseType: "postgresql"})
			if err != nil || registration.Runner.Image != test.image || registration.Runner.ImagePullPolicy != test.policy {
				t.Fatalf("rendered registration = %+v, %v; want %s / %s", registration.Runner, err, test.image, test.policy)
			}
		})
	}
}

func TestChartSysbenchConfiguration(t *testing.T) {
	chart := filepath.Join("..", "charts", "plugin-bench")
	appVersion := chartAppVersion(t, chart)
	defaultImage := "ghcr.io/openeverest/plugin-bench-sysbench:" + appVersion
	defaultPgbenchImage := "ghcr.io/openeverest/plugin-bench-pgbench:" + appVersion
	for _, test := range []struct {
		name, settings, image, policy string
		pgbenchDisabled               bool
		pgbenchImage                  string
	}{
		{name: "enabled by default", image: defaultImage, policy: "IfNotPresent"},
		{name: "explicit disablement", settings: "drivers.sysbench.enabled=false,drivers.sysbench.image.repository=example.com/unused,drivers.sysbench.image.tag=unused"},
		{name: "explicit enablement", settings: "drivers.sysbench.enabled=true", image: defaultImage, policy: "IfNotPresent"},
		{name: "tag only", settings: "drivers.sysbench.enabled=true,drivers.sysbench.image.tag=2.0.0", image: "ghcr.io/openeverest/plugin-bench-sysbench:2.0.0", policy: "IfNotPresent"},
		{name: "repository only", settings: "drivers.sysbench.enabled=true,drivers.sysbench.image.repository=example.com/custom-sysbench", image: "example.com/custom-sysbench:" + appVersion, policy: "IfNotPresent"},
		{name: "pull policy only", settings: "drivers.sysbench.enabled=true,drivers.sysbench.image.pullPolicy=Always", image: defaultImage, policy: "Always"},
		{name: "all image overrides", settings: "drivers.sysbench.enabled=true,drivers.sysbench.image.repository=local/sysbench,drivers.sysbench.image.tag=dev,drivers.sysbench.image.pullPolicy=Never", image: "local/sysbench:dev", policy: "Never"},
		{name: "legacy overrides stay with pgbench", settings: "drivers.sysbench.enabled=true,runner.image.repository=example.com/legacy,runner.image.tag=old,runner.image.pullPolicy=Never", image: defaultImage, policy: "IfNotPresent", pgbenchImage: "example.com/legacy:old"},
		{name: "sysbench only", settings: "drivers.sysbench.enabled=true,drivers.pgbench.enabled=false,drivers.go-ycsb.enabled=false", image: defaultImage, policy: "IfNotPresent", pgbenchDisabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var args []string
			if test.settings != "" {
				args = []string{"--set", test.settings}
			}
			deployment := renderDriverDeployment(t, chart, args...)
			var payload string
			for _, variable := range deployment.Spec.Template.Spec.Containers[0].Env {
				if variable.Name == "BENCHMARK_DRIVERS" {
					payload = variable.Value
				}
			}
			t.Setenv("BENCHMARK_DRIVERS", payload)
			t.Setenv("RUNNER_IMAGE", "unused:legacy")
			registry, err := benchmarkRegistryFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			registration, err := registry.Resolve("sysbench", driver.TargetInfo{DatabaseType: "mysql"})
			if test.image == "" {
				var selectionError *driver.Error
				if !errors.As(err, &selectionError) || selectionError.Kind != driver.DriverSelection {
					t.Fatalf("disabled sysbench resolution = %v; want driver selection error", err)
				}
			} else if err != nil || registration.Runner.Image != test.image || registration.Runner.ImagePullPolicy != test.policy {
				t.Fatalf("sysbench registration = %+v, %v; want %s / %s", registration.Runner, err, test.image, test.policy)
			}
			pgbench, err := registry.Resolve("pgbench", driver.TargetInfo{DatabaseType: "postgresql"})
			if test.pgbenchDisabled {
				var selectionError *driver.Error
				if !errors.As(err, &selectionError) || selectionError.Kind != driver.DriverSelection {
					t.Fatalf("disabled pgbench resolution = %v; want driver selection error", err)
				}
				return
			}
			wantImage, wantPolicy := defaultPgbenchImage, "IfNotPresent"
			if test.pgbenchImage != "" {
				wantImage, wantPolicy = test.pgbenchImage, "Never"
			}
			if err != nil || pgbench.Runner.Image != wantImage || pgbench.Runner.ImagePullPolicy != wantPolicy {
				t.Fatalf("pgbench registration = %+v, %v; want %s / %s", pgbench.Runner, err, wantImage, wantPolicy)
			}
		})
	}
}

func TestChartGoYCSBConfiguration(t *testing.T) {
	chart := filepath.Join("..", "charts", "plugin-bench")
	appVersion := chartAppVersion(t, chart)
	defaultImage := "ghcr.io/openeverest/plugin-bench-go-ycsb:" + appVersion
	for _, test := range []struct {
		name, settings, image, policy string
		onlyGoYCSB                    bool
	}{
		{name: "enabled by default", image: defaultImage, policy: "IfNotPresent"},
		{name: "explicit enablement", settings: "drivers.go-ycsb.enabled=true", image: defaultImage, policy: "IfNotPresent"},
		{name: "tag only", settings: "drivers.go-ycsb.image.tag=2.0.0", image: "ghcr.io/openeverest/plugin-bench-go-ycsb:2.0.0", policy: "IfNotPresent"},
		{name: "repository only", settings: "drivers.go-ycsb.image.repository=example.com/custom-ycsb", image: "example.com/custom-ycsb:" + appVersion, policy: "IfNotPresent"},
		{name: "all image overrides", settings: "drivers.go-ycsb.image.repository=local/go-ycsb,drivers.go-ycsb.image.tag=dev,drivers.go-ycsb.image.pullPolicy=Never", image: "local/go-ycsb:dev", policy: "Never"},
		{name: "disabled with unused image settings", settings: "drivers.go-ycsb.enabled=false,drivers.go-ycsb.image.repository=unused,drivers.go-ycsb.image.tag=unused"},
		{name: "legacy overrides do not affect go-ycsb", settings: "drivers.go-ycsb.enabled=true,runner.image.repository=example.com/legacy,runner.image.tag=old,runner.image.pullPolicy=Always", image: defaultImage, policy: "IfNotPresent"},
		{name: "go-ycsb only", settings: "drivers.go-ycsb.enabled=true,drivers.pgbench.enabled=false,drivers.sysbench.enabled=false", image: defaultImage, policy: "IfNotPresent", onlyGoYCSB: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var args []string
			if test.settings != "" {
				args = []string{"--set", test.settings}
			}
			deployment := renderDriverDeployment(t, chart, args...)
			var payload string
			for _, variable := range deployment.Spec.Template.Spec.Containers[0].Env {
				if variable.Name == "BENCHMARK_DRIVERS" {
					payload = variable.Value
				}
			}
			t.Setenv("BENCHMARK_DRIVERS", payload)
			registry, err := benchmarkRegistryFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			wantIDs := []string{"pgbench", "sysbench"}
			if test.image != "" {
				wantIDs = []string{"go-ycsb", "pgbench", "sysbench"}
			}
			if test.onlyGoYCSB {
				wantIDs = []string{"go-ycsb"}
			}
			if !slices.Equal(registry.IDs(), wantIDs) {
				t.Fatalf("enabled drivers = %v; want %v", registry.IDs(), wantIDs)
			}
			registration, err := registry.Resolve("go-ycsb", driver.TargetInfo{DatabaseType: "mongodb"})
			if test.image == "" {
				var selectionError *driver.Error
				if !errors.As(err, &selectionError) || selectionError.Kind != driver.DriverSelection {
					t.Fatalf("disabled go-ycsb resolution = %v; want driver selection error", err)
				}
			} else if err != nil || registration.Runner.Image != test.image || registration.Runner.ImagePullPolicy != test.policy {
				t.Fatalf("go-ycsb registration = %+v, %v; want %s / %s", registration.Runner, err, test.image, test.policy)
			}
		})
	}
}

func TestChartDriverDisablementAndMultipleEntries(t *testing.T) {
	deployment := renderDriverDeployment(t, filepath.Join("..", "charts", "plugin-bench"),
		"--set", "drivers.pgbench.enabled=false,drivers.sysbench.enabled=false,drivers.go-ycsb.enabled=false,runner.image.repository=legacy,runner.image.tag=old,drivers.future.enabled=true,drivers.future.image.tag=2.0.0")
	var payload string
	for _, variable := range deployment.Spec.Template.Spec.Containers[0].Env {
		if variable.Name == "BENCHMARK_DRIVERS" {
			payload = variable.Value
		}
	}
	var entries map[string]driverDeploymentConfig
	if err := json.Unmarshal([]byte(payload), &entries); err != nil {
		t.Fatal(err)
	}
	pgbench, sysbench, goycsb, future := entries["pgbench"], entries["sysbench"], entries["go-ycsb"], entries["future"]
	if len(entries) != 4 || pgbench.Enabled == nil || *pgbench.Enabled || pgbench.Image != "" ||
		sysbench.Enabled == nil || *sysbench.Enabled || sysbench.Image != "" || sysbench.ImagePullPolicy != "" ||
		goycsb.Enabled == nil || *goycsb.Enabled || goycsb.Image != "" || goycsb.ImagePullPolicy != "" ||
		future.Enabled == nil || !*future.Enabled || future.Image != "ghcr.io/openeverest/plugin-bench-future:2.0.0" || future.ImagePullPolicy != "IfNotPresent" {
		t.Fatalf("wrong rendered driver entries: %s", payload)
	}
	t.Setenv("BENCHMARK_DRIVERS", payload)
	t.Setenv("RUNNER_IMAGE", "usable:legacy")
	if _, err := benchmarkRegistryFromEnv(); err == nil || !strings.Contains(err.Error(), "not compiled into the backend") {
		t.Fatalf("Helm configuration must not install unknown driver implementations: %v", err)
	}
}

func TestChartRejectsScalarImageSettings(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("Helm is required for chart rendering checks")
	}
	chart := filepath.Join("..", "charts", "plugin-bench")
	for _, test := range []struct{ name, setting, message string }{
		{"driver image", "drivers.pgbench.image=ghcr.io/example/pgbench:1.0", "drivers.pgbench.image must be an object"},
		{"sysbench image", "drivers.sysbench.image=ghcr.io/example/sysbench:1.0", "drivers.sysbench.image must be an object"},
		{"go-ycsb image", "drivers.go-ycsb.image=ghcr.io/example/go-ycsb:1.0", "drivers.go-ycsb.image must be an object"},
		{"legacy image", "runner.image=ghcr.io/example/pgbench:1.0", "runner.image must be an object"},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := exec.Command(helm, "template", "driver-config-test", chart,
				"--show-only", "templates/deployment.yaml", "--set-string", test.setting).CombinedOutput()
			if err == nil || !strings.Contains(string(output), test.message) {
				t.Fatalf("expected %q rendering error, got %v\n%s", test.message, err, output)
			}
		})
	}
}

func TestPackagedChartUsesReleaseAppVersionForImages(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("Helm is required for chart packaging checks")
	}
	const releaseVersion = "9.8.7"
	destination := t.TempDir()
	output, err := exec.Command(helm, "package", filepath.Join("..", "charts", "plugin-bench"),
		"--version", releaseVersion, "--app-version", releaseVersion, "--destination", destination).CombinedOutput()
	if err != nil {
		t.Fatalf("helm package: %v\n%s", err, output)
	}
	archive := filepath.Join(destination, "plugin-bench-"+releaseVersion+".tgz")
	for _, test := range []struct{ name, tag string }{
		{"release default", releaseVersion},
		{"tag override", "runner-override"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var args []string
			if test.tag != releaseVersion {
				args = append(args, "--set-string", "drivers.pgbench.image.tag="+test.tag+",drivers.sysbench.image.tag="+test.tag+",drivers.go-ycsb.image.tag="+test.tag)
			}
			deployment := renderDriverDeployment(t, archive, args...)
			container := deployment.Spec.Template.Spec.Containers[0]
			if container.Image != "ghcr.io/openeverest/plugin-bench:"+releaseVersion {
				t.Fatalf("packaged backend image = %q", container.Image)
			}
			var payload string
			for _, variable := range container.Env {
				if variable.Name == "BENCHMARK_DRIVERS" {
					payload = variable.Value
				}
			}
			t.Setenv("BENCHMARK_DRIVERS", payload)
			registry, err := benchmarkRegistryFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			for _, target := range []struct{ id, databaseType string }{{"pgbench", "postgresql"}, {"sysbench", "mysql"}, {"go-ycsb", "mongodb"}} {
				registration, err := registry.Resolve(target.id, driver.TargetInfo{DatabaseType: target.databaseType})
				wantImage := "ghcr.io/openeverest/plugin-bench-" + target.id + ":" + test.tag
				if err != nil || registration.Runner.Image != wantImage {
					t.Fatalf("packaged %s runner image = %q, error = %v; want %s", target.id, registration.Runner.Image, err, wantImage)
				}
			}
		})
	}
}
