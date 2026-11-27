package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const memtierFixture = "testdata/memtier-2.6.1.json"

func TestPublishMetricsRejectsUnansweredRequests(t *testing.T) {
	data, err := os.ReadFile(memtierFixture)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	resultPath := filepath.Join(dir, "memtier.json")
	terminationPath := filepath.Join(dir, "termination-log")
	input := strings.Replace(string(data), `"Unanswered requests": 0`, `"Unanswered requests": 3`, 1)
	if err := os.WriteFile(resultPath, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(terminationPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := publishMetrics(resultPath, terminationPath); !errors.Is(err, errUnansweredRequests) {
		t.Fatalf("expected an unanswered requests error, got %v", err)
	}
	data, err = os.ReadFile(terminationPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("unexpected metrics for incomplete measurement: %s", data)
	}
}

func TestPublishMetrics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination-log")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := publishMetrics(memtierFixture, path); err != nil {
		t.Fatal(err)
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
		{Name: "ops_per_second", Unit: "ops/s", Value: 9630.35},
		{Name: "latency_avg", Unit: "ms", Value: 0.204},
		{Name: "latency_p95", Unit: "ms", Value: 0.375},
		{Name: "latency_p99", Unit: "ms", Value: 0.687},
		{Name: "get_ops_per_second", Unit: "ops/s", Value: 8666.78},
		{Name: "set_ops_per_second", Unit: "ops/s", Value: 963.57},
		{Name: "get_hits_per_second", Unit: "ops/s", Value: 8666.78},
		{Name: "get_misses_per_second", Unit: "ops/s", Value: 0},
	}}
	if !reflect.DeepEqual(record, want) || len(data) > maxTerminationBytes {
		t.Fatalf("unexpected termination record: %s", data)
	}
}

func TestParseMemtierMetricsUsesCombinedPercentiles(t *testing.T) {
	data, err := os.ReadFile(memtierFixture)
	if err != nil {
		t.Fatal(err)
	}
	// Give Totals different percentiles from both Gets and Sets.
	input := strings.Replace(string(data), `"p95.00": 0.375`, `"p95.00": 0.4`, 1)
	input = strings.Replace(input, `"p99.00": 0.687`, `"p99.00": 0.7`, 1)
	record, err := parseMemtierMetrics([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]float64)
	for _, m := range record.Metrics {
		values[m.Name] = m.Value
	}
	if values["latency_p95"] != 0.4 || values["latency_p99"] != 0.7 {
		t.Fatalf("expected combined percentiles, got p95=%v and p99=%v", values["latency_p95"], values["latency_p99"])
	}
}

func TestParseMemtierMetricsRejectsInvalidSummaries(t *testing.T) {
	data, err := os.ReadFile(memtierFixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, from, to string }{
		{"missing rate", `"Misses/sec": 0.00`, `"ignored": 0.00`},
		{"null rate", "9630.35", "null"},
		{"negative rate", "9630.35", "-1"},
		{"overflow", "9630.35", "1e999"},
		{"string rate", "9630.35", `"9630.35"`},
		{"reversed percentiles", "0.687", "0.1"},
		{"malformed JSON", "9630.35", "NaN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := strings.ReplaceAll(string(data), tc.from, tc.to)
			if _, err := parseMemtierMetrics([]byte(input)); err == nil {
				t.Fatal("expected invalid summary to be rejected")
			}
		})
	}
}
