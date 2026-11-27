package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Include time for seeding and verification in addition to measurement.
const runTimeout = 10 * time.Minute

func run(ctx context.Context, c config, executable string, files privateFiles) error {
	ctx, timeout := context.WithTimeout(ctx, runTimeout)
	defer timeout()
	endpoint, err := newValkeyEndpoint(c, files)
	if err != nil {
		return err
	}
	if err := endpoint.preflight(ctx); err != nil {
		return err
	}

	connectionArgs := []string{
		"--server=" + c.host,
		"--port=" + strconv.Itoa(c.port),
		"--authenticate=" + c.username + ":" + c.password,
	}
	if c.tls {
		connectionArgs = append(connectionArgs, "--tls", "--cacert="+files.caPath,
			"--sni="+strings.TrimSuffix(c.host, "."), "--tls-protocols=TLSv1.2,TLSv1.3")
		if files.certPath != "" {
			connectionArgs = append(connectionArgs, "--cert="+files.certPath, "--key="+files.keyPath)
		}
	}
	args := newWorkloadArgs(c, files.resultPath)
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), connectTimeout)
		defer stop()
		if err := removeBenchmarkKeys(cleanupCtx, endpoint, args.prefix, c.keyCount); err != nil {
			fmt.Fprintf(os.Stderr, "runner warning: benchmark key cleanup incomplete: %v; remaining keys expire automatically\n", err)
		}
	}()
	env := make([]string, 0)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "VALKEY_") {
			env = append(env, entry)
		}
	}
	redact := memtierLogRedactor(c)
	logBudget := &memtierLogBudget{remaining: maxMemtierLogBytes}
	for _, phase := range []struct {
		name string
		args []string
	}{
		{"seeding", args.seed},
		{"measurement", args.measure},
	} {
		fmt.Fprintln(os.Stdout, "Valkey benchmark:", phase.name)
		cmd := exec.CommandContext(ctx, executable, append(phase.args, connectionArgs...)...)
		cmd.Env = env
		stdout := &memtierLogWriter{destination: os.Stdout, redact: redact, budget: logBudget}
		stderr := &memtierLogWriter{destination: os.Stderr, redact: redact, budget: logBudget, watchErrorReplies: true}
		cmd.Stdout, cmd.Stderr = stdout, stderr
		err := cmd.Run()
		stdout.finish()
		stderr.finish()
		if stdout.err != nil || stderr.err != nil {
			fmt.Fprintln(os.Stderr, "runner warning: could not forward Memtier output")
		}
		if cause := context.Cause(ctx); cause != nil {
			return fmt.Errorf("%s failed: %w", phase.name, cause)
		}
		if err != nil {
			return fmt.Errorf("Memtier %s failed: %w", phase.name, err)
		}
		if stderr.sawErrorReply {
			return fmt.Errorf("Memtier %s failed: Valkey returned an error reply", phase.name)
		}
		if phase.name == "seeding" {
			if err := verifySeed(ctx, endpoint, args.prefix, c.keyCount); err != nil {
				return fmt.Errorf("seed validation failed: %w", err)
			}
		}
	}
	if cause := context.Cause(ctx); cause != nil {
		return fmt.Errorf("measurement failed: %w", cause)
	}
	fmt.Fprintln(os.Stdout, "Valkey benchmark: measurement complete")
	return nil
}

func runnerMain(ctx context.Context) error {
	c, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}
	executable, err := exec.LookPath("memtier_benchmark")
	if err != nil {
		return errors.New("memtier_benchmark executable was not found")
	}
	files, cleanup, err := createPrivateFiles(c)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := run(ctx, c, executable, files); err != nil {
		return err
	}
	if err := publishMetrics(files.resultPath, terminationLogPath); err != nil {
		if errors.Is(err, errUnansweredRequests) {
			return err
		}
		fmt.Fprintln(os.Stderr, "runner warning: structured metrics unavailable")
	}
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := runnerMain(ctx)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Valkey runner:", err)
		os.Exit(1)
	}
}
