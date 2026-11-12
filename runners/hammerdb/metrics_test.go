package main

import (
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSummaryCollectorNativePostgreSQLFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/tprocc-postgresql-6.1.1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Phases map[string]struct {
			Result []string `json:"result"`
		} `json:"phases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	input := "Vuser 1:" + fixture.Phases["run"].Result[3]
	for _, size := range []int{1, 7, len(input)} {
		collector := summaryCollector{engine: "postgresql"}
		for offset := 0; offset < len(input); offset += size {
			io.WriteString(&collector, input[offset:min(offset+size, len(input))])
		}
		got, err := collector.result()
		want := resultRecord{Version: 1, Metrics: []metric{{"nopm", "orders/min", 3115}, {"tpm", "tx/min", 7280}}}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("result=%+v err=%v", got, err)
		}
	}
}

func TestSummaryCollectorMySQLFormat(t *testing.T) {
	// Format example, not a successful live MySQL workload fixture.
	collector := summaryCollector{engine: "mysql"}
	io.WriteString(&collector, "unrelated progress\nVuser 1:TEST RESULT : System achieved 4275 NOPM from 9936 MySQL TPM\r\n")
	record, err := collector.result()
	if err != nil || record.Metrics[0].Value != 4275 || record.Metrics[1].Value != 9936 {
		t.Fatalf("record=%+v err=%v", record, err)
	}
}

func TestSummaryCollectorRejectsInvalidAndAmbiguousResults(t *testing.T) {
	valid := "TEST RESULT : System achieved 1 NOPM from 2 PostgreSQL TPM\n"
	for _, input := range []string{
		"", "progress only\n", valid + valid,
		strings.ReplaceAll(valid, "PostgreSQL", "MySQL"),
		strings.ReplaceAll(valid, "1 NOPM", "NaN NOPM"),
		strings.ReplaceAll(valid, "1 NOPM", "-1 NOPM"),
		strings.ReplaceAll(valid, "2 PostgreSQL", "+Inf PostgreSQL"),
		strings.ReplaceAll(valid, "2 PostgreSQL", "1e999 PostgreSQL"),
		"TEST RESULT malformed\n" + valid,
		strings.Repeat("x", maxSummaryLineBytes+1) + valid,
	} {
		collector := summaryCollector{engine: "postgresql"}
		if n, err := io.WriteString(&collector, input); n != len(input) || err != nil {
			t.Fatal("collector interrupted output")
		}
		if _, err := collector.result(); err == nil {
			t.Fatal("accepted invalid or ambiguous summary")
		}
		if cap(collector.line) > maxSummaryLineBytes {
			t.Fatal("unbounded buffering")
		}
	}
	collector := summaryCollector{engine: "mysql"}
	io.WriteString(&collector, "TEST RESULT : System achieved 0 NOPM from 0 MySQL TPM")
	if _, err := collector.result(); err != nil {
		t.Fatal(err)
	}
}

func TestTerminationRecordSharedContract(t *testing.T) {
	data, err := os.ReadFile("../../testdata/runner-result-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var record resultRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "termination")
	if err := os.WriteFile(path, []byte("old record"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeTerminationResult(path, record); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got resultRecord
	if err := json.Unmarshal(written, &got); err != nil || !reflect.DeepEqual(got, record) {
		t.Fatal("changed shared wire contract")
	}
}

func TestTerminationRejectsInvalidBeforeTruncating(t *testing.T) {
	valid := metric{"nopm", "orders/min", 1}
	for _, record := range []resultRecord{
		{Version: 2, Metrics: []metric{valid}}, {Version: 1},
		{Version: 1, Metrics: []metric{valid, valid}},
		{Version: 1, Metrics: []metric{{"", "tx/min", 1}}},
		{Version: 1, Metrics: []metric{{"tpm", "", 1}}},
		{Version: 1, Metrics: []metric{{"tpm", "tx/min", -1}}},
		{Version: 1, Metrics: []metric{{"tpm", "tx/min", math.NaN()}}},
		{Version: 1, Metrics: []metric{{"tpm", "tx/min", math.Inf(1)}}},
		{Version: 1, Metrics: []metric{{strings.Repeat("x", 4096), "tx/min", 1}}},
	} {
		path := filepath.Join(t.TempDir(), "termination")
		os.WriteFile(path, []byte("original"), 0600)
		if err := writeTerminationResult(path, record); err == nil {
			t.Fatal("accepted invalid record")
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "original" {
			t.Fatal("invalid record damaged existing file")
		}
	}
	path := filepath.Join(t.TempDir(), "absent")
	if err := writeTerminationResult(path, resultRecord{Version: 1, Metrics: []metric{valid}}); err == nil {
		t.Fatal("created missing termination file")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("unexpected termination file")
	}
}

func TestTimingNativeFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/tprocc-postgresql-6.1.1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Phases map[string]struct {
			Timing json.RawMessage `json:"timing"`
		} `json:"phases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	collector := summaryCollector{engine: "postgresql"}
	input := "TEST RESULT : System achieved 3115 NOPM from 7280 PostgreSQL TPM\nRUNNER TIMING BEGIN JOB123\n" + string(fixture.Phases["run"].Timing) + "\nRUNNER TIMING END\n"
	for _, b := range []byte(input) {
		collector.Write([]byte{b})
	}
	collector.collectTiming("JOB123")
	record, err := collector.result()
	if err != nil || len(record.Metrics) != 7 {
		t.Fatalf("metrics=%+v err=%v", record, err)
	}
	want := []float64{9.61, 7.754, 11.404, 1.149, 3.771}
	for i, value := range want {
		if record.Metrics[i+2].Value != value || record.Metrics[i+2].Unit != "ms" {
			t.Fatal("incorrect transaction latency")
		}
	}
}

func TestOptionalTimingFailuresKeepThroughput(t *testing.T) {
	for _, body := range []string{
		`not json`, `{}`, `{"NEWORD":{"avg_ms":"NaN"}}`, `{"NEWORD":{"avg_ms":"-1"}}`,
		`{"NEWORD":{"avg_ms":"Infinity"}}`, `{"NEWORD":{"avg_ms":null}}`,
		`{"NEWORD":{"avg_ms":"1"},"PAYMENT":{"avg_ms":"bad"}}`,
		strings.Repeat("x", 17000),
	} {
		collector := summaryCollector{engine: "mysql"}
		io.WriteString(&collector, "TEST RESULT : System achieved 1 NOPM from 2 MySQL TPM\nRUNNER TIMING BEGIN JOB123\n"+body+"\nRUNNER TIMING END\n")
		collector.collectTiming("JOB123")
		record, err := collector.result()
		if err != nil || len(record.Metrics) != 2 {
			t.Fatalf("optional data discarded throughput: %+v %v", record, err)
		}
	}
	for _, suffix := range []string{"", "RUNNER TIMING END\n", "RUNNER TIMING END\nRUNNER TIMING BEGIN JOB123\n{}\nRUNNER TIMING END\n"} {
		collector := summaryCollector{engine: "mysql"}
		io.WriteString(&collector, "TEST RESULT : System achieved 1 NOPM from 2 MySQL TPM\nRUNNER TIMING BEGIN OTHERJOB\n{\"NEWORD\":{\"avg_ms\":\"1\"}}\n"+suffix)
		collector.collectTiming("JOB123")
		record, err := collector.result()
		if err != nil || len(record.Metrics) != 2 {
			t.Fatal("accepted incomplete, duplicate, or unrelated timing")
		}
	}
}
