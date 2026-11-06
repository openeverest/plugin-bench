package coordinator

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestRunLabelsRunnerResourcesWithRunID(t *testing.T) {
	client := fake.NewSimpleClientset()
	var secretLabels, jobLabels, podLabels map[string]string
	client.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		secret := action.(ktesting.CreateAction).GetObject().(*corev1.Secret)
		secretLabels, secret.UID = secret.Labels, "secret-uid"
		return false, nil, nil
	})
	creationFailure := errors.New("stop after Job creation")
	client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		job := action.(ktesting.CreateAction).GetObject().(*batchv1.Job)
		jobLabels, podLabels = job.Labels, job.Spec.Template.Labels
		return true, nil, creationFailure
	})
	c, err := New(validConfig(), client)
	if err != nil {
		t.Fatal(err)
	}
	input := validRunInput()
	input.RunID = "0123456789abcdef"
	if _, err := c.Run(context.Background(), input); !errors.Is(err, creationFailure) {
		t.Fatalf("Run error = %v", err)
	}
	want := map[string]string{managedByLabel: managedByValue, instanceLabel: "plugin-bench", RunIDLabel: "0123456789abcdef", processLabel: c.processID}
	for name, got := range map[string]map[string]string{"Secret": secretLabels, "Job": jobLabels, "Pod template": podLabels} {
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s labels = %v, want %v", name, got, want)
		}
	}
}

func TestRunRejectsRunIDThatIsNotALabelValue(t *testing.T) {
	client := fake.NewSimpleClientset()
	c, err := New(validConfig(), client)
	if err != nil {
		t.Fatal(err)
	}
	input := validRunInput()
	input.RunID = "not a label/value"
	if _, err := c.Run(context.Background(), input); err == nil {
		t.Fatal("accepted an invalid run ID")
	}
	if len(client.Actions()) != 0 {
		t.Fatalf("invalid run ID reached Kubernetes: %v", client.Actions())
	}
}

func managedObjectMeta(namespace, name string, labels map[string]string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Namespace: namespace, Name: name, UID: types.UID("uid-" + name), Labels: labels}
}

func installationRunLabels(installation, runID string) map[string]string {
	return map[string]string{managedByLabel: managedByValue, instanceLabel: installation, RunIDLabel: runID}
}

func TestCleanupOrphanedResourcesRemovesOnlyThisInstallationsResources(t *testing.T) {
	namespace := validConfig().WorkloadNamespace
	managed := installationRunLabels(validConfig().InstallationID, "old-run")
	otherInstallation := installationRunLabels("plugin-bench-staging", "active-run")
	client := fake.NewSimpleClientset(
		&batchv1.Job{ObjectMeta: managedObjectMeta(namespace, "orphan-job", managed)},
		&batchv1.Job{ObjectMeta: managedObjectMeta(namespace, "unrelated-job", map[string]string{"app": "other"})},
		&batchv1.Job{ObjectMeta: managedObjectMeta(namespace, "other-installation-job", otherInstallation)},
		&batchv1.Job{ObjectMeta: managedObjectMeta("elsewhere", "other-namespace-job", managed)},
		&corev1.Secret{ObjectMeta: managedObjectMeta(namespace, "orphan-secret", managed)},
		&corev1.Secret{ObjectMeta: managedObjectMeta(namespace, "legacy-unlabeled-secret", nil)},
		&corev1.Secret{ObjectMeta: managedObjectMeta(namespace, "other-installation-secret", otherInstallation)},
		&corev1.Secret{ObjectMeta: managedObjectMeta("elsewhere", "other-namespace-secret", managed)},
	)
	c, err := New(validConfig(), client)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := c.CleanupOrphanedResources(context.Background())
	if err != nil || cleanup != (OrphanCleanup{Jobs: 1, Secrets: 1}) {
		t.Fatalf("cleanup = %+v, %v", cleanup, err)
	}
	remainingJobs := names(t, client, "jobs")
	remainingSecrets := names(t, client, "secrets")
	if !reflect.DeepEqual(remainingJobs, []string{"other-installation-job", "other-namespace-job", "unrelated-job"}) ||
		!reflect.DeepEqual(remainingSecrets, []string{"legacy-unlabeled-secret", "other-installation-secret", "other-namespace-secret"}) {
		t.Fatalf("remaining Jobs = %v, Secrets = %v", remainingJobs, remainingSecrets)
	}
	for _, action := range client.Actions() {
		if deletion, ok := action.(ktesting.DeleteAction); ok && deletion.GetResource().Resource == "jobs" {
			if policy := deletion.GetDeleteOptions().PropagationPolicy; policy == nil || *policy != metav1.DeletePropagationBackground {
				t.Fatal("orphaned Job deletion must not block startup on Pod termination")
			}
		}
	}
}

// Cleanup must be safe to retry after the backend accepts runs, so it may
// remove only resources created by an earlier process of this installation.
func TestCleanupOrphanedResourcesKeepsCurrentProcessResources(t *testing.T) {
	namespace := validConfig().WorkloadNamespace
	client := fake.NewSimpleClientset()
	c, err := New(validConfig(), client)
	if err != nil {
		t.Fatal(err)
	}
	previousProcess := c.runLabels("old-run")
	previousProcess[processLabel] = "previous-process"
	ctx := context.Background()
	for _, object := range []struct {
		name   string
		labels map[string]string
	}{
		{"active", c.runLabels("active-run")},
		{"orphan", previousProcess},
	} {
		if _, err := client.BatchV1().Jobs(namespace).Create(ctx, &batchv1.Job{ObjectMeta: managedObjectMeta(namespace, object.name+"-job", object.labels)}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := client.CoreV1().Secrets(namespace).Create(ctx, &corev1.Secret{ObjectMeta: managedObjectMeta(namespace, object.name+"-secret", object.labels)}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	cleanup, err := c.CleanupOrphanedResources(ctx)
	if err != nil || cleanup != (OrphanCleanup{Jobs: 1, Secrets: 1}) {
		t.Fatalf("cleanup = %+v, %v", cleanup, err)
	}
	if jobs, secrets := names(t, client, "jobs"), names(t, client, "secrets"); !reflect.DeepEqual(jobs, []string{"active-job"}) ||
		!reflect.DeepEqual(secrets, []string{"active-secret"}) {
		t.Fatalf("remaining Jobs = %v, Secrets = %v; want only the current process's resources", jobs, secrets)
	}
}

func TestCleanupOrphanedResourcesContinuesAfterFailures(t *testing.T) {
	namespace := validConfig().WorkloadNamespace
	client := fake.NewSimpleClientset(
		&batchv1.Job{ObjectMeta: managedObjectMeta(namespace, "orphan-job", installationRunLabels(validConfig().InstallationID, "run"))},
		&corev1.Secret{ObjectMeta: managedObjectMeta(namespace, "orphan-secret", installationRunLabels(validConfig().InstallationID, "run"))},
	)
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "jobs"}, "", errors.New("no list"))
	client.PrependReactor("list", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, forbidden
	})
	c, err := New(validConfig(), client)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := c.CleanupOrphanedResources(context.Background())
	if !errors.Is(err, forbidden) || cleanup != (OrphanCleanup{Secrets: 1}) {
		t.Fatalf("cleanup = %+v, %v; want the Secret removed and the Job failure reported", cleanup, err)
	}
}

func names(t *testing.T, client *fake.Clientset, resource string) []string {
	t.Helper()
	var result []string
	switch resource {
	case "jobs":
		list, err := client.BatchV1().Jobs("").List(context.Background(), metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range list.Items {
			result = append(result, item.Name)
		}
	case "secrets":
		list, err := client.CoreV1().Secrets("").List(context.Background(), metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range list.Items {
			result = append(result, item.Name)
		}
	}
	slices.Sort(result)
	return result
}
