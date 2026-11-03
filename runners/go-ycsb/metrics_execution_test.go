package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRunWithMetricsCollectsOnlyMeasuredStdout(t *testing.T) {
	load, err := os.ReadFile("testdata/load-v1.0.3.txt")
	if err != nil {
		t.Fatal(err)
	}
	measured, err := os.ReadFile("testdata/run-v1.0.3.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, initialize := range []bool{false, true} {
		t.Run(map[bool]string{false: "reuse", true: "load-and-run"}[initialize], func(t *testing.T) {
			c, err := configFromEnvironment(map[string]string{"MONGODB_URI": "mongodb://bench:1648@localhost/ycsb"})
			if err != nil {
				t.Fatal(err)
			}
			c.initialize, c.recordCount = initialize, 1000
			path := filepath.Join(t.TempDir(), "termination-log")
			if err := os.WriteFile(path, []byte("old contents"), 0600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			var phases []string
			err = runWithMetrics(context.Background(), c, func(_ context.Context, args []string, out, errOut io.Writer) error {
				phases = append(phases, args[0])
				io.WriteString(out, c.uri+"\n")
				if args[0] == "load" {
					_, err := out.Write(load)
					return err
				}
				// Conflicting stderr metrics must never reach the collector.
				io.WriteString(errOut, "Run finished, takes 1s\nTOTAL - OPS: 999, Avg(us): 999000\n")
				_, err := out.Write(bytes.TrimSuffix(measured, []byte("\n")))
				return err
			}, &stdout, &stderr, path)
			if err != nil {
				t.Fatal(err)
			}
			wantPhases := []string{"run"}
			if initialize {
				wantPhases = []string{"load", "run"}
			}
			if !reflect.DeepEqual(phases, wantPhases) {
				t.Fatalf("wrong execution phases: %v", phases)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var record resultRecord
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			want := resultRecord{Version: 1, Metrics: []metric{
				{Name: "ops_per_second", Unit: "ops/s", Value: 1648.9},
				{Name: "latency_avg", Unit: "ms", Value: 1.192},
			}}
			if !reflect.DeepEqual(record, want) {
				t.Fatalf("metrics were contaminated by load, stderr or redaction: %+v", record)
			}
			if strings.Contains(stdout.String(), "1648") || strings.Contains(stdout.String(), "properties") ||
				!strings.Contains(stdout.String(), "[REDACTED]") || strings.Contains(stderr.String(), "runner warning:") {
				t.Fatal("metrics collection bypassed redaction or changed log handling")
			}
		})
	}
}

const testLoadSummary = "Run finished, takes 1s\nINSERT - Takes(s): 1, Count: 10\nTOTAL - OPS: 10, Avg(us): 1000\n"
const testRunSummary = "Run finished, takes 1s\nTOTAL - OPS: 100, Avg(us): 3200\n"

func TestRunWithMetricsSkipsPublicationOnFailureOrCancellation(t *testing.T) {
	for _, scenario := range []string{"load failure", "run failure", "incomplete load", "load operation error", "run operation error", "stderr operation error", "cancel before start", "cancel after load", "cancel after run"} {
		t.Run(scenario, func(t *testing.T) {
			c, _ := configFromEnvironment(validEnvironment())
			c.initialize, c.recordCount = true, 10
			path := filepath.Join(t.TempDir(), "termination-log")
			if err := os.WriteFile(path, []byte("unchanged"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "cancel before start" {
				cancel()
			}
			cause := errors.New("child failure: " + c.uri)
			calls := 0
			var stdout, stderr bytes.Buffer
			err := runWithMetrics(ctx, c, func(_ context.Context, args []string, out, errOut io.Writer) error {
				calls++
				if args[0] == "load" {
					if scenario == "incomplete load" {
						io.WriteString(out, "Run finished, takes 1s\nINSERT - Takes(s): 1, Count: 9\n")
					} else {
						io.WriteString(out, testLoadSummary)
					}
					if scenario == "load operation error" {
						io.WriteString(out, "INSERT_ERROR - Takes(s): 1, Count: 1\n")
					}
					if scenario == "load failure" {
						return cause
					}
					if scenario == "cancel after load" {
						cancel()
					}
				} else {
					io.WriteString(out, testRunSummary)
					switch scenario {
					case "run failure":
						return cause
					case "run operation error":
						io.WriteString(out, "READ_ERROR - Takes(s): 1, Count: 1\n")
					case "stderr operation error":
						io.WriteString(errOut, "UPDATE_ERROR - Takes(s): 1, Count: 1\n")
					case "cancel after run":
						cancel()
					}
				}
				return nil
			}, &stdout, &stderr, path)
			if err == nil || strings.Contains(err.Error(), c.uri) {
				t.Fatal("workload failure/cancellation must remain a safe failure")
			}
			if strings.HasPrefix(scenario, "cancel ") && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation cause was lost")
			}
			if (scenario == "load failure" || scenario == "run failure") && !errors.Is(err, cause) {
				t.Fatal("child failure cause was lost")
			}
			wantCalls := 2
			if scenario == "cancel before start" {
				wantCalls = 0
			} else if strings.HasPrefix(scenario, "load ") || scenario == "incomplete load" || scenario == "cancel after load" {
				wantCalls = 1
			}
			data, readErr := os.ReadFile(path)
			if calls != wantCalls || readErr != nil || string(data) != "unchanged" || strings.Contains(stderr.String(), "runner warning:") {
				t.Fatal("failed execution continued or attempted to publish metrics")
			}
		})
	}
}

func TestRunWithMetricsFailuresAreNonfatal(t *testing.T) {
	for _, scenario := range []string{"missing summary", "invalid summary", "oversized summary", "missing file", "unwritable file"} {
		t.Run(scenario, func(t *testing.T) {
			c, _ := configFromEnvironment(validEnvironment())
			dir := t.TempDir()
			path := filepath.Join(dir, "termination-log")
			if scenario == "unwritable file" {
				path = dir
			} else if scenario != "missing file" {
				if err := os.WriteFile(path, []byte("unchanged"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var stdout, stderr bytes.Buffer
			err := runWithMetrics(context.Background(), c, func(_ context.Context, _ []string, out, _ io.Writer) error {
				text := testRunSummary
				switch scenario {
				case "missing summary":
					text = "benchmark completed\n"
				case "invalid summary":
					text = "Run finished, takes 1s\nTOTAL - OPS: private-sentinel, Avg(us): 3200\n"
				case "oversized summary":
					text = "Run finished, takes 1s\nTOTAL - OPS: 100, Avg(us): 3200, Extra: " + strings.Repeat("x", maxSummaryLineBytes) + "\n"
				}
				_, err := io.WriteString(out, text)
				return err
			}, &stdout, &stderr, path)
			if err != nil || stderr.String() != "runner warning: structured metrics unavailable\n" {
				t.Fatalf("optional metrics failure changed success or exposed details: %v, %q", err, stderr.String())
			}
			if scenario == "missing file" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("metrics writing created a missing termination file")
				}
			} else if scenario != "unwritable file" {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "unchanged" {
					t.Fatal("invalid metrics changed the termination record")
				}
			}
		})
	}
}
