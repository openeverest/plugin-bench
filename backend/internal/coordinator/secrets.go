package coordinator

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	credentialSecretNamePrefix = "plugin-bench-credentials-"
)

// SecretRef identifies the credential Secret created for one benchmark run.
// The UID allows later cleanup to verify the exact resource being deleted.
type SecretRef struct {
	Name string
	UID  types.UID
}

// createCredentialSecret creates the per-run credential Secret in the
// configured workload namespace. It does not log or return the credentials.
func (c *Coordinator) createCredentialSecret(ctx context.Context, data map[string][]byte, labels map[string]string) (SecretRef, error) {
	var reference SecretRef
	if c == nil {
		return reference, errors.New("coordinator is nil")
	}
	if ctx == nil {
		return reference, errors.New("secret creation context is nil")
	}
	if c.kubeClient == nil {
		return reference, ErrKubernetesClientRequired
	}
	if err := c.config.Validate(); err != nil {
		return reference, fmt.Errorf("invalid coordinator configuration: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return reference, err
	}
	name, err := generateCredentialSecretName()
	if err != nil {
		return reference, err
	}

	secret, err := newCredentialSecret(c.config.WorkloadNamespace, name, data, labels)
	if err != nil {
		return reference, fmt.Errorf("build credential Secret: %w", err)
	}

	created, err := c.kubeClient.CoreV1().Secrets(c.config.WorkloadNamespace).Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		return reference, fmt.Errorf("create credential Secret: %w", err)
	}
	return SecretRef{Name: created.Name, UID: created.UID}, nil
}

// deleteCredentialSecret removes a credential Secret created for a run. A
// missing Secret is already in the desired state and is treated as success.
func (c *Coordinator) deleteCredentialSecret(ctx context.Context, reference SecretRef) error {
	if c == nil {
		return errors.New("coordinator is nil")
	}
	if ctx == nil {
		return errors.New("secret deletion context is nil")
	}
	if c.kubeClient == nil {
		return ErrKubernetesClientRequired
	}
	if err := c.config.Validate(); err != nil {
		return fmt.Errorf("invalid coordinator configuration: %w", err)
	}
	if strings.TrimSpace(reference.Name) == "" {
		return errors.New("credential Secret name is required")
	}
	if reference.UID == "" {
		return errors.New("credential Secret UID is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	err := c.kubeClient.CoreV1().Secrets(c.config.WorkloadNamespace).Delete(ctx, reference.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &reference.UID},
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete credential Secret: %w", err)
	}
	return nil
}

func generateCredentialSecretName() (string, error) {
	var suffix [8]byte
	if _, err := cryptorand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("generate credential Secret name: %w", err)
	}
	return credentialSecretNamePrefix + hex.EncodeToString(suffix[:]), nil
}

// newCredentialSecret builds the per-run credential Secret. It only creates
// an in-memory Kubernetes object; the caller is responsible for API calls and
// cleanup.
func newCredentialSecret(namespace, name string, data map[string][]byte, labels map[string]string) (*corev1.Secret, error) {
	if strings.TrimSpace(namespace) == "" {
		return nil, errors.New("secret namespace is required")
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("secret name is required")
	}
	owned := maps.Clone(data)
	for key, value := range owned {
		owned[key] = slices.Clone(value)
	}

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    copyLabels(labels),
		},
		Type: corev1.SecretTypeOpaque,
		Data: owned,
	}, nil
}
