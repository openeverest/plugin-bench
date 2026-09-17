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
	port, timeout, duration, clients, threads, scale int
	initialize                                       bool
}

func loadConfig(getenv func(string) string) (config, error) {
	var c config
	for _, name := range []string{"PGHOST", "PGDATABASE", "PGUSER"} {
		if strings.TrimSpace(getenv(name)) == "" {
			return c, fmt.Errorf("%s is required", name)
		}
	}
	if getenv("PGPASSWORD") == "" && getenv("PGPASSFILE") == "" {
		return c, errors.New("PGPASSWORD or PGPASSFILE is required")
	}
	if getenv("PGPASSWORD") == "" {
		path := getenv("PGPASSFILE")
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return c, errors.New("PGPASSFILE must refer to an existing regular file")
		}
		// libpq ignores password files accessible by group or other users.
		if info.Mode().Perm()&0077 != 0 {
			return c, errors.New("PGPASSFILE must not allow group or other access (use mode 0600)")
		}
		file, err := os.Open(path)
		if err != nil {
			return c, errors.New("PGPASSFILE must be readable by the runner user")
		}
		file.Close()
	}
	for _, field := range []struct {
		name              string
		fallback, maximum int
		target            *int
	}{
		{"PGPORT", 5432, 65535, &c.port},
		{"PGCONNECT_TIMEOUT", 10, 2147483647, &c.timeout},
		{"BENCH_DURATION", 60, 2147483647, &c.duration},
		{"BENCH_CLIENTS", 1, 2147483647, &c.clients},
		{"BENCH_THREADS", 1, 2147483647, &c.threads},
		{"BENCH_SCALE", 1, 2147483647, &c.scale},
	} {
		value := getenv(field.name)
		if value == "" {
			*field.target = field.fallback
			continue
		}
		n, err := strconv.ParseUint(value, 10, 31)
		if err != nil || n == 0 || n > uint64(field.maximum) {
			return c, fmt.Errorf("%s must be an integer between 1 and %d", field.name, field.maximum)
		}
		*field.target = int(n)
	}
	switch getenv("BENCH_INITIALIZE") {
	case "", "false":
	case "true":
		c.initialize = true
	default:
		return c, errors.New("BENCH_INITIALIZE must be true or false")
	}
	if c.threads > c.clients {
		return c, errors.New("BENCH_THREADS cannot exceed BENCH_CLIENTS")
	}
	return c, nil
}

// commandEnvironment preserves connection and TLS settings, replacing defaulted
// values without putting credentials into command arguments.
func commandEnvironment(env []string, c config) []string {
	result := make([]string, 0, len(env)+2)
	for _, entry := range env {
		if !strings.HasPrefix(entry, "PGPORT=") && !strings.HasPrefix(entry, "PGCONNECT_TIMEOUT=") {
			result = append(result, entry)
		}
	}
	return append(result, "PGPORT="+strconv.Itoa(c.port), "PGCONNECT_TIMEOUT="+strconv.Itoa(c.timeout))
}

// execute streams pgbench output and terminates the child when the runner is
// cancelled. A child that ignores SIGTERM is killed after five seconds.
func execute(ctx context.Context, executable string, args, env []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	return cmd.Run()
}

func run(ctx context.Context, c config, invoke func(context.Context, []string) error) error {
	if c.initialize {
		if err := invoke(ctx, []string{"-i", "-s", strconv.Itoa(c.scale)}); err != nil {
			return fmt.Errorf("initialization failed: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return invoke(ctx, []string{"-c", strconv.Itoa(c.clients), "-j", strconv.Itoa(c.threads), "-T", strconv.Itoa(c.duration)})
}

func runnerMain() int {
	c, err := loadConfig(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration error:", err)
		return 1
	}
	executable, err := exec.LookPath("pgbench")
	if err != nil {
		fmt.Fprintln(os.Stderr, "runner error: pgbench executable was not found")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	env := commandEnvironment(os.Environ(), c)
	err = run(ctx, c, func(ctx context.Context, args []string) error {
		return execute(ctx, executable, args, env, os.Stdout, os.Stderr)
	})
	if ctx.Err() != nil {
		fmt.Fprintln(os.Stderr, "runner cancelled")
		return 1
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "runner error: pgbench execution failed")
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
