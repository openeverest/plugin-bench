package main

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"go.mongodb.org/mongo-driver/x/mongo/driver/connstring"
)

const (
	benchmarkDatabase = "ycsb"
	workloadPath      = "/etc/go-ycsb/workloads/workloada"
	reportInterval    = 10 // Seconds; go-ycsb requires a positive ticker interval.
	// go-ycsb always serves unauthenticated pprof, by default on every
	// interface (":6060"). Keep it reachable only from inside the Pod.
	debugPprofAddress = "127.0.0.1:6060"
)

type config struct {
	uri, collection, tlsCA               string
	recordCount, operationCount, threads int
	initialize                           bool
}

// loadConfig validates environment settings without connecting to MongoDB.
// Keep the original URI: its path and authSource also control authentication.
func loadConfig(getenv func(string) string) (config, error) {
	c := config{uri: getenv("MONGODB_URI")}
	if c.uri == "" {
		return config{}, errors.New("MONGODB_URI is required")
	}
	if !strings.HasPrefix(c.uri, "mongodb://") || strings.ContainsFunc(c.uri, unicode.IsControl) {
		return config{}, errors.New("MONGODB_URI must be a valid mongodb:// URI")
	}
	connection, err := connstring.ParseAndValidate(c.uri)
	if err != nil {
		// Parser errors may contain credentials or the complete URI.
		return config{}, errors.New("MONGODB_URI must be a valid mongodb:// URI")
	}
	if connection.Database != "" && connection.Database != benchmarkDatabase {
		return config{}, errors.New("go-ycsb supports only the ycsb database")
	}
	// Instances with operator-issued certificates are not publicly trusted;
	// their CA must be supplied for verification when the URI enables TLS.
	c.tlsCA = getenv("MONGODB_TLS_CA")
	if c.tlsCA != "" && !x509.NewCertPool().AppendCertsFromPEM([]byte(c.tlsCA)) {
		return config{}, errors.New("MONGODB_TLS_CA must contain a PEM-encoded CA certificate")
	}

	c.collection = getenv("BENCH_COLLECTION")
	if c.collection == "" {
		c.collection = "usertable"
	}
	// Use a conservative namespace limit suitable for sharded targets too.
	if strings.TrimSpace(c.collection) == "" || strings.HasPrefix(c.collection, "system.") ||
		strings.Contains(c.collection, "$") || strings.ContainsFunc(c.collection, unicode.IsControl) ||
		strings.HasPrefix(c.collection, ".") || strings.HasSuffix(c.collection, ".") || strings.Contains(c.collection, "..") ||
		len(benchmarkDatabase)+1+len(c.collection) > 235 {
		return config{}, errors.New("BENCH_COLLECTION must be a non-system collection name without control characters, $, or empty dot segments; namespace must not exceed 235 bytes")
	}

	for _, field := range []struct {
		name     string
		fallback int
		target   *int
	}{
		{"BENCH_RECORD_COUNT", 10000, &c.recordCount},
		{"BENCH_OPERATION_COUNT", 20000, &c.operationCount},
		{"BENCH_THREADS", 1, &c.threads},
	} {
		value := getenv(field.name)
		if value == "" {
			*field.target = field.fallback
			continue
		}
		n, err := strconv.ParseUint(value, 10, 31)
		if err != nil || n == 0 {
			return config{}, fmt.Errorf("%s must be an integer between 1 and 2147483647", field.name)
		}
		*field.target = int(n)
	}
	if c.threads > c.operationCount {
		return config{}, errors.New("BENCH_THREADS must not exceed BENCH_OPERATION_COUNT")
	}
	switch getenv("BENCH_INITIALIZE") {
	case "", "false":
	case "true":
		c.initialize = true
	default:
		return config{}, errors.New("BENCH_INITIALIZE must be true or false")
	}

	return c, nil
}

type phaseExecutionError struct {
	phase string
	cause error
}

func (e *phaseExecutionError) Error() string { return e.phase + " failed" }
func (e *phaseExecutionError) Unwrap() error { return e.cause }

// commandEnvironment keeps connection settings out of the child environment
// because the child reads them from private files. It also forces stable
// output formatting for parsing.
func commandEnvironment(env []string) []string {
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, "MONGODB_URI=") && !strings.HasPrefix(entry, "MONGODB_TLS_CA=") &&
			!strings.HasPrefix(entry, "LC_ALL=") {
			result = append(result, entry)
		}
	}
	return append(result, "LC_ALL=C")
}

// execute streams both child output streams and sends SIGTERM on cancellation.
// A child that does not stop gets killed after five seconds.
func execute(ctx context.Context, executable string, args, env []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	return cmd.Run()
}

// run owns the private file and inspects/sanitizes both streams for each phase.
func run(ctx context.Context, c config,
	invoke func(context.Context, []string, io.Writer, io.Writer) error,
	stdout, stderr io.Writer,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	connectionPath, cleanup, err := writeConnectionConfig(c)
	if err != nil {
		return err
	}
	defer cleanup()
	phases := []string{"run"}
	if c.initialize {
		phases = []string{"load", "run"}
	}
	for _, phase := range phases {
		if err := ctx.Err(); err != nil {
			return err
		}
		threads := c.threads
		if phase == "load" {
			threads = 1
		}
		args := []string{
			phase, "mongodb",
			"-P", workloadPath,
			"-P", connectionPath,
			"-p", "recordcount=" + strconv.Itoa(c.recordCount),
			"-p", "operationcount=" + strconv.Itoa(c.operationCount),
			"-p", "debug.pprof=" + debugPprofAddress,
			"--threads", strconv.Itoa(threads),
			"--table", c.collection,
			"--interval", strconv.Itoa(reportInterval),
		}
		output, safeStdout, safeStderr := newPhaseOutput(c, phase == "load", stdout, stderr)
		invokeErr := invoke(ctx, args, safeStdout, safeStderr)
		outputErr := output.finish(safeStdout, safeStderr)
		if err := ctx.Err(); err != nil {
			return err
		}
		if invokeErr != nil {
			// Preserve the exit/cancellation cause without printing its potentially sensitive text.
			return &phaseExecutionError{phase: phase, cause: invokeErr}
		}
		if outputErr != nil {
			return fmt.Errorf("%s failed: %w", phase, outputErr)
		}
	}
	return ctx.Err()
}

// runWithMetrics observes raw measured stdout while run sanitizes both log
// streams. Metrics are published only after all workload checks succeed.
func runWithMetrics(ctx context.Context, c config,
	invoke func(context.Context, []string, io.Writer, io.Writer) error,
	stdout, stderr io.Writer, terminationPath string,
) error {
	var collector summaryCollector
	err := run(ctx, c, func(ctx context.Context, args []string, safeOut, safeErr io.Writer) error {
		if len(args) > 0 && args[0] == "run" {
			safeOut = io.MultiWriter(safeOut, &collector)
		}
		return invoke(ctx, args, safeOut, safeErr)
	}, stdout, stderr)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	record, err := collector.result()
	if err == nil {
		err = writeTerminationResult(terminationPath, record)
	}
	if err != nil {
		fmt.Fprintln(stderr, "runner warning: structured metrics unavailable")
	}
	return nil
}

func runnerMain() int {
	c, err := loadConfig(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration error:", err)
		return 1
	}
	executable, err := exec.LookPath("go-ycsb")
	if err != nil {
		fmt.Fprintln(os.Stderr, "runner error: go-ycsb executable was not found")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	env := commandEnvironment(os.Environ())
	err = runWithMetrics(ctx, c, func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		return execute(ctx, executable, args, env, stdout, stderr)
	}, os.Stdout, os.Stderr, terminationLogPath)
	if ctx.Err() != nil {
		fmt.Fprintln(os.Stderr, "runner cancelled")
		return 1
	}
	if err != nil {
		// run returns safe diagnostics; child errors expose only the phase name.
		fmt.Fprintln(os.Stderr, "runner error:", err)
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
			return exitErr.ExitCode()
		}
		return 1
	}
	return 0
}

func main() {
	os.Exit(runnerMain())
}
