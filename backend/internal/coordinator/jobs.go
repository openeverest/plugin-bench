package coordinator

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

const (
	runnerContainerName      = "pgbench-runner"
	benchmarkJobNamePrefix   = "plugin-bench-pgbench-"
	credentialCleanupTimeout = 10 * time.Second
	jobPollInterval          = time.Second
)

// JobRef identifies the Job created for one benchmark run.
type JobRef struct {
	Name string
	UID  types.UID
}

// waitForJob polls a created Job until Kubernetes reports completion or
// failure. Log collection and resource cleanup happen in later lifecycle
// steps.
func (c *Coordinator) waitForJob(ctx context.Context, reference JobRef) error {
	return c.waitForJobAtInterval(ctx, reference, jobPollInterval)
}

func (c *Coordinator) waitForJobAtInterval(ctx context.Context, reference JobRef, interval time.Duration) error {
	if c == nil {
		return errors.New("coordinator is nil")
	}
	if ctx == nil {
		return errors.New("job polling context is nil")
	}
	if c.kubeClient == nil {
		return ErrKubernetesClientRequired
	}
	if err := c.config.Validate(); err != nil {
		return fmt.Errorf("invalid coordinator configuration: %w", err)
	}
	if strings.TrimSpace(reference.Name) == "" {
		return errors.New("benchmark Job name is required")
	}
	if interval <= 0 {
		return errors.New("job polling interval must be positive")
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		job, err := c.kubeClient.BatchV1().Jobs(c.config.WorkloadNamespace).Get(ctx, reference.Name, metav1.GetOptions{})
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("get benchmark Job %q: %w", reference.Name, err)
		}
		if reference.UID != "" && job.UID != "" && job.UID != reference.UID {
			return fmt.Errorf("benchmark Job %q UID does not match the created Job", reference.Name)
		}

		for _, condition := range job.Status.Conditions {
			if condition.Status != corev1.ConditionTrue {
				continue
			}
			switch condition.Type {
			case batchv1.JobComplete:
				return nil
			case batchv1.JobFailed:
				return fmt.Errorf("benchmark Job %q failed", reference.Name)
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

type benchmarkResources struct {
	secret SecretRef
	job    JobRef
}

// findJobPod uses the Kubernetes Job label to narrow the search, then checks
// controller ownership so a same-named Job from another run cannot match.
// Multiple owned Pods are an error: choosing one arbitrarily could hide output.
func (c *Coordinator) findJobPod(ctx context.Context, reference JobRef) (*corev1.Pod, error) {
	if c == nil {
		return nil, errors.New("coordinator is nil")
	}
	if ctx == nil {
		return nil, errors.New("Pod discovery context is nil")
	}
	if c.kubeClient == nil {
		return nil, ErrKubernetesClientRequired
	}
	if err := c.config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid coordinator configuration: %w", err)
	}
	if strings.TrimSpace(reference.Name) == "" || reference.UID == "" {
		return nil, errors.New("benchmark Job name and UID are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	pods, err := c.kubeClient.CoreV1().Pods(c.config.WorkloadNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set{batchv1.JobNameLabel: reference.Name}.String(),
	})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil {
		return nil, fmt.Errorf("list Pods for benchmark Job %q: %w", reference.Name, err)
	}
	var found *corev1.Pod
	for i := range pods.Items {
		pod := &pods.Items[i]
		owner := metav1.GetControllerOf(pod)
		if owner == nil || owner.APIVersion != batchv1.SchemeGroupVersion.String() ||
			owner.Kind != "Job" || owner.Name != reference.Name || owner.UID != reference.UID {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("multiple Pods found for benchmark Job %q", reference.Name)
		}
		found = pod
	}
	if found == nil {
		return nil, fmt.Errorf("no Pod found for benchmark Job %q", reference.Name)
	}
	return found, nil
}

// createBenchmarkResources creates the Secret before the Job that consumes
// it. If Job creation fails, the Secret is removed before the error returns.
// Monitoring and normal run cleanup are handled by the execution lifecycle.
func (c *Coordinator) createBenchmarkResources(ctx context.Context, connection Connection, options Options, labels map[string]string) (benchmarkResources, error) {
	var resources benchmarkResources
	if c == nil {
		return resources, errors.New("coordinator is nil")
	}
	if ctx == nil {
		return resources, errors.New("resource creation context is nil")
	}
	if err := c.config.Validate(); err != nil {
		return resources, fmt.Errorf("invalid coordinator configuration: %w", err)
	}
	if err := connection.Validate(); err != nil {
		return resources, fmt.Errorf("invalid database connection: %w", err)
	}
	if err := options.Validate(); err != nil {
		return resources, fmt.Errorf("invalid benchmark options: %w", err)
	}
	if _, err := resourceRequirements(c.config.Resources); err != nil {
		return resources, fmt.Errorf("invalid runner resources: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return resources, err
	}

	secret, err := c.createCredentialSecret(ctx, connection, labels)
	if err != nil {
		return resources, err
	}

	job, err := c.createBenchmarkJob(ctx, secret, options, labels)
	if err == nil {
		return benchmarkResources{secret: secret, job: job}, nil
	}

	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), credentialCleanupTimeout)
	defer cancel()
	cleanupErr := c.deleteCredentialSecret(cleanupCtx, secret)
	if cleanupErr != nil {
		return resources, errors.Join(err, fmt.Errorf("cleanup credential Secret after Job creation failure: %w", cleanupErr))
	}
	return resources, err
}

// createBenchmarkJob builds and creates one runner Job in the configured
// workload namespace. It does not wait for completion or read logs.
func (c *Coordinator) createBenchmarkJob(ctx context.Context, secretRef SecretRef, options Options, labels map[string]string) (JobRef, error) {
	var reference JobRef
	if c == nil {
		return reference, errors.New("coordinator is nil")
	}
	if ctx == nil {
		return reference, errors.New("job creation context is nil")
	}
	if c.kubeClient == nil {
		return reference, ErrKubernetesClientRequired
	}
	if err := c.config.Validate(); err != nil {
		return reference, fmt.Errorf("invalid coordinator configuration: %w", err)
	}
	if strings.TrimSpace(secretRef.Name) == "" {
		return reference, errors.New("credential Secret name is required")
	}
	if err := options.Validate(); err != nil {
		return reference, fmt.Errorf("invalid benchmark options: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return reference, err
	}

	name, err := generateBenchmarkJobName()
	if err != nil {
		return reference, err
	}
	job, err := newBenchmarkJob(c.config, name, secretRef.Name, options, labels)
	if err != nil {
		return reference, fmt.Errorf("build benchmark Job: %w", err)
	}

	created, err := c.kubeClient.BatchV1().Jobs(c.config.WorkloadNamespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return reference, fmt.Errorf("create benchmark Job: %w", err)
	}
	return JobRef{Name: created.Name, UID: created.UID}, nil
}

func generateBenchmarkJobName() (string, error) {
	var suffix [8]byte
	if _, err := cryptorand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("generate benchmark Job name: %w", err)
	}
	return benchmarkJobNamePrefix + hex.EncodeToString(suffix[:]), nil
}

// newBenchmarkJob builds the per-run Job object. It only creates an in-memory
// Kubernetes object; the caller is responsible for API calls and cleanup.
func newBenchmarkJob(config Config, name, secretName string, options Options, labels map[string]string) (*batchv1.Job, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid coordinator configuration: %w", err)
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("job name is required")
	}
	if strings.TrimSpace(secretName) == "" {
		return nil, errors.New("credential secret name is required")
	}
	if err := options.Validate(); err != nil {
		return nil, fmt.Errorf("invalid benchmark options: %w", err)
	}
	resources, err := resourceRequirements(config.Resources)
	if err != nil {
		return nil, err
	}

	backoffLimit := int32(0)
	activeDeadlineSeconds := int64(config.ExecutionTimeout.Seconds())
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: config.WorkloadNamespace,
			Labels:    copyLabels(labels),
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoffLimit,
			ActiveDeadlineSeconds: &activeDeadlineSeconds,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: copyLabels(labels)},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					ServiceAccountName:           config.RunnerServiceAccount,
					AutomountServiceAccountToken: boolPtr(false),
					Containers: []corev1.Container{{
						Name:            runnerContainerName,
						Image:           config.RunnerImage,
						ImagePullPolicy: corev1.PullPolicy(config.ImagePullPolicy),
						Resources:       resources,
						Env:             benchmarkEnvironment(secretName, options),
					}},
				},
			},
		},
	}, nil
}

func benchmarkEnvironment(secretName string, options Options) []corev1.EnvVar {
	// Kubernetes copies each matching field from the temporary Secret into an
	// environment variable in the runner container. The names are identical
	// because the runner reads these exact variables: PGHOST, PGPORT,
	// PGDATABASE, PGUSER, and PGPASSWORD.
	secretKeys := []string{
		secretKeyHost,
		secretKeyPort,
		secretKeyDatabase,
		secretKeyUser,
		secretKeyPassword,
	}
	env := make([]corev1.EnvVar, 0, len(secretKeys)+5)
	for _, key := range secretKeys {
		env = append(env, corev1.EnvVar{
			Name: key,
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
				Key:                  key,
			}},
		})
	}
	env = append(env,
		corev1.EnvVar{Name: "BENCH_DURATION", Value: strconv.Itoa(options.Duration)},
		corev1.EnvVar{Name: "BENCH_CLIENTS", Value: strconv.Itoa(options.Clients)},
		corev1.EnvVar{Name: "BENCH_THREADS", Value: strconv.Itoa(options.Threads)},
		corev1.EnvVar{Name: "BENCH_SCALE", Value: strconv.Itoa(options.Scale)},
		corev1.EnvVar{Name: "BENCH_INITIALIZE", Value: strconv.FormatBool(options.Initialize)},
	)
	return env
}

func resourceRequirements(resources Resources) (corev1.ResourceRequirements, error) {
	result := corev1.ResourceRequirements{}
	requests, err := parseQuantities("request", resources.CPURequest, resources.MemoryRequest)
	if err != nil {
		return result, err
	}
	limits, err := parseQuantities("limit", resources.CPULimit, resources.MemoryLimit)
	if err != nil {
		return result, err
	}
	result.Requests = requests
	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		request, hasRequest := requests[name]
		limit, hasLimit := limits[name]
		if hasRequest && hasLimit && request.Cmp(limit) > 0 {
			return corev1.ResourceRequirements{}, fmt.Errorf("%s request cannot exceed limit", name)
		}
	}
	result.Limits = limits
	return result, nil
}

func parseQuantities(kind, cpu, memory string) (corev1.ResourceList, error) {
	result := corev1.ResourceList{}
	for name, value := range map[string]string{"cpu": cpu, "memory": memory} {
		if strings.TrimSpace(value) == "" {
			continue
		}
		quantity, err := resource.ParseQuantity(value)
		if err != nil {
			return nil, fmt.Errorf("invalid %s %s resource quantity: %w", kind, name, err)
		}
		resourceName := corev1.ResourceName(name)
		if quantity.Sign() < 0 {
			return nil, fmt.Errorf("%s %s resource quantity must be nonnegative", kind, name)
		}
		if resourceName == corev1.ResourceCPU {
			// Round a copy so precision is checked without changing the value.
			rounded := quantity.DeepCopy()
			if !rounded.RoundUp(resource.Milli) {
				return nil, fmt.Errorf("%s cpu resource quantity must use increments of 1m", kind)
			}
		}
		result[resourceName] = quantity
	}
	return result, nil
}

func copyLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	result := make(map[string]string, len(labels))
	for key, value := range labels {
		result[key] = value
	}
	return result
}

func boolPtr(value bool) *bool {
	return &value
}

func formatPort(port int) string {
	return strconv.Itoa(port)
}
