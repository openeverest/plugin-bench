package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func executionConfig(t *testing.T) config {
	t.Helper()
	env := validEnvironment("mysql")
	c, err := loadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRunPhases(t *testing.T) {
	for _, initialize := range []bool{false, true} {
		c := executionConfig(t)
		c.initialize = initialize
		paths, cleanup, err := writeScripts(c)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		var calls []string
		var stdout, stderr bytes.Buffer
		err = run(context.Background(), c, paths, func(_ context.Context, args []string, out, diagnostic io.Writer) error {
			if len(args) != 2 || args[0] != "auto" {
				t.Fatalf("unexpected arguments: %v", args)
			}
			phase := strings.TrimSuffix(filepath.Base(args[1]), ".tcl")
			calls = append(calls, phase)
			if _, err := os.Stat(args[1]); err != nil {
				t.Fatal(err)
			}
			io.WriteString(out, phase+" output\n")
			io.WriteString(diagnostic, phase+" diagnostic\n")
			return os.WriteFile(filepath.Join(paths.directory, phase+".done"), []byte("1 "+phase+" JOB123\n"), 0600)
		}, &stdout, &stderr)
		want := []string{"run"}
		if initialize {
			want = []string{"prepare", "run"}
		}
		if err != nil || !reflect.DeepEqual(calls, want) {
			t.Fatalf("calls=%v err=%v", calls, err)
		}
		if !strings.Contains(stdout.String(), "run output") || !strings.Contains(stderr.String(), "run diagnostic") {
			t.Fatal("lost child output")
		}
	}
}

func TestRunStopsAfterFailedPreparation(t *testing.T) {
	for _, scenario := range []string{"process", "missing", "malformed", "wrong-phase", "oversized", "symlink", "cancelled", "stale"} {
		t.Run(scenario, func(t *testing.T) {
			c := executionConfig(t)
			c.initialize = true
			paths, cleanup, err := writeScripts(c)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "stale" {
				os.WriteFile(paths.prepareDone, []byte("1 prepare OLD\n"), 0600)
			}
			calls := 0
			err = run(ctx, c, paths, func(context.Context, []string, io.Writer, io.Writer) error {
				calls++
				record := "1 prepare JOB123\n"
				switch scenario {
				case "process":
					os.WriteFile(paths.prepareDone, []byte(record), 0600)
					return errors.New("sensitive child error")
				case "missing":
					return nil
				case "malformed":
					record = "worker failed\n"
				case "wrong-phase":
					record = "1 run JOB123\n"
				case "oversized":
					record += strings.Repeat("x", 1000)
				case "symlink":
					return os.Symlink(paths.prepare, paths.prepareDone)
				case "cancelled":
					cancel()
				}
				return os.WriteFile(paths.prepareDone, []byte(record), 0600)
			}, io.Discard, io.Discard)
			if err == nil || strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("expected safe failure, got %v", err)
			}
			want := 1
			if scenario == "stale" {
				want = 0
			}
			if calls != want {
				t.Fatalf("started unexpected phases: %d", calls)
			}
		})
	}
}

func TestRunStagedExecutionAndCleanup(t *testing.T) {
	for _, scenario := range []string{"success", "failure", "cancellation"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("TMPDIR", root)
			executable := filepath.Join(t.TempDir(), "hammerdbcli")
			script := `#!/bin/sh
test "$PWD" = "$(dirname "$0")" || exit 2
test -z "${DB_PASSWORD+x}" || exit 3
test "$TMP" = "$TMPDIR" && test "$TMP" = "$TEMP" && test "$TMP" = "$HOME" || exit 4
test -d "$TMP" || exit 5
test "$1" = auto || exit 6
case "$SCENARIO" in
failure) exit 7 ;;
cancellation) trap 'exit 1' TERM; sleep 30 & wait ;;
esac
phase=$(basename "$2" .tcl)
umask 077
printf '1 %s JOB123\n' "$phase" > "$(dirname "$2")/$phase.done"
`
			if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			c := executionConfig(t)
			c.initialize = true
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if scenario == "cancellation" {
				timer := time.AfterFunc(100*time.Millisecond, cancel)
				defer timer.Stop()
			}
			err := runStaged(ctx, c, executable, append(os.Environ(), "SCENARIO="+scenario, "DB_PASSWORD=private"), io.Discard, io.Discard, filepath.Join(root, "absent"))
			if (scenario == "success") != (err == nil) {
				t.Fatalf("unexpected result: %v", err)
			}
			if scenario == "cancellation" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			entries, readErr := os.ReadDir(root)
			if readErr != nil || len(entries) != 0 {
				t.Fatal("private files left after execution")
			}
		})
	}
}

func TestRunAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := run(ctx, executionConfig(t), scriptPaths{}, func(context.Context, []string, io.Writer, io.Writer) error {
		t.Fatal("started a cancelled run")
		return nil
	}, io.Discard, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation: %v", err)
	}
}
