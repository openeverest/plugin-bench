package main

import (
	"bufio"
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
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func validEnvironment() map[string]string {
	return map[string]string{
		"MYSQL_HOST":     "mysql",
		"MYSQL_DATABASE": "sbtest",
		"MYSQL_USER":     "sbuser",
		"MYSQL_PASSWORD": "dummy-secret",
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	env := validEnvironment()
	got, err := loadConfig(func(key string) string { return env[key] })
	want := config{
		host: "mysql", database: "sbtest", user: "sbuser", password: "dummy-secret",
		port: 3306, duration: 60, threads: 1, tables: 1, tableSize: 10000,
	}
	if err != nil || got != want {
		t.Fatal("configuration did not preserve connection settings and apply the expected defaults")
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	env := validEnvironment()
	for key, value := range map[string]string{
		"MYSQL_HOST": "127.0.0.1", "MYSQL_DATABASE": "custom_bench", "MYSQL_USER": "custom_user",
		"MYSQL_PASSWORD": "  p@ss,'\"#\\word  ", "MYSQL_PORT": "3307",
		"BENCH_DURATION": "30", "BENCH_THREADS": "4", "BENCH_TABLES": "2",
		"BENCH_TABLE_SIZE": "5000", "BENCH_INITIALIZE": "true",
	} {
		env[key] = value
	}
	got, err := loadConfig(func(key string) string { return env[key] })
	want := config{
		host: "127.0.0.1", database: "custom_bench", user: "custom_user", password: env["MYSQL_PASSWORD"],
		port: 3307, duration: 30, threads: 4, tables: 2, tableSize: 5000, initialize: true,
	}
	if err != nil || got != want {
		t.Fatal("configuration did not preserve explicit connection and benchmark settings")
	}
}

func TestLoadConfigRejectsInvalidConnection(t *testing.T) {
	for _, field := range []string{"MYSQL_HOST", "MYSQL_DATABASE", "MYSQL_USER", "MYSQL_PASSWORD"} {
		values := []string{"", "invalid\x00value"}
		if field != "MYSQL_PASSWORD" {
			values = append(values, " \t\n")
		}
		for i, value := range values {
			t.Run(field+"/"+strconv.Itoa(i), func(t *testing.T) {
				env := validEnvironment()
				env[field] = value
				assertConfigError(t, env, field)
			})
		}
	}
	t.Run("multiple hosts", func(t *testing.T) {
		env := validEnvironment()
		env["MYSQL_HOST"] = "mysql-a,mysql-b"
		assertConfigError(t, env, "MYSQL_HOST")
	})
}

func TestLoadConfigPreservesConnectionValues(t *testing.T) {
	for _, host := range []string{"mysql.default.svc", "127.0.0.1", "::1"} {
		env := validEnvironment()
		env["MYSQL_HOST"] = host
		env["MYSQL_DATABASE"] = " bench database "
		env["MYSQL_USER"] = " bench user "
		env["MYSQL_PASSWORD"] = " \t "
		got, err := loadConfig(func(key string) string { return env[key] })
		if err != nil || got.host != host || got.database != env["MYSQL_DATABASE"] ||
			got.user != env["MYSQL_USER"] || got.password != env["MYSQL_PASSWORD"] {
			t.Fatal("connection configuration was rejected or silently changed")
		}
	}
}

func TestLoadConfigNumericBounds(t *testing.T) {
	for _, field := range []struct {
		name    string
		maximum int
		read    func(config) int
	}{
		{"MYSQL_PORT", 65535, func(c config) int { return c.port }},
		{"BENCH_DURATION", 2147483647, func(c config) int { return c.duration }},
		{"BENCH_THREADS", 2147483647, func(c config) int { return c.threads }},
		{"BENCH_TABLES", 2147483647, func(c config) int { return c.tables }},
		{"BENCH_TABLE_SIZE", 2147483647, func(c config) int { return c.tableSize }},
	} {
		t.Run(field.name, func(t *testing.T) {
			for _, value := range []string{
				"0", "00", "-1", "+1", "1.5", "1e3", "abc", " ", " 1", "1 ",
				"2147483648", "999999999999999999999999", strconv.Itoa(field.maximum + 1),
			} {
				env := validEnvironment()
				env[field.name] = value
				// Table settings remain meaningful when existing tables are reused.
				env["BENCH_INITIALIZE"] = "false"
				assertConfigError(t, env, field.name)
			}
			for _, value := range []int{1, field.maximum} {
				env := validEnvironment()
				env[field.name] = strconv.Itoa(value)
				got, err := loadConfig(func(key string) string { return env[key] })
				if err != nil || field.read(got) != value {
					t.Fatalf("valid boundary for %s was rejected or changed", field.name)
				}
			}
		})
	}
}

func TestLoadConfigInitialization(t *testing.T) {
	for _, value := range []string{"", "false", "true"} {
		env := validEnvironment()
		env["BENCH_INITIALIZE"] = value
		got, err := loadConfig(func(key string) string { return env[key] })
		if err != nil || got.initialize != (value == "true") {
			t.Fatalf("unexpected initialization setting for %q", value)
		}
	}
	for _, value := range []string{"TRUE", "False", "1", "0", "yes", " true ", "dummy-secret"} {
		env := validEnvironment()
		env["BENCH_INITIALIZE"] = value
		assertConfigError(t, env, "BENCH_INITIALIZE")
	}
}

func assertConfigError(t *testing.T, env map[string]string, field string) {
	t.Helper()
	got, err := loadConfig(func(key string) string { return env[key] })
	if err == nil {
		t.Fatalf("expected a configuration error for %s", field)
	}
	if !strings.Contains(err.Error(), field) {
		t.Fatalf("error did not identify the invalid setting %s", field)
	}
	// Short numeric inputs can also occur in the documented valid range.
	if strings.Contains(err.Error(), "dummy-secret") ||
		(len(env[field]) >= 8 && strings.Contains(err.Error(), env[field])) {
		t.Fatal("configuration error exposed a supplied value")
	}
	if got != (config{}) {
		t.Fatal("configuration error returned partially populated credentials")
	}
}

func TestRunWorkflow(t *testing.T) {
	failure := errors.New("child failure")
	for _, tc := range []struct {
		name       string
		initialize bool
		failAt     int
		phases     []string
	}{
		{"reuse", false, 0, []string{"run"}},
		{"initialize", true, 0, []string{"prepare", "run"}},
		{"preparation fails", true, 1, []string{"prepare"}},
		{"benchmark fails", true, 2, []string{"prepare", "run"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testConfig(t)
			c.duration, c.threads, c.tables, c.tableSize, c.initialize = 7, 4, 2, 500, tc.initialize
			var output bytes.Buffer
			var calls [][]string
			err := run(context.Background(), c, "/private/connection.conf", func(_ context.Context, args []string, stdout io.Writer) error {
				calls = append(calls, args)
				fmt.Fprintln(stdout, args[len(args)-1])
				if len(calls) == tc.failAt {
					return failure
				}
				return nil
			}, &output)
			if len(calls) != len(tc.phases) || errors.Is(err, failure) != (tc.failAt > 0) {
				t.Fatalf("unexpected invocation count or error: %v", err)
			}
			for i, phase := range tc.phases {
				threads := "4"
				if phase == "prepare" {
					threads = "1"
				}
				want := []string{
					"--config-file=/private/connection.conf", "--db-driver=mysql",
					"--threads=" + threads, "--tables=2", "--table-size=500", "--time=7",
					"--events=0", "--report-interval=0", "--db-debug=off", "--mysql-debug=off",
					"oltp_read_write", phase,
				}
				if !reflect.DeepEqual(calls[i], want) || strings.Contains(strings.Join(calls[i], " "), c.password) {
					t.Fatal("wrong arguments or credentials present in command arguments")
				}
			}
			if output.String() != strings.Join(tc.phases, "\n")+"\n" {
				t.Fatal("subprocess output was not streamed")
			}
		})
	}
}

func TestRunCancellation(t *testing.T) {
	for _, cancelAt := range []int{0, 1, 2} {
		t.Run(strconv.Itoa(cancelAt), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelAt == 0 {
				cancel()
			}
			c := testConfig(t)
			c.initialize = true
			calls := 0
			err := run(ctx, c, "/private/connection.conf", func(context.Context, []string, io.Writer) error {
				calls++
				if calls == cancelAt {
					cancel()
				}
				return nil
			}, io.Discard)
			if !errors.Is(err, context.Canceled) || calls != cancelAt {
				t.Fatal("cancelled execution started another phase or lost cancellation")
			}
		})
	}
}

func TestCommandEnvironment(t *testing.T) {
	env := []string{"LC_ALL=fr_FR.UTF-8", "LC_ALL=de_DE.UTF-8", "MYSQL_PASSWORD=dummy-secret", "PATH=/bin"}
	before := append([]string(nil), env...)
	got := commandEnvironment(env)
	if !reflect.DeepEqual(got, []string{"PATH=/bin", "LC_ALL=C"}) || !reflect.DeepEqual(env, before) {
		t.Fatal("child environment did not enforce the locale, remove the password, or preserve its input")
	}
}

func TestChildProcess(t *testing.T) {
	switch os.Getenv("RUNNER_TEST_CHILD") {
	case "exit":
		fmt.Fprint(os.Stdout, "stdout/"+os.Getenv("LC_ALL"))
		fmt.Fprint(os.Stderr, "stderr")
		os.Exit(7)
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
		fmt.Fprintln(os.Stdout, "ready")
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

func TestExecutePreservesStreamsAndExitCode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	env := commandEnvironment([]string{"RUNNER_TEST_CHILD=exit", "GOCOVERDIR=" + t.TempDir()})
	err := execute(context.Background(), os.Args[0], []string{"-test.run=^TestChildProcess$"}, env, &stdout, &stderr)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("child exit code was lost: %v", err)
	}
	if stdout.String() != "stdout/C" || stderr.String() != "stderr" {
		t.Fatal("child stdout, stderr, or locale changed")
	}
}

func TestExecuteKillsChildIgnoringTermination(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	env := []string{"RUNNER_TEST_CHILD=ignore-term", "GOCOVERDIR=" + t.TempDir()}
	go func() {
		done <- execute(ctx, os.Args[0], []string{"-test.run=^TestChildProcess$"},
			env, writer, io.Discard)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("child did not exit during test cleanup")
		}
	})
	ready := make(chan bool, 1)
	go func() {
		line, err := bufio.NewReader(reader).ReadString('\n')
		ready <- err == nil && line == "ready\n"
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("child did not become ready")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child did not start")
	}
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err == nil || time.Since(start) < 4*time.Second {
			t.Fatal("child did not receive its termination grace period")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child was not killed after its termination grace period")
	}
}

func TestRunnerEntrypoint(t *testing.T) {
	if os.Getenv("RUNNER_TEST_ENTRYPOINT") == "true" {
		os.Exit(runnerMain())
	}
}

func TestRunnerEntrypointCleansUpAfterChildFailure(t *testing.T) {
	dir := t.TempDir()
	connectionPath := filepath.Join(dir, "connection-path")
	// This stub records only the config path and fails with a tool exit code.
	const stub = `#!/bin/sh
for argument do
  case "$argument" in
    --config-file=*) printf '%s' "${argument#--config-file=}" > "$RUNNER_TEST_CONNECTION_PATH" ;;
  esac
done
printf 'native output\n'
exit 7
`
	if err := os.WriteFile(filepath.Join(dir, "sysbench"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + dir, "TMPDIR=" + dir, "RUNNER_TEST_ENTRYPOINT=true",
		"RUNNER_TEST_CONNECTION_PATH=" + connectionPath, "GOCOVERDIR=" + dir,
	}
	for key, value := range validEnvironment() {
		env = append(env, key+"="+value)
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
		t.Fatal("entrypoint did not preserve sysbench's exit code")
	}
	if stdout.String() != "native output\n" || stderr.String() != "runner error: sysbench execution failed\n" {
		t.Fatal("entrypoint output changed or leaked configuration")
	}
	path, err := os.ReadFile(connectionPath)
	if err != nil {
		t.Fatal("stub was not invoked with a connection file")
	}
	if _, err := os.Stat(filepath.Dir(string(path))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("entrypoint left its connection configuration directory behind")
	}
}
