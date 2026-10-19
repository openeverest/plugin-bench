package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func validEnvironment() map[string]string {
	return map[string]string{"PGHOST": "postgres", "PGDATABASE": "bench", "PGUSER": "postgres", "PGPASSWORD": "dummy-secret"}
}

func TestConfig(t *testing.T) {
	env := validEnvironment()
	c, err := loadConfig(func(key string) string { return env[key] })
	if err != nil || c != (config{port: 5432, timeout: 10, duration: 60, clients: 1, threads: 1, scale: 1}) {
		t.Fatalf("unexpected defaults: %+v, %v", c, err)
	}
	for _, field := range []string{"PGPORT", "PGCONNECT_TIMEOUT", "BENCH_DURATION", "BENCH_CLIENTS", "BENCH_THREADS", "BENCH_SCALE"} {
		for _, value := range []string{"0", "00", "-1", "abc", "999999999999999999999999"} {
			t.Run(field+"/"+value, func(t *testing.T) {
				env := validEnvironment()
				env[field] = value
				_, err := loadConfig(func(key string) string { return env[key] })
				if err == nil || !strings.Contains(err.Error(), field) {
					t.Fatalf("expected safe field error, got %v", err)
				}
			})
		}
	}
	for _, field := range []string{"PGHOST", "PGDATABASE", "PGUSER", "PGPASSWORD"} {
		t.Run("missing/"+field, func(t *testing.T) {
			env := validEnvironment()
			delete(env, field)
			_, err := loadConfig(func(key string) string { return env[key] })
			if err == nil || strings.Contains(err.Error(), "dummy-secret") {
				t.Fatalf("expected safe validation error, got %v", err)
			}
		})
	}
	for key, value := range map[string]string{"PGPORT": "65536", "BENCH_INITIALIZE": "yes", "BENCH_THREADS": "2"} {
		env := validEnvironment()
		env[key] = value
		if _, err := loadConfig(func(key string) string { return env[key] }); err == nil {
			t.Fatalf("accepted invalid %s", key)
		}
	}
}

func TestPasswordFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pgpass")
	if err := os.WriteFile(path, []byte("postgres:5432:bench:postgres:dummy-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		mode       os.FileMode
		password   string
		wantError  bool
	}{
		{"valid", path, 0600, "", false},
		{"missing", filepath.Join(dir, "missing"), 0600, "", true},
		{"directory", dir, 0600, "", true},
		{"insecure permissions", path, 0644, "", true},
		{"unreadable", path, 0000, "", true},
		{"password takes precedence", filepath.Join(dir, "missing"), 0600, "dummy-secret", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "unreadable" && os.Geteuid() == 0 {
				t.Skip("root can read mode-0000 files")
			}
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}
			env := validEnvironment()
			env["PGPASSWORD"], env["PGPASSFILE"] = tc.password, tc.path
			_, err := loadConfig(func(key string) string { return env[key] })
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v, wantError=%v", err, tc.wantError)
			}
			if err != nil && (strings.Contains(err.Error(), tc.path) || strings.Contains(err.Error(), "dummy-secret")) {
				t.Fatal("error exposed password file path or contents")
			}
		})
	}
}

func TestRun(t *testing.T) {
	failure := errors.New("child failure")
	for _, tc := range []struct {
		name       string
		initialize bool
		failAt     int
		wantCalls  int
	}{
		{"benchmark only", false, 0, 1},
		{"initialize first", true, 0, 2},
		{"initialization failure", true, 1, 1},
		{"benchmark failure", false, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			err := run(context.Background(), config{initialize: tc.initialize, scale: 3, clients: 4, threads: 2, duration: 7},
				func(_ context.Context, args []string) error {
					calls = append(calls, args)
					if len(calls) == tc.failAt {
						return failure
					}
					return nil
				})
			if len(calls) != tc.wantCalls || errors.Is(err, failure) != (tc.failAt != 0) {
				t.Fatalf("calls=%v, error=%v", calls, err)
			}
			want := [][]string{{"-c", "4", "-j", "2", "-T", "7"}}
			if tc.initialize {
				want = append([][]string{{"-i", "-s", "3"}}, want...)
			}
			if !reflect.DeepEqual(calls, want[:tc.wantCalls]) {
				t.Fatalf("wrong arguments: %v", calls)
			}
		})
	}
}

func TestChildProcess(t *testing.T) {
	switch os.Getenv("RUNNER_TEST_CHILD") {
	case "exit":
		os.Stdout.WriteString(os.Getenv("PGPORT") + "/" + os.Getenv("PGCONNECT_TIMEOUT") + "/" + os.Getenv("PGSSLMODE"))
		os.Exit(7)
	case "wait":
		time.Sleep(time.Minute)
		os.Exit(0)
	case "summary":
		data, err := os.ReadFile(filepath.Join("testdata", "summary-pg16.txt"))
		if err != nil {
			os.Exit(1)
		}
		os.Stdout.Write(data)
		os.Exit(0)
	}
}

func TestExecute(t *testing.T) {
	// A coverage-instrumented child needs a directory to avoid adding a runtime
	// warning to stdout, where this test checks the exact subprocess output.
	coverageDir := t.TempDir()
	env := commandEnvironment([]string{"RUNNER_TEST_CHILD=exit", "PGPORT=", "PGCONNECT_TIMEOUT=", "PGSSLMODE=require", "GOCOVERDIR=" + coverageDir}, config{port: 5432, timeout: 10})
	var output bytes.Buffer
	err := execute(context.Background(), os.Args[0], []string{"-test.run=^TestChildProcess$"}, env, &output, &output)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("lost exit status: %v", err)
	}
	if output.String() != "5432/10/require" {
		t.Fatalf("unexpected child environment: %q", output.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = execute(ctx, os.Args[0], []string{"-test.run=^TestChildProcess$"}, []string{"RUNNER_TEST_CHILD=wait", "GOCOVERDIR=" + coverageDir}, &output, &output)
	if err == nil || ctx.Err() == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("child did not stop promptly: %v", err)
	}
}

func TestCommandEnvironmentForcesStableLocale(t *testing.T) {
	env := []string{"LC_ALL=fr_FR.UTF-8", "LC_ALL=de_DE.UTF-8", "PGSSLMODE=require", "PATH=/bin"}
	before := append([]string(nil), env...)
	got := commandEnvironment(env, config{port: 5432, timeout: 10})
	var locales []string
	for _, entry := range got {
		if strings.HasPrefix(entry, "LC_ALL=") {
			locales = append(locales, entry)
		}
	}
	if !reflect.DeepEqual(locales, []string{"LC_ALL=C"}) || !reflect.DeepEqual(env, before) {
		t.Fatal("locale override is ambiguous or modified caller environment")
	}
}

func TestRunWithMetricsStreamsOutputAndExcludesInitialization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination-log")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	var calls int
	initialization := "initializing\nlatency average = 999 ms\ntps = 999 (without initial connection time)\n"
	summary := summaryFixture(t)
	err := runWithMetrics(context.Background(), config{initialize: true}, func(_ context.Context, args []string, output io.Writer) error {
		calls++
		if args[0] == "-i" {
			_, err := io.WriteString(output, initialization)
			return err
		}
		_, err := io.WriteString(output, summary)
		return err
	}, &stdout, &stderr, path)
	if err != nil || calls != 2 || stderr.Len() != 0 {
		t.Fatalf("calls=%d, error=%v, diagnostics=%q", calls, err, stderr.String())
	}
	if stdout.String() != initialization+summary {
		t.Fatal("runner logs changed")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("..", "..", "testdata", "runner-result-v1.json"))
	if err != nil || !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(fixture)) {
		t.Fatal("termination metrics include initialization output or differ from contract")
	}
}

func TestRunWithMetricsPreservesFailuresAndCancellation(t *testing.T) {
	childFailure := errors.New("child failure")
	for _, scenario := range []string{"initialization failure", "benchmark failure", "cancelled before run", "cancelled after output"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "cancelled before run" {
				cancel()
			}
			path := filepath.Join(t.TempDir(), "termination-log")
			if err := os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			calls := 0
			err := runWithMetrics(ctx, config{initialize: scenario == "initialization failure"}, func(_ context.Context, _ []string, output io.Writer) error {
				calls++
				if _, err := io.WriteString(output, summaryFixture(t)); err != nil {
					return err
				}
				if scenario == "cancelled after output" {
					cancel()
					return nil
				}
				return childFailure
			}, &stdout, &stderr, path)
			if strings.HasPrefix(scenario, "cancelled") {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
			} else if !errors.Is(err, childFailure) {
				t.Fatalf("child failure lost: %v", err)
			}
			if scenario == "cancelled before run" && calls != 0 {
				t.Fatal("cancelled runner started a subprocess")
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil || len(data) != 0 {
				t.Fatal("failed or cancelled run wrote success metrics")
			}
		})
	}
}

func TestRunWithMetricsUnavailableDoesNotFailBenchmark(t *testing.T) {
	for _, scenario := range []string{"no summary", "malformed summary", "unwritable termination file", "missing termination file"} {
		t.Run(scenario, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "termination-log")
			if scenario != "missing termination file" {
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "unwritable termination file" {
				path = t.TempDir()
			}
			var stdout, stderr bytes.Buffer
			err := runWithMetrics(context.Background(), config{}, func(_ context.Context, _ []string, output io.Writer) error {
				text := summaryFixture(t)
				if scenario == "no summary" {
					text = "no transactions processed\n"
				}
				if scenario == "malformed summary" {
					text = "latency average = credential-sentinel ms\n"
				}
				_, err := io.WriteString(output, text)
				return err
			}, &stdout, &stderr, path)
			if err != nil {
				t.Fatalf("successful benchmark failed due to unavailable metrics: %v", err)
			}
			if stderr.String() != "runner warning: structured metrics unavailable\n" {
				t.Fatalf("unexpected diagnostic: %q", stderr.String())
			}
		})
	}
}

func TestRunWithMetricsCollectsSubprocessStdout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination-log")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	env := []string{"RUNNER_TEST_CHILD=summary", "GOCOVERDIR=" + t.TempDir()}
	var stdout, stderr bytes.Buffer
	err := runWithMetrics(context.Background(), config{}, func(ctx context.Context, _ []string, output io.Writer) error {
		return execute(ctx, os.Args[0], []string{"-test.run=^TestChildProcess$"}, env, output, &stderr)
	}, &stdout, &stderr, path)
	if err != nil || stderr.Len() != 0 || stdout.String() != summaryFixture(t) {
		t.Fatalf("subprocess output changed or execution failed: %v, stderr=%q", err, stderr.String())
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(data, []byte(`"value":1234.5`)) {
		t.Fatalf("subprocess metrics were not written: %v", err)
	}
}
