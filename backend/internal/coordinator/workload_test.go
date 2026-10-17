package coordinator

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// A fake tool's contract deliberately has no PostgreSQL environment names.
func validRunInput() RunInput {
	return RunInput{
		Runner: driver.RunnerConfig{Image: "example.com/benchmark-tool:v1", ImagePullPolicy: "IfNotPresent"},
		Spec: driver.RunnerSpec{
			Env:        map[string]string{"WORKLOAD_DURATION": "60", "WORKLOAD_WORKERS": "1"},
			SecretData: map[string][]byte{"token": []byte("sensitive-test-password")},
			SecretEnv:  map[string]string{"AUTH_TOKEN": "token"},
		},
	}
}

func TestRunRejectsInvalidPreparedWorkloadBeforeWrites(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*RunInput)
	}{
		{"missing image", func(i *RunInput) { i.Runner.Image = "" }},
		{"pull policy", func(i *RunInput) { i.Runner.ImagePullPolicy = "sometimes" }},
		{"invalid env name", func(i *RunInput) { i.Spec.Env["invalid/key-credential-sentinel"] = "value" }},
		{"invalid secret env name", func(i *RunInput) { i.Spec.SecretEnv["invalid/key-credential-sentinel"] = "token" }},
		{"invalid secret key", func(i *RunInput) { i.Spec.SecretData["invalid/key-credential-sentinel"] = []byte("secret") }},
		{"missing secret reference", func(i *RunInput) { i.Spec.SecretEnv["AUTH_TOKEN"] = "missing" }},
		{"duplicate env", func(i *RunInput) { i.Spec.Env["AUTH_TOKEN"] = "plain" }},
		{"NUL env", func(i *RunInput) { i.Spec.Env["MODE"] = "credential-sentinel\x00" }},
		{"NUL secret env", func(i *RunInput) { i.Spec.SecretData["token"] = []byte("credential-sentinel\x00") }},
		{"NUL argument", func(i *RunInput) { i.Spec.Args = []string{"credential-sentinel\x00"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := validRunInput()
			test.mutate(&input)
			client := fake.NewSimpleClientset()
			c, err := New(validConfig(), client)
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Run(context.Background(), input)
			if err == nil || strings.Contains(err.Error(), "credential-sentinel") || len(client.Actions()) != 0 {
				t.Fatalf("invalid workload caused writes or an unsafe error: %v", err)
			}
		})
	}
}

func TestCreateResourcesOwnsPreparedWorkloadBeforeFirstWrite(t *testing.T) {
	input := validRunInput()
	input.Spec.Command = []string{"tool"}
	input.Spec.Args = []string{"--measure"}
	input.NodeAffinity = validNodeAffinity()
	wantAffinity := input.NodeAffinity.DeepCopy()
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		secret := action.(ktesting.CreateAction).GetObject().(*corev1.Secret)
		secret.UID = "secret-uid"
		input.Spec.Env["WORKLOAD_DURATION"] = "changed"
		input.Spec.SecretData["token"][0] = 'X'
		input.Spec.SecretEnv["AUTH_TOKEN"] = "changed"
		input.Spec.Command[0] = "changed"
		input.Spec.Args[0] = "changed"
		input.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].Weight = 1
		if string(secret.Data["token"]) != "sensitive-test-password" {
			t.Error("Secret data aliased input")
		}
		return false, nil, nil
	})
	client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		job := action.(ktesting.CreateAction).GetObject().(*batchv1.Job)
		container := job.Spec.Template.Spec.Containers[0]
		if container.Command[0] != "tool" || container.Args[0] != "--measure" || container.Env[0].ValueFrom.SecretKeyRef.Key != "token" ||
			container.Env[1].Value != "60" || !reflect.DeepEqual(job.Spec.Template.Spec.Affinity.NodeAffinity, wantAffinity) {
			t.Error("Job aliased caller-owned execution input")
		}
		job.UID = "job-uid"
		return false, nil, nil
	})
	c, err := New(validConfig(), client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.createBenchmarkResources(context.Background(), input, nil); err != nil {
		t.Fatal(err)
	}
}
