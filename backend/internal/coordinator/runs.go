package coordinator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const failureLogTimeout = 10 * time.Second

// Connection contains resolved database credentials. Do not log this value.
// Database must explicitly identify the database used for benchmarking.
type Connection struct {
	Host     string
	Port     int
	Database string
	Username string
	Password string
}

func (c Connection) Validate() error {
	if strings.TrimSpace(c.Host) == "" {
		return errors.New("database host is required")
	}
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("database port must be between 1 and 65535")
	}
	if strings.TrimSpace(c.Database) == "" {
		return errors.New("database name is required")
	}
	if strings.TrimSpace(c.Username) == "" {
		return errors.New("database username is required")
	}
	if c.Password == "" {
		return errors.New("database password is required")
	}
	return nil
}

// Options contains per-run settings. Duration is measured in seconds.
// Initialize must be explicitly enabled: it drops and recreates pgbench tables.
type Options struct {
	Duration   int
	Clients    int
	Threads    int
	Scale      int
	Initialize bool
}

func (o Options) Validate() error {
	// Match the runner's supported positive 31-bit integer range.
	const maximum = 1<<31 - 1
	if o.Duration < 1 || o.Duration > maximum {
		return errors.New("benchmark duration must be between 1 and 2147483647 seconds")
	}
	if o.Clients < 1 || o.Clients > maximum {
		return errors.New("benchmark clients must be between 1 and 2147483647")
	}
	if o.Threads < 1 || o.Threads > maximum {
		return errors.New("benchmark threads must be between 1 and 2147483647")
	}
	if o.Threads > o.Clients {
		return errors.New("benchmark threads cannot exceed clients")
	}
	if o.Scale < 1 || o.Scale > maximum {
		return errors.New("benchmark scale must be between 1 and 2147483647")
	}
	return nil
}

// Result contains execution output, not parsed metrics or persisted history.
// A run will return this alongside an error when execution or cleanup fails.
type Result struct {
	JobName         string
	Output          string
	OutputTruncated bool
}

// Run creates and executes one benchmark Job, returns the runner output, and
// removes the Job and temporary credential Secret before returning.
func (c *Coordinator) Run(ctx context.Context, connection Connection, options Options) (result Result, runErr error) {
	if c == nil {
		return result, errors.New("coordinator is nil")
	}
	if ctx == nil {
		return result, errors.New("run context is nil")
	}
	if err := c.config.Validate(); err != nil {
		return result, fmt.Errorf("invalid coordinator configuration: %w", err)
	}
	if err := connection.Validate(); err != nil {
		return result, fmt.Errorf("invalid database connection: %w", err)
	}
	if err := options.Validate(); err != nil {
		return result, fmt.Errorf("invalid benchmark options: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	// Bound the coordinator's API calls and waiting, independently of the
	// Kubernetes Job deadline. An earlier caller deadline still takes priority.
	ctx, cancelExecution := context.WithTimeout(ctx, c.config.ExecutionTimeout)
	defer cancelExecution()

	resources, err := c.createBenchmarkResources(ctx, connection, options, nil)
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

	result.Output, result.OutputTruncated, err = c.collectRunnerLogs(logCtx, pod)
	if err != nil {
		return result, errors.Join(runErr, fmt.Errorf("collect runner logs for Job %q: %w", resources.job.Name, err))
	}
	return result, runErr
}

// DefaultOptions matches the existing pgbench runner defaults.
func DefaultOptions() Options {
	return Options{Duration: 60, Clients: 1, Threads: 1, Scale: 1}
}
