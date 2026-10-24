package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func expectedResult() resultRecord {
	return resultRecord{Version: 1, Metrics: []metric{
		{Name: "tps", Unit: "tx/s", Value: 1234.5},
		{Name: "latency_avg", Unit: "ms", Value: 3.2},
	}}
}

func summaryFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "summary-sysbench-1.0.20.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSummaryCollectorParsesSysbench(t *testing.T) {
	input := summaryFixture(t)
	for _, chunkSize := range []int{1, 7, len(input)} {
		var collector summaryCollector
		var output bytes.Buffer
		writer := io.MultiWriter(&output, &collector)
		for offset := 0; offset < len(input); offset += chunkSize {
			chunk := input[offset:min(offset+chunkSize, len(input))]
			if n, err := io.WriteString(writer, chunk); err != nil || n != len(chunk) {
				t.Fatalf("Write() = %d, %v", n, err)
			}
		}
		got, err := collector.result()
		if err != nil || !reflect.DeepEqual(got, expectedResult()) {
			t.Fatalf("chunk size %d: result = %+v, %v", chunkSize, got, err)
		}
		if output.String() != input {
			t.Fatal("metrics collection changed the log stream")
		}
	}
}

func TestSummaryCollectorAcceptsZeroAndFinalLineWithoutNewline(t *testing.T) {
	var collector summaryCollector
	input := "SQL statistics:\r\n    transactions: 0 (0.00 per sec.)\r\nLatency (ms):\r\n    avg: 0.00"
	io.WriteString(&collector, input)
	got, err := collector.result()
	want := resultRecord{Version: 1, Metrics: []metric{
		{Name: "tps", Unit: "tx/s", Value: 0}, {Name: "latency_avg", Unit: "ms", Value: 0},
	}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("result = %+v, %v; want %+v", got, err, want)
	}
}

func TestSummaryCollectorIgnoresUnrelatedAndOversizedLines(t *testing.T) {
	var collector summaryCollector
	input := "avg: 999\ntransactions: 1 (999 per sec.)\n[ 5s ] thds: 2 tps: 999 lat (ms,95%): 999\n"
	io.WriteString(&collector, input)
	oversized := strings.Repeat("x", maxSummaryLineBytes)
	if n, err := io.WriteString(&collector, oversized); n != len(oversized) || err != nil {
		t.Fatal("collector interrupted output")
	}
	if len(collector.line) > maxSummaryLineBytes || cap(collector.line) > maxSummaryLineBytes {
		t.Fatal("collector buffered an unbounded line")
	}
	// The suffix of a discarded line must not become a section header.
	io.WriteString(&collector, "SQL statistics:\n    transactions: 1 (999 per sec.)\n"+summaryFixture(t)+"    avg: 999\n")
	got, err := collector.result()
	if err != nil || !reflect.DeepEqual(got, expectedResult()) {
		t.Fatalf("result = %+v, %v", got, err)
	}
}

func TestSummaryCollectorRejectsMalformedOrUnavailableMetrics(t *testing.T) {
	sql := "SQL statistics:\n    transactions: 37035 (1234.50 per sec.)\n"
	latency := "Latency (ms):\n    avg: 3.20\n"
	inputs := []string{
		"", sql, latency, sql + "avg: 3.20\n",
		sql + "Latency (s):\n    avg: 3.20\n",
		sql + "Latency (ms):\nThreads fairness:\n    avg: 3.20\n",
		sql + sql + latency, sql + latency + latency,
		"SQL statistics:\n" + sql + latency,
		sql + "Latency (ms):\n" + latency,
		sql + "    transactions: 1 (2 per sec.)\n" + latency,
		sql + latency + "    avg: 4\n",
	}
	for _, value := range []string{"NaN", "+Inf", "-Inf", "1e999", "-1", "credential-sentinel", "1,23", ""} {
		inputs = append(inputs,
			"SQL statistics:\n    transactions: 37035 ("+value+" per sec.)\n"+latency,
			sql+"Latency (ms):\n    avg: "+value+"\n",
		)
	}
	for _, text := range []string{
		"not-a-count (1234.50 per sec.)", "-1 (1234.50 per sec.)",
		"37035 1234.50 per sec.)", "37035 (1234.50 per second)",
		"37035 (1234.50 per sec.) credential-sentinel",
	} {
		inputs = append(inputs, "SQL statistics:\n    transactions: "+text+"\n"+latency)
	}
	for _, input := range inputs {
		var collector summaryCollector
		if n, err := io.WriteString(&collector, input); n != len(input) || err != nil {
			t.Fatal("malformed metrics interrupted stdout")
		}
		got, err := collector.result()
		if err == nil || !reflect.DeepEqual(got, resultRecord{}) {
			t.Fatalf("accepted invalid summary %q", input)
		}
		if strings.Contains(err.Error(), "credential-sentinel") {
			t.Fatal("parser error exposed output")
		}
	}
}

func TestTerminationResultMatchesSharedFixture(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("..", "..", "testdata", "runner-result-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "termination-log")
	if err := os.WriteFile(path, []byte("old contents"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeTerminationResult(path, expectedResult()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(fixture)) {
		t.Fatalf("termination record %s does not match fixture: %v", got, err)
	}
}

func TestTerminationResultRejectsInvalidRecordsBeforeWriting(t *testing.T) {
	for _, record := range []resultRecord{
		{}, {Version: 2, Metrics: expectedResult().Metrics}, {Version: 1},
		{Version: 1, Metrics: []metric{{Name: "", Unit: "ms", Value: 1}}},
		{Version: 1, Metrics: []metric{{Name: "tps", Unit: " ", Value: 1}}},
		{Version: 1, Metrics: []metric{{Name: "tps", Unit: "tx/s", Value: math.NaN()}}},
		{Version: 1, Metrics: []metric{{Name: "tps", Unit: "tx/s", Value: math.Inf(1)}}},
		{Version: 1, Metrics: []metric{{Name: "tps", Unit: "tx/s", Value: 1}, {Name: "tps", Unit: "tx/s", Value: 2}}},
		{Version: 1, Metrics: []metric{{Name: strings.Repeat("credential-sentinel", maxTerminationBytes), Unit: "ms", Value: 1}}},
	} {
		path := filepath.Join(t.TempDir(), "termination-log")
		if err := os.WriteFile(path, []byte("unchanged"), 0600); err != nil {
			t.Fatal(err)
		}
		err := writeTerminationResult(path, record)
		if err == nil || strings.Contains(err.Error(), "credential-sentinel") {
			t.Fatalf("expected safe validation error, got %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "unchanged" {
			t.Fatal("invalid record truncated the existing termination file")
		}
	}
}

func TestTerminationResultSizeBoundary(t *testing.T) {
	record := resultRecord{Version: 1, Metrics: []metric{{Name: "x", Unit: "ms", Value: 0}}}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	record.Metrics[0].Name = strings.Repeat("x", maxTerminationBytes-len(encoded)+1)
	path := filepath.Join(t.TempDir(), "termination-log")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeTerminationResult(path, record); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) != maxTerminationBytes {
		t.Fatalf("record size = %d, %v", len(data), err)
	}
	record.Metrics[0].Name += "x"
	if err := writeTerminationResult(path, record); err == nil {
		t.Fatal("accepted a record beyond the termination message limit")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, data) {
		t.Fatal("oversized record changed the termination file")
	}
}

func TestTerminationResultFileFailures(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	for _, path := range []string{missing, dir, ""} {
		if err := writeTerminationResult(path, expectedResult()); err == nil || strings.Contains(err.Error(), dir) {
			t.Fatalf("expected safe file error, got %v", err)
		}
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created a replacement termination file")
	}
	if _, err := os.Stat("/dev/full"); err == nil {
		if err := writeTerminationResult("/dev/full", expectedResult()); err == nil {
			t.Fatal("termination file write failure was ignored")
		}
	}
}

func TestRunWithMetricsKeepsPreparationAndLogsSeparate(t *testing.T) {
	c := testConfig(t)
	c.initialize = true
	path := filepath.Join(t.TempDir(), "termination-log")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	fixture := summaryFixture(t)
	// Metrics at the end remain available even after more output than the
	// backend's log limit, and the collector still retains only one short line.
	measured := strings.Repeat("x", 2*1024*1024) + "\n" + fixture
	err := runWithMetrics(context.Background(), c, "/private/connection.conf", func(_ context.Context, args []string, output io.Writer) error {
		// A summary in preparation output must not become duplicate metrics.
		text := fixture
		if args[len(args)-1] == "run" {
			text = measured
		}
		_, err := io.WriteString(output, text)
		return err
	}, &stdout, &stderr, path)
	if err != nil || stdout.String() != fixture+measured || stderr.Len() != 0 {
		t.Fatalf("execution failed or log streams changed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got resultRecord
	if err := json.Unmarshal(data, &got); err != nil || !reflect.DeepEqual(got, expectedResult()) {
		t.Fatalf("metrics = %+v, %v", got, err)
	}
}

func TestRunWithMetricsUnavailableIsNonfatal(t *testing.T) {
	fixture := summaryFixture(t)
	for _, tc := range []struct {
		name, output string
		missingFile  bool
	}{
		{"missing summary", "native output\n", false},
		{"invalid summary", strings.Replace(fixture, "1234.50 per sec.", "NaN per sec.", 1), false},
		{"missing file", fixture, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "termination-log")
			if !tc.missingFile {
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			var stdout, stderr bytes.Buffer
			err := runWithMetrics(context.Background(), testConfig(t), "/private/connection.conf", func(_ context.Context, _ []string, output io.Writer) error {
				_, err := io.WriteString(output, tc.output)
				return err
			}, &stdout, &stderr, path)
			if err != nil || stdout.String() != tc.output || stderr.String() != "runner warning: structured metrics unavailable\n" {
				t.Fatalf("metrics failure changed benchmark success or logs: %v", err)
			}
			data, err := os.ReadFile(path)
			if (!tc.missingFile && (err != nil || len(data) != 0)) || (tc.missingFile && !errors.Is(err, os.ErrNotExist)) {
				t.Fatal("unavailable metrics wrote a result or created a file")
			}
		})
	}
}

func TestRunWithMetricsUnwritableFileIsNonfatal(t *testing.T) {
	var stdout, stderr bytes.Buffer
	fixture := summaryFixture(t)
	// A directory cannot be opened as the writable termination file, including
	// when the tests run with elevated filesystem permissions.
	err := runWithMetrics(context.Background(), testConfig(t), "/private/connection.conf", func(_ context.Context, _ []string, output io.Writer) error {
		_, err := io.WriteString(output, fixture)
		return err
	}, &stdout, &stderr, t.TempDir())
	if err != nil || stdout.String() != fixture || stderr.String() != "runner warning: structured metrics unavailable\n" {
		t.Fatalf("termination file failure changed benchmark success or logs: %v", err)
	}
}

func TestRunWithMetricsFailureOrCancellationDoesNotPublish(t *testing.T) {
	failure := errors.New("child failed")
	fixture := summaryFixture(t)
	for _, tc := range []struct {
		name     string
		failAt   string
		cancelAt string
		wantErr  error
	}{
		{"preparation failure", "prepare", "", failure},
		{"benchmark failure", "run", "", failure},
		{"cancelled before execution", "", "before", context.Canceled},
		{"cancelled after preparation", "", "prepare", context.Canceled},
		{"cancelled after benchmark", "", "run", context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelAt == "before" {
				cancel()
			}
			c := testConfig(t)
			c.initialize = true
			path := filepath.Join(t.TempDir(), "termination-log")
			if err := os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			err := runWithMetrics(ctx, c, "/private/connection.conf", func(_ context.Context, args []string, output io.Writer) error {
				io.WriteString(output, fixture)
				phase := args[len(args)-1]
				if phase == tc.cancelAt {
					cancel()
				}
				if phase == tc.failAt {
					return failure
				}
				return nil
			}, io.Discard, &stderr, path)
			if !errors.Is(err, tc.wantErr) || stderr.Len() != 0 {
				t.Fatalf("execution error changed or metrics warning emitted: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil || len(data) != 0 {
				t.Fatal("failed or cancelled execution published success metrics")
			}
		})
	}
}
