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

func TestSummaryCollectorParsesFinalGoYCSBMetrics(t *testing.T) {
	data, err := os.ReadFile("testdata/run-v1.0.3.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := resultRecord{Version: 1, Metrics: []metric{
		{Name: "ops_per_second", Unit: "ops/s", Value: 1648.9},
		{Name: "latency_avg", Unit: "ms", Value: 1.192},
	}}
	for _, chunkSize := range []int{1, 7, len(data)} {
		var collector summaryCollector
		var logs bytes.Buffer
		writer := io.MultiWriter(&logs, &collector)
		for offset := 0; offset < len(data); offset += chunkSize {
			end := min(offset+chunkSize, len(data))
			chunk := data[offset:end]
			if n, err := writer.Write(chunk); err != nil || n != len(chunk) {
				t.Fatalf("collector interrupted stdout: %d, %v", n, err)
			}
		}
		got, err := collector.result()
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("chunk size %d: got %+v, %v", chunkSize, got, err)
		}
		if !bytes.Equal(logs.Bytes(), data) {
			t.Fatal("collector changed stdout")
		}
	}
}

func TestSummaryCollectorAcceptsZeroAndFinalPartialLine(t *testing.T) {
	var collector summaryCollector
	io.WriteString(&collector, "Run finished, takes 1s\r\nTOTAL - OPS: 0, Avg(us): 0")
	got, err := collector.result()
	want := resultRecord{Version: 1, Metrics: []metric{
		{Name: "ops_per_second", Unit: "ops/s", Value: 0},
		{Name: "latency_avg", Unit: "ms", Value: 0},
	}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("zero metrics or final partial line rejected: %+v, %v", got, err)
	}
}

func TestSummaryCollectorRejectsUnavailableOrMalformedMetrics(t *testing.T) {
	marker := "Run finished, takes 1s\n"
	total := "TOTAL - OPS: 100, Avg(us): 3200\n"
	inputs := []string{
		"", total, marker, marker + "READ - OPS: 100, Avg(us): 3200\n",
		marker + "TOTAL_ERROR - OPS: 100, Avg(us): 3200\n",
		marker + "TOTAL - OPS: 100\n", marker + "TOTAL - Avg(us): 3200\n",
		marker + total + total, marker + total + marker,
		marker + "TOTAL - OPS: 1, OPS: 2, Avg(us): 3200\n",
		marker + "TOTAL - OPS: NaN, OPS: 2, Avg(us): 3200\n",
		marker + "TOTAL - OPS: 1, Avg(us): 1, Avg(us): 2\n",
		marker + "TOTAL - OPS: 1,23, Avg(us): 3200\n",
	}
	for _, value := range []string{"NaN", "+Inf", "-Inf", "1e999", "-1", "private-sentinel", "", "1 trailing"} {
		inputs = append(inputs,
			marker+"TOTAL - OPS: "+value+", Avg(us): 3200\n",
			marker+"TOTAL - OPS: 100, Avg(us): "+value+"\n",
		)
	}
	for _, input := range inputs {
		var collector summaryCollector
		if n, err := io.WriteString(&collector, input); err != nil || n != len(input) {
			t.Fatal("invalid metrics interrupted logging")
		}
		got, err := collector.result()
		if err == nil || !reflect.DeepEqual(got, resultRecord{}) || strings.Contains(err.Error(), "private-sentinel") {
			t.Fatal("invalid summary must return no metrics and a safe error")
		}
	}
}

func TestSummaryCollectorRejectsFailedRunFixtures(t *testing.T) {
	for _, name := range []string{"missing-data", "duplicate-load"} {
		data, err := os.ReadFile("testdata/" + name + "-v1.0.3.txt")
		if err != nil {
			t.Fatal(err)
		}
		var collector summaryCollector
		collector.Write(data)
		if _, err := collector.result(); err == nil {
			t.Fatalf("accepted metrics from %s", name)
		}
	}
}

func TestSummaryCollectorBoundsStorageAndRecoversAfterLongNoise(t *testing.T) {
	marker := "Run finished, takes 1s\n"
	total := "TOTAL - OPS: 100, Avg(us): 3200\n"
	var collector summaryCollector
	noise := strings.Repeat("x", maxSummaryLineBytes*3)
	collector.Write([]byte(noise))
	if len(collector.line) > maxSummaryLineBytes || cap(collector.line) > maxSummaryLineBytes {
		t.Fatal("collector retained an oversized line")
	}
	io.WriteString(&collector, total+marker+total)
	if _, err := collector.result(); err != nil {
		t.Fatalf("oversized progress noise prevented final metrics: %v", err)
	}
	for _, suffix := range []string{"", "\n" + total} {
		var invalid summaryCollector
		io.WriteString(&invalid, marker+"TOTAL - OPS: 100, Avg(us): 3200, Extra: "+noise+suffix)
		if _, err := invalid.result(); err == nil {
			t.Fatal("accepted an oversized final summary or a replacement after it")
		}
	}
}

func TestTerminationResultMatchesSharedFixture(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("..", "..", "testdata", "runner-result-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The shared fixture checks the generic wire format; go-ycsb's collector
	// will supply its own metric names and units.
	record := resultRecord{Version: 1, Metrics: []metric{
		{Name: "tps", Unit: "tx/s", Value: 1234.5},
		{Name: "latency_avg", Unit: "ms", Value: 3.2},
	}}
	path := filepath.Join(t.TempDir(), "termination-log")
	if err := os.WriteFile(path, []byte("old contents"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeTerminationResult(path, record); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(fixture)) {
		t.Fatalf("termination record differs from the shared contract: %s, %v", got, err)
	}
}

func TestTerminationResultRejectsInvalidRecordsBeforeWriting(t *testing.T) {
	validMetric := metric{Name: "ops_per_second", Unit: "ops/s", Value: 1}
	for _, record := range []resultRecord{
		{}, {Version: 2, Metrics: []metric{validMetric}}, {Version: 1},
		{Version: 1, Metrics: []metric{{Name: "", Unit: "ms", Value: 1}}},
		{Version: 1, Metrics: []metric{{Name: "latency_avg", Unit: " ", Value: 1}}},
		{Version: 1, Metrics: []metric{{Name: "latency_avg", Unit: "ms", Value: math.NaN()}}},
		{Version: 1, Metrics: []metric{{Name: "latency_avg", Unit: "ms", Value: math.Inf(1)}}},
		{Version: 1, Metrics: []metric{{Name: "latency_avg", Unit: "ms", Value: math.Inf(-1)}}},
		{Version: 1, Metrics: []metric{{Name: "latency_avg", Unit: "ms", Value: -1}}},
		{Version: 1, Metrics: []metric{validMetric, validMetric}},
		{Version: 1, Metrics: []metric{{Name: strings.Repeat("private-sentinel", maxTerminationBytes), Unit: "ms", Value: 1}}},
	} {
		path := filepath.Join(t.TempDir(), "termination-log")
		if err := os.WriteFile(path, []byte("unchanged"), 0600); err != nil {
			t.Fatal(err)
		}
		err := writeTerminationResult(path, record)
		if err == nil || strings.Contains(err.Error(), "private-sentinel") {
			t.Fatalf("expected a safe validation error, got %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "unchanged" {
			t.Fatal("invalid metrics must not truncate the termination file")
		}
	}
}

func TestTerminationResultSizeBoundary(t *testing.T) {
	record := resultRecord{Version: 1, Metrics: []metric{{Name: "x", Unit: "ops/s", Value: 0}}}
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
	want, err := os.ReadFile(path)
	if err != nil || len(want) != maxTerminationBytes {
		t.Fatalf("expected a 4096-byte record, got %d bytes: %v", len(want), err)
	}
	record.Metrics[0].Name += "x"
	if err := writeTerminationResult(path, record); err == nil {
		t.Fatal("accepted a record larger than 4096 bytes")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("oversized record changed the existing file")
	}
}

func TestTerminationResultHandlesUnavailableFile(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	record := resultRecord{Version: 1, Metrics: []metric{{Name: "ops_per_second", Unit: "ops/s", Value: 0}}}
	for _, path := range []string{missing, dir, ""} {
		if err := writeTerminationResult(path, record); err == nil || strings.Contains(err.Error(), dir) {
			t.Fatalf("expected a safe file error, got %v", err)
		}
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("writer must not create a missing termination file")
	}
}
