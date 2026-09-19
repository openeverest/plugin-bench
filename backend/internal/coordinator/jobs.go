package coordinator

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const runnerContainerName = "pgbench-runner"

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
