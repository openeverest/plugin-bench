package main

import (
	"context"
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
)

type config struct {
	host, database, user, password             string
	port, duration, threads, tables, tableSize int
	initialize                                 bool
}

// loadConfig validates the runner's environment without opening a connection.
// Connection values are retained verbatim, including password whitespace.
func loadConfig(getenv func(string) string) (config, error) {
	var c config
	for _, field := range []struct {
		name   string
		target *string
	}{
		{"MYSQL_HOST", &c.host},
		{"MYSQL_DATABASE", &c.database},
		{"MYSQL_USER", &c.user},
		{"MYSQL_PASSWORD", &c.password},
	} {
		value := getenv(field.name)
		if value == "" || (field.name != "MYSQL_PASSWORD" && strings.TrimSpace(value) == "") {
			return config{}, fmt.Errorf("%s is required", field.name)
		}
		if strings.ContainsRune(value, '\x00') {
			return config{}, fmt.Errorf("%s must not contain NUL bytes", field.name)
		}
		*field.target = value
	}
	if strings.Contains(c.host, ",") {
		return config{}, fmt.Errorf("MYSQL_HOST must specify one host")
	}

	for _, field := range []struct {
		name              string
		fallback, maximum int
		target            *int
	}{
		{"MYSQL_PORT", 3306, 65535, &c.port},
		{"BENCH_DURATION", 60, 2147483647, &c.duration},
		{"BENCH_THREADS", 1, 2147483647, &c.threads},
		{"BENCH_TABLES", 1, 2147483647, &c.tables},
		{"BENCH_TABLE_SIZE", 10000, 2147483647, &c.tableSize},
	} {
		value := getenv(field.name)
		if value == "" {
			*field.target = field.fallback
			continue
		}
		n, err := strconv.ParseUint(value, 10, 31)
		if err != nil || n == 0 || n > uint64(field.maximum) {
			return config{}, fmt.Errorf("%s must be an integer between 1 and %d", field.name, field.maximum)
		}
		*field.target = int(n)
	}

	switch getenv("BENCH_INITIALIZE") {
	case "", "false":
	case "true":
		c.initialize = true
	default:
		return config{}, fmt.Errorf("BENCH_INITIALIZE must be true or false")
	}
	return c, nil
}

func commandEnvironment(env []string) []string {
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, "LC_ALL=") && !strings.HasPrefix(entry, "MYSQL_PASSWORD=") {
			result = append(result, entry)
		}
	}
	// Sysbench reads the password from the private connection file. Stable
	// output labels and decimal separators are needed by summary collection.
	return append(result, "LC_ALL=C")
}

// execute streams output and gives the child five seconds to stop after SIGTERM.
func execute(ctx context.Context, executable string, args, env []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	return cmd.Run()
}

func run(ctx context.Context, c config, connectionPath string, invoke func(context.Context, []string, io.Writer) error, stdout io.Writer) error {
	phases := []string{"run"}
	if c.initialize {
		phases = []string{"prepare", "run"}
	}
	for _, phase := range phases {
		if err := ctx.Err(); err != nil {
			return err
		}
		args := []string{
			"--config-file=" + connectionPath,
			"--db-driver=mysql",
			"--threads=" + strconv.Itoa(c.threads),
			"--tables=" + strconv.Itoa(c.tables),
			"--table-size=" + strconv.Itoa(c.tableSize),
			"--time=" + strconv.Itoa(c.duration),
			"--events=0",
			"--report-interval=0",
			"--db-debug=off",
			"--mysql-debug=off",
			"oltp_read_write", phase,
		}
		if err := invoke(ctx, args, stdout); err != nil {
			return fmt.Errorf("%s failed: %w", phase, err)
		}
	}
	return ctx.Err()
}

// runWithMetrics observes measured stdout after preparation and preserves the
// tool's log streams. A metrics failure does not change benchmark success.
func runWithMetrics(ctx context.Context, c config, connectionPath string, invoke func(context.Context, []string, io.Writer) error, stdout, stderr io.Writer, terminationPath string) error {
	var collector summaryCollector
	err := run(ctx, c, connectionPath, func(ctx context.Context, args []string, output io.Writer) error {
		if len(args) > 0 && args[len(args)-1] == "run" {
			output = io.MultiWriter(output, &collector)
		}
		return invoke(ctx, args, output)
	}, stdout)
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
	executable, err := exec.LookPath("sysbench")
	if err != nil {
		fmt.Fprintln(os.Stderr, "runner error: sysbench executable was not found")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	path, cleanup, err := writeConnectionConfig(c)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration error:", err)
		return 1
	}
	defer cleanup()
	env := commandEnvironment(os.Environ())
	err = runWithMetrics(ctx, c, path, func(ctx context.Context, args []string, stdout io.Writer) error {
		return execute(ctx, executable, args, env, stdout, os.Stderr)
	}, os.Stdout, os.Stderr, terminationLogPath)
	if ctx.Err() != nil {
		fmt.Fprintln(os.Stderr, "runner cancelled")
		return 1
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "runner error: sysbench execution failed")
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
