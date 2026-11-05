package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProcessChild(t *testing.T) {
	switch os.Getenv("RUNNER_TEST_PROCESS") {
	case "exit":
		fmt.Fprintf(os.Stdout, "uri=%s locale=%s\n", os.Getenv("MONGODB_URI"), os.Getenv("LC_ALL"))
		fmt.Fprintln(os.Stderr, "child stderr")
		os.Exit(7)
	case "wait":
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		fmt.Fprintln(os.Stdout, "ready")
		<-signals
		fmt.Fprintln(os.Stdout, "received SIGTERM")
		os.Exit(0)
	case "ignore":
		signal.Ignore(syscall.SIGTERM)
		fmt.Fprintln(os.Stdout, "ready")
		for {
			time.Sleep(time.Hour)
		}
	}
}

func TestCommandEnvironmentRemovesURIAndForcesStableLocale(t *testing.T) {
	env := []string{"MONGODB_URI=mongodb://user:secret@host/ycsb", "MONGODB_TLS_CA=-----BEGIN CERTIFICATE-----", "LC_ALL=fr_FR.UTF-8", "PATH=/bin", "LC_ALL=de_DE.UTF-8"}
	before := append([]string(nil), env...)
	got := commandEnvironment(env)
	var locales []string
	for _, entry := range got {
		if strings.HasPrefix(entry, "MONGODB_URI=") || strings.HasPrefix(entry, "MONGODB_TLS_CA=") {
			t.Fatal("child must read connection settings from private files, not its environment")
		}
		if strings.HasPrefix(entry, "LC_ALL=") {
			locales = append(locales, entry)
		}
	}
	if !reflect.DeepEqual(locales, []string{"LC_ALL=C"}) || !reflect.DeepEqual(env, before) {
		t.Fatal("child environment must have one stable locale without mutating the caller")
	}
}

func TestExecutePreservesExitCodeAndDrainsBothStreams(t *testing.T) {
	coverageDir := t.TempDir()
	env := commandEnvironment([]string{
		"RUNNER_TEST_PROCESS=exit",
		"MONGODB_URI=mongodb://user:secret@host/ycsb",
		"LC_ALL=fr_FR.UTF-8",
		"GOCOVERDIR=" + coverageDir,
	})
	var stdout, stderr bytes.Buffer
	err := execute(context.Background(), os.Args[0], []string{"-test.run=^TestProcessChild$"}, env, &stdout, &stderr)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("child exit status was not preserved: %v", err)
	}
	if stdout.String() != "uri= locale=C\n" || stderr.String() != "child stderr\n" {
		t.Fatalf("child streams/environment were unexpected: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// Cancel only once the child has installed its signal handler and reported ready.
type cancelOnReadyWriter struct {
	buffer      bytes.Buffer
	cancel      context.CancelFunc
	cancelledAt time.Time
}

func (w *cancelOnReadyWriter) Write(data []byte) (int, error) {
	n, err := w.buffer.Write(data)
	if w.cancelledAt.IsZero() && bytes.Contains(w.buffer.Bytes(), []byte("ready\n")) {
		w.cancelledAt = time.Now()
		w.cancel()
	}
	return n, err
}

func TestRunCancellationSendsSIGTERMAndCleansConnectionFile(t *testing.T) {
	for _, mode := range []string{"wait", "ignore"} {
		t.Run(mode, func(t *testing.T) {
			parent := t.TempDir()
			t.Setenv("TMPDIR", parent)
			c, err := configFromEnvironment(validEnvironment())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			stdout := &cancelOnReadyWriter{cancel: cancel}
			var stderr bytes.Buffer
			var childErr error
			err = run(ctx, c, func(ctx context.Context, args []string, safeOut, safeErr io.Writer) error {
				childErr = execute(ctx, os.Args[0], []string{"-test.run=^TestProcessChild$"}, []string{
					"RUNNER_TEST_PROCESS=" + mode, "GOCOVERDIR=" + t.TempDir(),
				}, safeOut, safeErr)
				return childErr
			}, stdout, &stderr)
			if !errors.Is(err, context.Canceled) || stdout.cancelledAt.IsZero() {
				t.Fatalf("cancellation should follow child readiness: %v", err)
			}
			if mode == "wait" && !strings.Contains(stdout.buffer.String(), "received SIGTERM") {
				t.Fatal("child did not receive SIGTERM")
			}
			if mode == "ignore" {
				var exitErr *exec.ExitError
				if !errors.As(childErr, &exitErr) {
					t.Fatalf("child ignoring SIGTERM must be killed: %v", childErr)
				}
				status, ok := exitErr.Sys().(syscall.WaitStatus)
				if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL || time.Since(stdout.cancelledAt) < 5*time.Second {
					t.Fatal("child ignoring SIGTERM must receive SIGKILL after its five-second grace period")
				}
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatal("cancelled execution must clean up its private connection file")
			}
		})
	}
}

func TestRunnerEntrypoint(t *testing.T) {
	if os.Getenv("RUNNER_TEST_ENTRYPOINT") == "true" {
		os.Exit(runnerMain())
	}
}

func TestRunnerEntrypointRedactsLogsPreservesExitCodeAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	connectionPathFile := filepath.Join(dir, "connection-path")
	const stub = `#!/bin/sh
connection=
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-P" ]; then shift; connection="$1"; fi
  shift
done
printf '%s' "$connection" > "$RUNNER_TEST_CONNECTION_PATH"
printf '%s\n' '***************** properties *****************'
while IFS= read -r line; do printf '%s\n' "$line"; done < "$connection"
printf '%s\n' '***************'
printf '%s\n' 'native output'
exit 7
`
	if err := os.WriteFile(filepath.Join(dir, "go-ycsb"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + dir,
		"TMPDIR=" + dir,
		"RUNNER_TEST_ENTRYPOINT=true",
		"RUNNER_TEST_CONNECTION_PATH=" + connectionPathFile,
		"GOCOVERDIR=" + dir,
		"MONGODB_URI=mongodb://bench:dummy-secret@localhost:27017/ycsb?authSource=admin",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunnerEntrypoint$")
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("entrypoint did not preserve go-ycsb's exit code: %v", err)
	}
	if stdout.String() != "native output\n" || stderr.String() != "runner error: run failed\n" ||
		strings.Contains(stdout.String()+stderr.String(), "dummy-secret") {
		t.Fatalf("entrypoint forwarded unexpected or sensitive output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	connectionPath, err := os.ReadFile(connectionPathFile)
	if err != nil {
		t.Fatal("go-ycsb stub was not invoked with a connection file")
	}
	if _, err := os.Stat(filepath.Dir(string(connectionPath))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("entrypoint left its private connection directory behind")
	}
}

func TestRunnerEntrypointReportsSafeConfigurationFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go-ycsb"), []byte("#!/bin/sh\nprintf 'unexpected child invocation\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunnerEntrypoint$")
	cmd.Env = []string{
		"PATH=" + dir, "TMPDIR=" + filepath.Join(dir, "missing-private-path"),
		"RUNNER_TEST_ENTRYPOINT=true", "GOCOVERDIR=" + dir,
		"MONGODB_URI=" + validEnvironment()["MONGODB_URI"],
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("entrypoint should report configuration failure: %v", err)
	}
	if stdout.Len() != 0 || stderr.String() != "runner error: could not create the private go-ycsb configuration directory\n" {
		t.Fatalf("entrypoint lost its safe failure reason: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
