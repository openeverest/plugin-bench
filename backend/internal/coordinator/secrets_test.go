package coordinator

import (
	"strings"
	"testing"
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
