package coordinator

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestNewCredentialSecretRejectsInvalidIdentity(t *testing.T) {
	for _, identity := range [][2]string{{" ", "credentials"}, {"workloads", " "}} {
		secret, err := newCredentialSecret(identity[0], identity[1], validRunInput().Spec.SecretData, nil)
		if err == nil || secret != nil || strings.Contains(err.Error(), "sensitive-test-password") {
			t.Fatalf("invalid identity must return a safe error: %v", err)
		}
	}
}

func TestNewCredentialSecretOwnsGenericData(t *testing.T) {
	data := map[string][]byte{"token": []byte("secret"), "config.json": []byte("{}")}
	secret, err := newCredentialSecret("plugin-bench-workloads", "credentials", data, nil)
	if err != nil {
		t.Fatal(err)
	}
	data["token"][0] = 'X'
	delete(data, "config.json")
	if secret.Namespace != "plugin-bench-workloads" || secret.Name != "credentials" || secret.Type != corev1.SecretTypeOpaque ||
		string(secret.Data["token"]) != "secret" || string(secret.Data["config.json"]) != "{}" {
		t.Fatal("Secret did not retain an independent copy of arbitrary driver data")
	}
}

func TestCreateCredentialSecret(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		action.(ktesting.CreateAction).GetObject().(*corev1.Secret).UID = types.UID("uid-run-1")
		return false, nil, nil
	})
	coordinator, err := New(validConfig(), client)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := coordinator.createCredentialSecret(context.Background(), validRunInput().Spec.SecretData, map[string]string{"plugin-bench/run-id": "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(reference.Name, credentialSecretNamePrefix) || reference.UID == "" {
		t.Fatalf("SecretRef = %+v, want generated name and UID", reference)
	}
	created, err := client.CoreV1().Secrets("plugin-bench-workloads").Get(context.Background(), reference.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(created.Data["token"]) != "sensitive-test-password" || created.Labels["plugin-bench/run-id"] != "run-1" {
		t.Fatal("Secret did not retain generic data and labels")
	}
}

func TestCreateCredentialSecretRejectsCanceledContext(t *testing.T) {
	client := fake.NewSimpleClientset()
	coordinator, err := New(validConfig(), client)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = coordinator.createCredentialSecret(ctx, validRunInput().Spec.SecretData, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	secrets, err := client.CoreV1().Secrets("plugin-bench-workloads").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(secrets.Items) != 0 {
		t.Fatalf("created %d Secret(s) after cancellation, want none", len(secrets.Items))
	}
}

func TestCreateCredentialSecretDoesNotExposeCredentialsOnAPIError(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("Kubernetes API unavailable")
	})
	coordinator, err := New(validConfig(), client)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = coordinator.createCredentialSecret(context.Background(), validRunInput().Spec.SecretData, nil)
	if err == nil || !strings.Contains(err.Error(), "Kubernetes API unavailable") {
		t.Fatalf("error = %v, want API error", err)
	}
	if strings.Contains(err.Error(), "sensitive-test-password") {
		t.Fatal("API error exposed database credentials")
	}
}

func TestDeleteCredentialSecret(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "credentials-run-1", Namespace: "plugin-bench-workloads", UID: "secret-uid-1"},
	})
	coordinator, err := New(validConfig(), client)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	client.PrependReactor("delete", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		options := action.(ktesting.DeleteAction).GetDeleteOptions()
		if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != "secret-uid-1" {
			t.Fatal("deletion must specify the recorded Secret UID")
		}
		return false, nil, nil
	})
	if err := coordinator.deleteCredentialSecret(context.Background(), SecretRef{Name: "credentials-run-1", UID: "secret-uid-1"}); err != nil {
		t.Fatalf("deleteCredentialSecret() error = %v", err)
	}
	_, err = client.CoreV1().Secrets(validConfig().WorkloadNamespace).Get(context.Background(), "credentials-run-1", metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("Get() error = %v, want NotFound", err)
	}
}

func TestDeleteCredentialSecretRequiresUID(t *testing.T) {
	client := fake.NewSimpleClientset()
	c, err := New(validConfig(), client)
	if err != nil {
		t.Fatal(err)
	}
	err = c.deleteCredentialSecret(context.Background(), SecretRef{Name: "credentials"})
	if err == nil || len(client.Actions()) != 0 {
		t.Fatal("missing UID must prevent deletion")
	}
}

func TestDeleteCredentialSecretAlreadyAbsent(t *testing.T) {
	c, err := New(validConfig(), fake.NewSimpleClientset())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.deleteCredentialSecret(context.Background(), SecretRef{Name: "credentials", UID: "original"}); err != nil {
		t.Fatalf("absent Secret should count as cleaned up: %v", err)
	}
}
