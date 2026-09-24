package coordinator

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	runnerContainerName    = "pgbench-runner"
	benchmarkJobNamePrefix = "plugin-bench-pgbench-"
	resourceCleanupTimeout = 10 * time.Second
	jobPollInterval        = time.Second
	maxRunnerLogBytes      = 1 << 20
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

type podLogReader func(context.Context, string, *corev1.PodLogOptions) (io.ReadCloser, error)

// collectRunnerLogs reads the runner container's combined Pod log stream and
// limits the returned output to maxRunnerLogBytes.
func (c *Coordinator) collectRunnerLogs(ctx context.Context, pod *corev1.Pod) (string, bool, error) {
	if c == nil {
		return "", false, errors.New("coordinator is nil")
	}
	if ctx == nil {
		return "", false, errors.New("log collection context is nil")
	}
	if c.kubeClient == nil {
		return "", false, ErrKubernetesClientRequired
	}
	if err := c.config.Validate(); err != nil {
		return "", false, fmt.Errorf("invalid coordinator configuration: %w", err)
	}
	if pod == nil || strings.TrimSpace(pod.Name) == "" {
		return "", false, errors.New("Pod name is required")
	}

	reader := func(ctx context.Context, name string, options *corev1.PodLogOptions) (io.ReadCloser, error) {
		return c.kubeClient.CoreV1().Pods(c.config.WorkloadNamespace).GetLogs(name, options).Stream(ctx)
	}
	return collectRunnerLogsWithReader(ctx, pod, reader)
}

func collectRunnerLogsWithReader(ctx context.Context, pod *corev1.Pod, readLogs podLogReader) (string, bool, error) {
	if ctx == nil {
		return "", false, errors.New("log collection context is nil")
	}
	if pod == nil || strings.TrimSpace(pod.Name) == "" {
		return "", false, errors.New("Pod name is required")
	}
	if readLogs == nil {
		return "", false, errors.New("Pod log reader is required")
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}

	stream, err := readLogs(ctx, pod.Name, &corev1.PodLogOptions{Container: runnerContainerName})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", false, ctxErr
		}
		return "", false, fmt.Errorf("open logs for Pod %q: %w", pod.Name, err)
	}
	if stream == nil {
		return "", false, fmt.Errorf("open logs for Pod %q: empty log stream", pod.Name)
	}

	data, readErr := io.ReadAll(io.LimitReader(stream, maxRunnerLogBytes+1))
	closeErr := stream.Close()
	if readErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", false, ctxErr
		}
		return "", false, fmt.Errorf("read logs for Pod %q: %w", pod.Name, readErr)
	}
	if closeErr != nil {
		return "", false, fmt.Errorf("close logs for Pod %q: %w", pod.Name, closeErr)
	}

	truncated := int64(len(data)) > maxRunnerLogBytes
	if truncated {
		data = data[:maxRunnerLogBytes]
	}
	return string(data), truncated, nil
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

	cleanupErr := c.cleanupBenchmarkResources(ctx, benchmarkResources{secret: secret})
	if cleanupErr != nil {
		return resources, errors.Join(err, fmt.Errorf("cleanup credential Secret after Job creation failure: %w", cleanupErr))
	}
	return resources, err
}

// deleteBenchmarkJob removes a Job created for a run. The UID precondition
// prevents deleting a different Job that was recreated with the same name.
// Foreground propagation asks Kubernetes to remove the owned Pod first.
func (c *Coordinator) deleteBenchmarkJob(ctx context.Context, reference JobRef) error {
	if c == nil {
		return errors.New("coordinator is nil")
	}
	if ctx == nil {
		return errors.New("Job deletion context is nil")
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
	if reference.UID == "" {
		return errors.New("benchmark Job UID is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	propagation := metav1.DeletePropagationForeground
	err := c.kubeClient.BatchV1().Jobs(c.config.WorkloadNamespace).Delete(ctx, reference.Name, metav1.DeleteOptions{
		Preconditions:     &metav1.Preconditions{UID: &reference.UID},
		PropagationPolicy: &propagation,
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete benchmark Job: %w", err)
	}
	// Foreground deletion keeps the Job until its blocking dependents are gone.
	// A successful DELETE only acknowledges the request; wait for confirmation.
	err = wait.PollUntilContextCancel(ctx, jobPollInterval, true, func(ctx context.Context) (bool, error) {
		job, err := c.kubeClient.BatchV1().Jobs(c.config.WorkloadNamespace).Get(ctx, reference.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if job.UID != reference.UID {
			return false, errors.New("Job UID changed before deletion could be confirmed")
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("wait for benchmark Job deletion: %w", err)
	}
	return nil
}

// cleanupBenchmarkResources removes the resources created for one run
func (c *Coordinator) cleanupBenchmarkResources(ctx context.Context, resources benchmarkResources) error {
	if c == nil {
		return errors.New("coordinator is nil")
	}
	if ctx == nil {
		return errors.New("resource cleanup context is nil")
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resourceCleanupTimeout)
	defer cancel()
	return c.cleanupBenchmarkResourcesWithContext(cleanupCtx, resources)
}

func (c *Coordinator) cleanupBenchmarkResourcesWithContext(ctx context.Context, resources benchmarkResources) error {
	var cleanupErrors []error
	if resources.job.Name != "" {
		jobCtx := ctx
		cancel := func() {}
		if deadline, ok := ctx.Deadline(); ok && resources.secret.Name != "" {
			// Reserve half the remaining cleanup budget for Secret deletion,
			// even if Kubernetes cannot finish deleting the Job in time.
			jobCtx, cancel = context.WithTimeout(ctx, time.Until(deadline)/2)
		}
		err := c.deleteBenchmarkJob(jobCtx, resources.job)
		cancel()
		if err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("cleanup benchmark Job: %w", err))
		}
	}
	if resources.secret.Name != "" {
		if err := c.deleteCredentialSecret(ctx, resources.secret); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("cleanup credential Secret: %w", err))
		}
	}
	return errors.Join(cleanupErrors...)
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
