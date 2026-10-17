package coordinator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
	"github.com/openeverest/plugin-bench/backend/internal/runnerresult"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const failureLogTimeout = 10 * time.Second

// RunInput describes a prepared workload. It contains credentials and must not
// be logged or persisted. Tool-specific preparation belongs to the caller.
type RunInput struct {
	Runner       driver.RunnerConfig
	Spec         driver.RunnerSpec
	NodeAffinity *corev1.NodeAffinity
	Resources    Resources
}

// Validate checks the generic execution contract. Resource inheritance and
// affinity are checked before the first Kubernetes write.
func (input RunInput) Validate() error {
	if err := input.Runner.Validate(); err != nil {
		return err
	}
	if err := input.Spec.Validate(); err != nil {
		return err
	}
	for name, value := range input.Spec.Env {
		if len(validation.IsEnvVarName(name)) != 0 || strings.ContainsRune(value, '\x00') {
			return errors.New("invalid runner environment variable")
		}
	}
	for name, key := range input.Spec.SecretEnv {
		if len(validation.IsEnvVarName(name)) != 0 {
			return errors.New("invalid runner secret environment name")
		}
		if strings.ContainsRune(string(input.Spec.SecretData[key]), '\x00') {
			return errors.New("invalid runner secret environment value")
		}
	}
	for key := range input.Spec.SecretData {
		if len(validation.IsConfigMapKey(key)) != 0 {
			return errors.New("invalid runner Secret key")
		}
	}
	for _, values := range [][]string{input.Spec.Command, input.Spec.Args} {
		for _, value := range values {
			if strings.ContainsRune(value, '\x00') {
				return errors.New("invalid runner command or argument")
			}
		}
	}
	return nil
}

func (input RunInput) clone() RunInput {
	input.Spec = input.Spec.Clone()
	input.NodeAffinity = input.NodeAffinity.DeepCopy()
	return input
}

// Result contains execution logs and optional structured runner metrics.
// A run will return this alongside an error when execution or cleanup fails.
type Result struct {
	JobName         string
	Output          string
	OutputTruncated bool
	Metrics         []runnerresult.Metric
}

// Run creates and executes one benchmark Job, returns the runner output, and
// removes the Job and temporary credential Secret before returning.
func (c *Coordinator) Run(ctx context.Context, input RunInput) (result Result, runErr error) {
	if c == nil {
		return result, errors.New("coordinator is nil")
	}
	if ctx == nil {
		return result, errors.New("run context is nil")
	}
	if err := c.config.Validate(); err != nil {
		return result, fmt.Errorf("invalid coordinator configuration: %w", err)
	}
	if err := input.Validate(); err != nil {
		return result, fmt.Errorf("invalid runner input: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	// Bound the coordinator's API calls and waiting, independently of the
	// Kubernetes Job deadline. An earlier caller deadline still takes priority.
	ctx, cancelExecution := context.WithTimeout(ctx, c.config.ExecutionTimeout)
	defer cancelExecution()

	resources, err := c.createBenchmarkResources(ctx, input, nil)
	if err != nil {
		return result, err
	}
	result.JobName = resources.job.Name

	defer func() {
		if cleanupErr := c.cleanupBenchmarkResources(ctx, resources); cleanupErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("cleanup benchmark resources: %w", cleanupErr))
		}
	}()

	logCtx := ctx
	if err := c.waitForJob(ctx, resources.job); err != nil {
		runErr = fmt.Errorf("wait for benchmark Job %q: %w", resources.job.Name, err)
		var schedulingErr *SchedulingError
		if errors.As(err, &schedulingErr) {
			// The scheduling failure already contains the last observed diagnostic.
			// This runner never started, so no container logs are available.
			return result, runErr
		}
		// Preserve diagnostics even when the execution context has expired.
		// Both Pod discovery and log retrieval share this bounded budget.
		var cancel context.CancelFunc
		logCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), failureLogTimeout)
		defer cancel()
	}

	pod, err := c.findJobPod(logCtx, resources.job)
	if err != nil {
		return result, errors.Join(runErr, fmt.Errorf("find runner Pod for Job %q: %w", resources.job.Name, err))
	}
	// Extract metrics before log reads and deferred cleanup, preserving them
	// even if those operations fail. Invalid records do not affect runErr.
	result.Metrics = runnerMetrics(pod)
	if runErr != nil {
		if condition := unschedulableCondition(pod); condition != nil {
			runErr = errors.Join(runErr, &SchedulingError{PodName: pod.Name, Message: condition.Message})
		}
		if !runnerHasStarted(pod) {
			return result, runErr
		}
	}

	result.Output, result.OutputTruncated, err = c.collectRunnerLogs(logCtx, pod)
	if err != nil {
		return result, errors.Join(runErr, fmt.Errorf("collect runner logs for Job %q: %w", resources.job.Name, err))
	}
	return result, runErr
}
