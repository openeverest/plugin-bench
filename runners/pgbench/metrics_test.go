package main

import (
	"bytes"
	"encoding/json"
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
	data, err := os.ReadFile(filepath.Join("testdata", "summary-pg16.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSummaryCollectorParsesPgbench16(t *testing.T) {
	input := summaryFixture(t)
	for _, chunkSize := range []int{1, 7, len(input)} {
		var collector summaryCollector
		var output bytes.Buffer
		writer := io.MultiWriter(&output, &collector)
		for offset := 0; offset < len(input); offset += chunkSize {
			end := offset + chunkSize
			if end > len(input) {
				end = len(input)
			}
			chunk := input[offset:end]
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
	input := "latency average = 0.000 ms (including failures)\r\ntps = 0.000000 (without initial connection time)"
	if _, err := io.WriteString(&collector, input); err != nil {
		t.Fatal(err)
	}
	got, err := collector.result()
	want := resultRecord{Version: 1, Metrics: []metric{{Name: "tps", Unit: "tx/s", Value: 0}, {Name: "latency_avg", Unit: "ms", Value: 0}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("result = %+v, %v; want %+v", got, err, want)
	}
}

func TestSummaryCollectorIgnoresUnrelatedAndOversizedLines(t *testing.T) {
	var collector summaryCollector
	oversized := strings.Repeat("x", maxSummaryLineBytes)
	if n, err := io.WriteString(&collector, oversized); n != len(oversized) || err != nil {
		t.Fatal("collector interrupted output")
	}
	if len(collector.line) > maxSummaryLineBytes || cap(collector.line) > 2*maxSummaryLineBytes {
		t.Fatal("collector buffered an unbounded line")
	}
	// A suffix on a discarded line must never be interpreted as a new metric.
	input := "tps = 999 (without initial connection time)\nprogress: 1.0 s, 9999.0 tps, lat 1.0 ms\n - latency average = 999 ms\n" + summaryFixture(t)
	if _, err := io.WriteString(&collector, input); err != nil {
		t.Fatal(err)
	}
	got, err := collector.result()
	if err != nil || !reflect.DeepEqual(got, expectedResult()) {
		t.Fatalf("result = %+v, %v", got, err)
	}
}

func TestSummaryCollectorRejectsUnavailableOrMalformedMetrics(t *testing.T) {
	latency := "latency average = 3.2 ms\n"
	tps := "tps = 1234.5 (without initial connection time)\n"
	inputs := []string{"", "progress: 1.0 s, 900 tps\n", latency, tps, latency + "tps = 1234.5 (including reconnection times)\n", tps + tps + latency}
	for _, value := range []string{"NaN", "+Inf", "-Inf", "1e999", "-1", "credential-sentinel", "1,23", ""} {
		inputs = append(inputs,
			latency+"tps = "+value+" (without initial connection time)\n",
			"latency average = "+value+" ms\n"+tps,
		)
	}
	inputs = append(inputs, latency+"tps = 1.0 (without initial connection time) credential-sentinel\n")
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
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(fixture)) {
		t.Fatalf("termination record %s does not match fixture %s", got, fixture)
	}
}

func TestTerminationResultRejectsInvalidRecordsBeforeWriting(t *testing.T) {
	for _, record := range []resultRecord{
		{}, {Version: 2, Metrics: expectedResult().Metrics}, {Version: 1},
		{Version: 1, Metrics: []metric{{Name: "", Unit: "ms", Value: 1}}},
		{Version: 1, Metrics: []metric{{Name: "latency", Unit: " ", Value: 1}}},
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

func TestTerminationResultAcceptsSizeBoundary(t *testing.T) {
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
}

func TestTerminationResultHandlesMissingOrUnwritableFile(t *testing.T) {
	dir := t.TempDir()
	for _, path := range []string{filepath.Join(dir, "missing"), dir, ""} {
		if err := writeTerminationResult(path, expectedResult()); err == nil || strings.Contains(err.Error(), dir) {
			t.Fatalf("expected safe file error, got %v", err)
		}
	}
}
