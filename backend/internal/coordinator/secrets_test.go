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

func TestNewCredentialSecretRejectsInvalidInput(t *testing.T) {
	for _, field := range []string{"namespace", "name", "host", "port", "database", "username", "password"} {
		t.Run(field, func(t *testing.T) {
			namespace, name := "workloads", "credentials"
			connection := Connection{Host: "postgres", Port: 5432, Database: "bench", Username: "postgres", Password: "sensitive-test-password"}
			switch field {
			case "namespace":
				namespace = " "
			case "name":
				name = " "
			case "host":
				connection.Host = ""
			case "port":
				connection.Port = 65536
			case "database":
				connection.Database = ""
			case "username":
				connection.Username = ""
			case "password":
				connection.Password = ""
			}
			secret, err := newCredentialSecret(namespace, name, connection, nil)
			if err == nil || secret != nil {
				t.Fatal("invalid input must return an error and no Secret")
			}
			if strings.Contains(err.Error(), "sensitive-test-password") {
				t.Fatal("validation error exposed credentials")
			}
		})
	}
}

func TestNewCredentialSecretBuildsConnectionData(t *testing.T) {
	connection := Connection{
		Host:     "postgres.example.com",
		Port:     5432,
		Database: "pgbench",
		Username: "postgres",
		Password: "secret",
	}

	secret, err := newCredentialSecret("plugin-bench-workloads", "pgbench-secret-1", connection, nil)
	if err != nil {
		t.Fatalf("newCredentialSecret() error = %v", err)
	}
	if secret.Namespace != "plugin-bench-workloads" || secret.Name != "pgbench-secret-1" {
		t.Fatalf("secret identity = %s/%s", secret.Namespace, secret.Name)
	}
	if secret.Type != "Opaque" {
		t.Fatalf("secret type = %q", secret.Type)
	}
	if string(secret.Data[secretKeyHost]) != connection.Host ||
		string(secret.Data[secretKeyPort]) != "5432" ||
		string(secret.Data[secretKeyDatabase]) != connection.Database ||
		string(secret.Data[secretKeyUser]) != connection.Username ||
		string(secret.Data[secretKeyPassword]) != connection.Password {
		t.Fatal("secret does not contain the expected connection data")
	}
}

func TestCreateCredentialSecret(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		secret := action.(ktesting.CreateAction).GetObject().(*corev1.Secret)
		secret.UID = types.UID("uid-run-1")
		return false, nil, nil
	})
	coordinator, err := New(validConfig(), client)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	connection := Connection{
		Host:     "postgres.example.com",
		Port:     5432,
		Database: "pgbench",
		Username: "postgres",
		Password: "sensitive-test-password",
	}

	reference, err := coordinator.createCredentialSecret(context.Background(), connection, map[string]string{"plugin-bench/run-id": "run-1"})
	if err != nil {
		t.Fatalf("createCredentialSecret() error = %v", err)
	}
	if !strings.HasPrefix(reference.Name, credentialSecretNamePrefix) || reference.UID == "" {
		t.Fatalf("SecretRef = %+v, want generated name and UID", reference)
	}
	created, err := client.CoreV1().Secrets("plugin-bench-workloads").Get(context.Background(), reference.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if string(created.Data[secretKeyPassword]) != connection.Password {
		t.Fatal("created Secret does not contain the password")
	}
	if created.Labels["plugin-bench/run-id"] != "run-1" {
		t.Fatal("created Secret did not retain run labels")
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

	_, err = coordinator.createCredentialSecret(ctx, validConnection(), nil)
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

	_, err = coordinator.createCredentialSecret(context.Background(), validConnection(), nil)
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

func validConnection() Connection {
	return Connection{
		Host:     "postgres.example.com",
		Port:     5432,
		Database: "pgbench",
		Username: "postgres",
		Password: "sensitive-test-password",
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
