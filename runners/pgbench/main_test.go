package main

import (
	"bytes"
	"context"
	"errors"
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
	}
}

func TestExecute(t *testing.T) {
	env := commandEnvironment([]string{"RUNNER_TEST_CHILD=exit", "PGPORT=", "PGCONNECT_TIMEOUT=", "PGSSLMODE=require"}, config{port: 5432, timeout: 10})
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
	err = execute(ctx, os.Args[0], []string{"-test.run=^TestChildProcess$"}, []string{"RUNNER_TEST_CHILD=wait"}, &output, &output)
	if err == nil || ctx.Err() == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("child did not stop promptly: %v", err)
	}
}
