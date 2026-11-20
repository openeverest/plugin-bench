package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunWithMetricsLifecycle(t *testing.T) {
	for _, scenario := range []string{"success", "reuse", "missing", "malformed", "duplicate", "wrong-engine", "stderr-only", "prepare-only", "process-failure", "missing-marker", "cancelled", "unwritable"} {
		t.Run(scenario, func(t *testing.T) {
			c := executionConfig(t)
			c.initialize = scenario != "reuse"
			c.password = "4275" // Logs redact this number; the raw collector must still see it.
			paths, cleanup, err := writeScripts(c)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			termination := filepath.Join(t.TempDir(), "termination")
			if err := os.WriteFile(termination, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			if scenario == "unwritable" {
				termination = filepath.Dir(termination)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var stdout, stderr bytes.Buffer
			var calls []string
			err = runWithMetrics(ctx, c, paths, func(_ context.Context, args []string, out, diagnostic io.Writer) error {
				phase := strings.TrimSuffix(filepath.Base(args[1]), ".tcl")
				calls = append(calls, phase)
				summary := "Vuser 1:TEST RESULT : System achieved 4275 NOPM from 9936 MySQL TPM\n"
				if phase == "prepare" {
					// A bogus preparation result must never contaminate the measured collector.
					io.WriteString(out, "TEST RESULT : System achieved 999 NOPM from 999 MySQL TPM\n")
				} else {
					io.WriteString(diagnostic, "password="+c.password+"\n")
					switch scenario {
					case "missing", "prepare-only":
						summary = "progress only\n"
					case "malformed":
						summary = "TEST RESULT broken\n"
					case "duplicate":
						summary += summary
					case "wrong-engine":
						summary = strings.ReplaceAll(summary, "MySQL", "PostgreSQL")
					case "stderr-only":
						io.WriteString(diagnostic, summary)
						summary = ""
					}
					// Exercise arbitrary chunk boundaries and an unterminated final line.
					if scenario == "success" {
						summary += "RUNNER TIMING BEGIN JOB123\n{\"NEWORD\":{\"avg_ms\":\"9.61\"}}\nRUNNER TIMING END\n"
					}
					summary = strings.TrimSuffix(summary, "\n")
					for _, b := range []byte(summary) {
						out.Write([]byte{b})
					}
					if scenario == "process-failure" {
						return errors.New("secret child failure")
					}
					if scenario == "missing-marker" {
						return nil
					}
					if scenario == "cancelled" {
						cancel()
					}
				}
				return os.WriteFile(filepath.Join(paths.directory, phase+".done"), []byte("1 "+phase+" JOB123\n"), 0600)
			}, &stdout, &stderr, termination)
			failed := scenario == "process-failure" || scenario == "missing-marker" || scenario == "cancelled"
			if failed != (err != nil) {
				t.Fatalf("unexpected result: %v", err)
			}
			if scenario == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal("lost cancellation")
			}
			if strings.Contains(stdout.String(), c.password) || strings.Contains(stderr.String(), c.password) {
				t.Fatal("credential reached logs")
			}
			wantCalls := 1
			if c.initialize {
				wantCalls = 2
			}
			if len(calls) != wantCalls {
				t.Fatalf("unexpected phases: %v", calls)
			}
			if scenario == "unwritable" {
				if !strings.Contains(stderr.String(), "metrics unavailable") {
					t.Fatal("missing write warning")
				}
				return
			}
			data, readErr := os.ReadFile(termination)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if scenario == "success" || scenario == "reuse" {
				var record resultRecord
				if err := json.Unmarshal(data, &record); err != nil {
					t.Fatal(err)
				}
				wantMetrics := 2
				if scenario == "success" {
					wantMetrics = 3
				}
				if len(record.Metrics) != wantMetrics || record.Metrics[0].Value != 4275 || record.Metrics[1].Value != 9936 {
					t.Fatalf("unexpected metrics: %+v", record)
				}
				if strings.Contains(stderr.String(), "metrics unavailable") {
					t.Fatal("warned on valid metrics")
				}
			} else {
				if string(data) != "original" {
					t.Fatal("failed or unavailable metrics replaced termination record")
				}
				if !failed && !strings.Contains(stderr.String(), "metrics unavailable") {
					t.Fatal("missing warning")
				}
			}
		})
	}
}

func TestStagedMetricsAndPrivateCleanup(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	executable := filepath.Join(t.TempDir(), "hammerdbcli")
	script := `#!/bin/sh
phase=$(basename "$2" .tcl)
if test "$phase" = run; then
 printf 'Vuser 1:TEST RESULT : System achieved 4275 NOPM from 9936 MySQL TPM\n'
fi
umask 077
printf '1 %s JOB123\n' "$phase" > "$(dirname "$2")/$phase.done"
`
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	termination := filepath.Join(t.TempDir(), "termination")
	if err := os.WriteFile(termination, nil, 0600); err != nil {
		t.Fatal(err)
	}
	c := executionConfig(t)
	c.initialize = true
	if err := runStaged(context.Background(), c, executable, os.Environ(), io.Discard, io.Discard, termination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(termination)
	if err != nil || !bytes.Contains(data, []byte(`"name":"nopm"`)) {
		t.Fatal("missing metrics")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("private files remain")
	}
}
