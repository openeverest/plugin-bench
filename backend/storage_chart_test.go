package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func storageEnv(deployment appsv1.Deployment) map[string]corev1.EnvVar {
	env := make(map[string]corev1.EnvVar)
	for _, variable := range deployment.Spec.Template.Spec.Containers[0].Env {
		if strings.HasPrefix(variable.Name, "BENCHMARK_STORAGE") {
			env[variable.Name] = variable
		}
	}
	return env
}

func TestChartStorageDefaultsToMemoryWithRecreateStrategy(t *testing.T) {
	deployment := renderDriverDeployment(t, filepath.Join("..", "charts", "plugin-bench"))
	env := storageEnv(deployment)
	if len(env) != 1 || env["BENCHMARK_STORAGE"].Value != "memory" {
		t.Fatalf("storage env = %+v; want memory only", env)
	}
	if deployment.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatalf("strategy = %q; the old backend must stop before the new one recovers runs", deployment.Spec.Strategy.Type)
	}
}

func TestChartStorageReadsPostgreSQLURIFromSecret(t *testing.T) {
	deployment := renderDriverDeployment(t, filepath.Join("..", "charts", "plugin-bench"),
		"--set", "storage.type=postgresql", "--set", "storage.postgresql.existingSecret=bench-history",
		"--set", "storage.postgresql.uriKey=connection")
	env := storageEnv(deployment)
	uri := env["BENCHMARK_STORAGE_POSTGRES_URI"]
	if env["BENCHMARK_STORAGE"].Value != "postgresql" || uri.Value != "" || uri.ValueFrom == nil || uri.ValueFrom.SecretKeyRef == nil ||
		uri.ValueFrom.SecretKeyRef.Name != "bench-history" || uri.ValueFrom.SecretKeyRef.Key != "connection" {
		t.Fatalf("storage env = %+v; want the URI only through a Secret reference", env)
	}
}

func TestChartRejectsInvalidStorageAndReplicaSettings(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("Helm is required for chart rendering checks")
	}
	for _, test := range []struct{ setting, want string }{
		{"storage.type=postgresql", "storage.postgresql.existingSecret is required"},
		{"storage.type=sqlite", "storage.type must be memory or postgresql"},
		{"replicaCount=2", "replicaCount must be 1"},
	} {
		output, err := exec.Command(helm, "template", "storage-test", filepath.Join("..", "charts", "plugin-bench"),
			"--show-only", "templates/deployment.yaml", "--set", test.setting).CombinedOutput()
		if err == nil || !strings.Contains(string(output), test.want) {
			t.Fatalf("%s: err = %v, output = %s; want %q", test.setting, err, output, test.want)
		}
	}
}
