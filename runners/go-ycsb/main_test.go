package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestBundledWorkload(t *testing.T) {
	data, err := os.ReadFile("workloads/workloada")
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatal("invalid workload property")
		}
		if _, exists := got[key]; exists {
			t.Fatal("duplicate workload property")
		}
		got[key] = value
	}
	want := map[string]string{
		"workload": "core", "readallfields": "true",
		"readproportion": "0.5", "updateproportion": "0.5",
		"insertproportion": "0", "scanproportion": "0", "readmodifywriteproportion": "0",
		"requestdistribution": "uniform", "fieldcount": "10", "fieldlength": "100",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("bundled workload must match the qualified profile without overriding per-run settings")
	}
	if reportInterval <= 0 {
		t.Fatal("report interval must be positive")
	}
}

func validEnvironment() map[string]string {
	return map[string]string{"MONGODB_URI": "mongodb://bench:dummy-secret@localhost:27017/ycsb?authSource=admin"}
}

func configFromEnvironment(env map[string]string) (config, error) {
	return loadConfig(func(key string) string { return env[key] })
}

func TestLoadConfigRequiresURI(t *testing.T) {
	if _, err := configFromEnvironment(nil); err == nil || err.Error() != "MONGODB_URI is required" {
		t.Fatal("a missing URI must be rejected")
	}
}

func TestLoadConfigPreservesURIs(t *testing.T) {
	for _, uri := range []string{
		"mongodb://localhost:27017", "mongodb://localhost:27017/", "mongodb://localhost/ycsb",
		"mongodb://bench:p%40ss%3A%2F%23%20@localhost/ycsb?authSource=admin",
		"mongodb://bench:dummy-secret@localhost/?authSource=ycsb",
		"mongodb://bench:dummy-secret@localhost/ycsb", // URI path supplies default auth database.
		"mongodb://host-a:27017,host-b:27017/ycsb?replicaSet=test&authSource=admin",
		"mongodb://localhost/y%63sb?connectTimeoutMS=3000",
	} {
		env := map[string]string{"MONGODB_URI": uri}
		got, err := configFromEnvironment(env)
		if err != nil || got.uri != uri {
			t.Fatal("valid URI or authentication settings were changed")
		}
	}
}

func TestLoadConfigRejectsInvalidURI(t *testing.T) {
	for i, uri := range []string{
		"", " ", "https://localhost/ycsb", "mongodb+srv://example.com/ycsb",
		"mongodb://", "mongodb://localhost:invalid/ycsb", "mongodb://bench:dummy-secret@localhost/%ZZ",
		"mongodb://localhost/ycsb\n", "mongodb://localhost/ycsb\x00",
		"mongodb://localhost/ycsb?authSource=", "mongodb://localhost/ycsb?connectTimeoutMS=invalid",
		"mongodb://localhost/customer_data", "mongodb://localhost/YCSB", "mongodb://localhost/admin?authSource=ycsb",
	} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			env := validEnvironment()
			env["MONGODB_URI"] = uri
			_, err := configFromEnvironment(env)
			if err == nil || strings.Contains(err.Error(), "dummy-secret") || strings.Contains(err.Error(), "customer_data") {
				t.Fatal("invalid URI must be rejected without revealing its contents")
			}
		})
	}
}

func TestLoadConfigRejectsOtherDatabase(t *testing.T) {
	_, err := configFromEnvironment(map[string]string{"MONGODB_URI": "mongodb://localhost/other"})
	if err == nil || err.Error() != "go-ycsb supports only the ycsb database" {
		t.Fatal("a different explicit database must receive the fixed-database error")
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	env := validEnvironment()
	got, err := configFromEnvironment(env)
	want := config{uri: env["MONGODB_URI"], collection: "usertable", recordCount: 10000, operationCount: 20000, threads: 1}
	if err != nil || got != want {
		t.Fatal("configuration did not preserve the URI and apply defaults")
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	env := validEnvironment()
	for key, value := range map[string]string{
		"BENCH_COLLECTION": "benchmark.records", "BENCH_RECORD_COUNT": "20",
		"BENCH_OPERATION_COUNT": "100", "BENCH_THREADS": "4", "BENCH_INITIALIZE": "true",
	} {
		env[key] = value
	}
	got, err := configFromEnvironment(env)
	want := config{uri: env["MONGODB_URI"], collection: "benchmark.records", recordCount: 20, operationCount: 100, threads: 4, initialize: true}
	if err != nil || got != want {
		t.Fatal("explicit configuration was not retained")
	}
}

func TestLoadConfigNumericLimits(t *testing.T) {
	for _, field := range []string{"BENCH_RECORD_COUNT", "BENCH_OPERATION_COUNT", "BENCH_THREADS"} {
		for _, value := range []string{"0", "-1", "1.5", "1e3", " 2", "2 ", "+2", "2147483648", "invalid"} {
			env := validEnvironment()
			env[field] = value
			if _, err := configFromEnvironment(env); err == nil || !strings.Contains(err.Error(), field) {
				t.Fatalf("%s must reject an invalid integer", field)
			}
		}
	}
	env := validEnvironment()
	for _, field := range []string{"BENCH_RECORD_COUNT", "BENCH_OPERATION_COUNT", "BENCH_THREADS"} {
		env[field] = "2147483647"
	}
	if _, err := configFromEnvironment(env); err != nil {
		t.Fatal("maximum supported integers must be accepted")
	}
	env["BENCH_OPERATION_COUNT"] = "1"
	if _, err := configFromEnvironment(env); err == nil || !strings.Contains(err.Error(), "BENCH_THREADS") {
		t.Fatal("threads cannot exceed the operation count")
	}
	// Loading will use one worker, so the measured worker count may exceed record count.
	env["BENCH_RECORD_COUNT"], env["BENCH_OPERATION_COUNT"], env["BENCH_THREADS"] = "1", "10", "2"
	env["BENCH_INITIALIZE"] = "true"
	if _, err := configFromEnvironment(env); err != nil {
		t.Fatal("measured threads need not be limited by record count")
	}
}

func TestLoadConfigInitialize(t *testing.T) {
	for _, value := range []string{"", "false", "true", "TRUE", "1", " true", "yes"} {
		env := validEnvironment()
		env["BENCH_INITIALIZE"] = value
		got, err := configFromEnvironment(env)
		valid := value == "" || value == "false" || value == "true"
		if valid && (err != nil || got.initialize != (value == "true")) || !valid && err == nil {
			t.Fatal("initialization must use strict boolean values")
		}
	}
}

func TestLoadConfigCollectionPolicy(t *testing.T) {
	for _, value := range []string{" ", "system.users", "bad$name", "bad\x00name", "bad\nname", ".bad", "bad.", "bad..name", strings.Repeat("x", 231), strings.Repeat("é", 116)} {
		env := validEnvironment()
		env["BENCH_COLLECTION"] = value
		if _, err := configFromEnvironment(env); err == nil || !strings.Contains(err.Error(), "BENCH_COLLECTION") || (strings.TrimSpace(value) != "" && strings.Contains(err.Error(), value)) {
			t.Fatal("unsupported collection must be rejected without echoing the supplied value")
		}
	}
	for _, value := range []string{"usertable", "bench.records", "bench-records", "集合", strings.Repeat("x", 230)} {
		env := validEnvironment()
		env["BENCH_COLLECTION"] = value
		got, err := configFromEnvironment(env)
		if err != nil || got.collection != value {
			t.Fatal("supported collection names must be preserved")
		}
	}
}

func TestRunPhaseArgumentsAndCleanup(t *testing.T) {
	for _, initialize := range []bool{false, true} {
		t.Run(strconv.FormatBool(initialize), func(t *testing.T) {
			parent := t.TempDir()
			t.Setenv("TMPDIR", parent)
			c, err := configFromEnvironment(validEnvironment())
			if err != nil {
				t.Fatal(err)
			}
			c.initialize, c.threads, c.collection = initialize, 2, "bench.records"
			var phases []string
			var connectionPath string
			var stdout, stderr strings.Builder
			ctx := context.Background()
			err = run(ctx, c, func(gotCtx context.Context, args []string, out, errOut io.Writer) error {
				if gotCtx != ctx {
					t.Fatal("invocation must receive the original context")
				}
				if connectionPath == "" {
					connectionPath = args[5]
				}
				threads := "2"
				if args[0] == "load" {
					threads = "1"
				}
				want := []string{args[0], "mongodb", "-P", workloadPath, "-P", connectionPath,
					"-p", "recordcount=10000", "-p", "operationcount=20000",
					"--threads", threads, "--table", "bench.records", "--interval", "10"}
				if !reflect.DeepEqual(args, want) || strings.Contains(strings.Join(args, " "), c.uri) {
					t.Fatal("phase arguments must use the profile, connection-file path and validated settings without credentials")
				}
				data, err := os.ReadFile(connectionPath)
				if err != nil || !strings.Contains(string(data), "mongodb.url=") {
					t.Fatal("connection file must exist during each invocation")
				}
				if args[0] == "load" {
					io.WriteString(out, "Run finished, takes 1s\nINSERT - Takes(s): 1, Count: 10000, OPS: 10000\n")
				}
				phases = append(phases, args[0])
				return nil
			}, &stdout, &stderr)
			want := []string{"run"}
			if initialize {
				want = []string{"load", "run"}
			}
			if err != nil || !reflect.DeepEqual(phases, want) {
				t.Fatal("incorrect phase order")
			}
			if _, err := os.Stat(filepath.Dir(connectionPath)); !os.IsNotExist(err) {
				t.Fatal("private directory must be removed after execution")
			}
		})
	}
}

func TestRunStopsAndCleansUp(t *testing.T) {
	for _, failure := range []string{"load", "run", "cancel-after-load", "cancel-before-start"} {
		t.Run(failure, func(t *testing.T) {
			parent := t.TempDir()
			t.Setenv("TMPDIR", parent)
			c, err := configFromEnvironment(validEnvironment())
			if err != nil {
				t.Fatal(err)
			}
			c.initialize = true
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failure == "cancel-before-start" {
				cancel()
			}
			childErr := errors.New("child execution failed")
			calls := 0
			err = run(ctx, c, func(_ context.Context, args []string, out, _ io.Writer) error {
				calls++
				if failure == "cancel-after-load" {
					cancel()
					return nil
				}
				if args[0] == failure {
					return childErr
				}
				if args[0] == "load" {
					io.WriteString(out, "Run finished, takes 1s\nINSERT - Takes(s): 1, Count: 10000, OPS: 10000\n")
				}
				return nil
			}, io.Discard, io.Discard)
			wantCalls := 1
			if failure == "run" {
				wantCalls = 2
			}
			if failure == "cancel-before-start" {
				wantCalls = 0
			}
			wantErr := childErr
			if strings.HasPrefix(failure, "cancel-") {
				wantErr = context.Canceled
			}
			if calls != wantCalls || !errors.Is(err, wantErr) {
				t.Fatal("execution must stop on failure/cancellation and preserve the cause")
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatal("private files must be cleaned up on failure")
			}
		})
	}
}
