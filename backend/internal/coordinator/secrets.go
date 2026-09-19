package coordinator

import (
	"errors"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	secretKeyHost     = "PGHOST"
	secretKeyPort     = "PGPORT"
	secretKeyDatabase = "PGDATABASE"
	secretKeyUser     = "PGUSER"
	secretKeyPassword = "PGPASSWORD"
)

// newCredentialSecret builds the per-run credential Secret. It only creates
// an in-memory Kubernetes object; the caller is responsible for API calls and
// cleanup.
func newCredentialSecret(namespace, name string, connection Connection, labels map[string]string) (*corev1.Secret, error) {
	if strings.TrimSpace(namespace) == "" {
		return nil, errors.New("secret namespace is required")
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("secret name is required")
	}
	if err := connection.Validate(); err != nil {
		return nil, err
	}

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    copyLabels(labels),
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			secretKeyHost:     []byte(connection.Host),
			secretKeyPort:     []byte(formatPort(connection.Port)),
			secretKeyDatabase: []byte(connection.Database),
			secretKeyUser:     []byte(connection.Username),
			secretKeyPassword: []byte(connection.Password),
		},
	}, nil
}
