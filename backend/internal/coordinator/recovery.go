package coordinator

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	managedByLabel = "app.kubernetes.io/managed-by"
	managedByValue = "plugin-bench"
	// RunIDLabel links runner Jobs, Pods, and Secrets to their run record.
	RunIDLabel = "benchmark.openeverest.io/run-id"
)

func runLabels(runID string) map[string]string {
	result := map[string]string{managedByLabel: managedByValue}
	if runID != "" {
		result[RunIDLabel] = runID
	}
	return result
}

// OrphanCleanup counts resources removed by CleanupOrphanedResources.
type OrphanCleanup struct {
	Jobs    int
	Secrets int
}

// CleanupOrphanedResources deletes every runner Job and credential Secret
// that a previous backend process left in the workload namespace. A crash
// otherwise leaves Secrets holding target database passwords indefinitely.
// Call it only before accepting runs: it also removes active resources, which
// is safe only while one backend replica owns the workload namespace.
func (c *Coordinator) CleanupOrphanedResources(ctx context.Context) (OrphanCleanup, error) {
	var cleanup OrphanCleanup
	if c == nil {
		return cleanup, errors.New("coordinator is nil")
	}
	selector := labels.Set{managedByLabel: managedByValue}.String()
	namespace := c.config.WorkloadNamespace
	var cleanupErrors []error

	jobs, err := c.kubeClient.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("list orphaned Jobs: %w", err))
	} else {
		// Background propagation lets the garbage collector remove Pods
		// without delaying startup until every Pod has terminated.
		propagation := metav1.DeletePropagationBackground
		for _, job := range jobs.Items {
			err := c.kubeClient.BatchV1().Jobs(namespace).Delete(ctx, job.Name, metav1.DeleteOptions{
				Preconditions: &metav1.Preconditions{UID: &job.UID}, PropagationPolicy: &propagation,
			})
			if err != nil && !apierrors.IsNotFound(err) {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("delete orphaned Job %q: %w", job.Name, err))
				continue
			}
			cleanup.Jobs++
		}
	}

	secrets, err := c.kubeClient.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("list orphaned credential Secrets: %w", err))
	} else {
		for _, secret := range secrets.Items {
			if err := c.deleteCredentialSecret(ctx, SecretRef{Name: secret.Name, UID: secret.UID}); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("delete orphaned credential Secret %q: %w", secret.Name, err))
				continue
			}
			cleanup.Secrets++
		}
	}
	return cleanup, errors.Join(cleanupErrors...)
}
