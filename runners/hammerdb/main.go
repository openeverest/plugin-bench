package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
)

func run(ctx context.Context, c config, paths scriptPaths, invoke func(context.Context, []string, io.Writer, io.Writer) error, stdout, stderr io.Writer) error {
	phases := []struct{ name, script, done string }{{"run", paths.run, paths.runDone}}
	if c.initialize {
		phases = append([]struct{ name, script, done string }{{"prepare", paths.prepare, paths.prepareDone}}, phases...)
	}
	for _, phase := range phases {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := os.Lstat(phase.done); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s completion record already exists or cannot be checked", phase.name)
		}
		if err := invoke(ctx, []string{"auto", phase.script}, stdout, stderr); err != nil {
			// Do not include arbitrary child errors, which may contain credentials.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("HammerDB %s execution failed", phase.name)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := checkCompletion(phase.done, phase.name); err != nil {
			return fmt.Errorf("HammerDB %s failed: %w", phase.name, err)
		}
	}
	return nil
}

// runStaged owns temporary files through execution, including cancellation.
// Each phase filters both streams before forwarding output to Pod logs.
func runStaged(ctx context.Context, c config, executable string, env []string, stdout, stderr io.Writer, terminationPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	paths, cleanup, err := writeScripts(c)
	if err != nil {
		return err
	}
	defer cleanup()
	childEnv := paths.environment(env)
	return runWithMetrics(ctx, c, paths, func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		return execute(ctx, executable, args, childEnv, stdout, stderr)
	}, stdout, stderr, terminationPath)
}

// runWithMetrics observes raw measured stdout alongside the redacting writer.
// A verified workload succeeds even if structured metrics are unavailable.
func runWithMetrics(ctx context.Context, c config, paths scriptPaths,
	invoke func(context.Context, []string, io.Writer, io.Writer) error,
	stdout, stderr io.Writer, terminationPath string,
) error {
	collector := summaryCollector{engine: c.databaseType}
	err := run(ctx, c, paths, func(ctx context.Context, args []string, destination, diagnostic io.Writer) error {
		out, safeErr := newOutput(c, destination, diagnostic)
		var output io.Writer = out
		if len(args) == 2 && args[1] == paths.run {
			output = io.MultiWriter(out, &collector)
		}
		executionErr := invoke(ctx, args, output, safeErr)
		outputErr := finishOutput(out, safeErr)
		return errors.Join(executionErr, outputErr)
	}, stdout, stderr)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Flush an unterminated final stdout line before examining optional timing.
	collector.flush()
	if data, readErr := os.ReadFile(paths.runDone); readErr == nil {
		fields := strings.Fields(string(data))
		if len(fields) == 3 {
			collector.collectTiming(fields[2])
		}
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
	executable, err := exec.LookPath("hammerdbcli")
	if err != nil {
		fmt.Fprintln(os.Stderr, "runner error: HammerDB CLI was not found")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = runStaged(ctx, c, executable, os.Environ(), os.Stdout, os.Stderr, terminationLogPath)
	if ctx.Err() != nil {
		fmt.Fprintln(os.Stderr, "runner cancelled")
		return 1
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "runner error: HammerDB execution failed")
		return 1
	}
	return 0
}

func main() { os.Exit(runnerMain()) }
