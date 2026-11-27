package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
)

var errUnansweredRequests = errors.New("Memtier measurement has unanswered requests")

const (
	terminationLogPath  = "/dev/termination-log"
	maxTerminationBytes = 4096
	maxMemtierJSONBytes = 4 << 20
)

// These types represent the shared version-1 runner result format. They are
// kept local because the Valkey runner is a standalone Go module.
type metric struct {
	Name  string  `json:"name"`
	Unit  string  `json:"unit"`
	Value float64 `json:"value"`
}

type resultRecord struct {
	Version int      `json:"version"`
	Metrics []metric `json:"metrics"`
}

type memtierSummary struct {
	Runtime struct {
		UnansweredRequests uint64 `json:"Unanswered requests"`
	} `json:"Runtime"`
	AllStats struct {
		Totals struct {
			OpsPerSecond   *float64 `json:"Ops/sec"`
			AverageLatency *float64 `json:"Average Latency"`
			Percentiles    struct {
				P95 *float64 `json:"p95.00"`
				P99 *float64 `json:"p99.00"`
			} `json:"Percentile Latencies"`
		} `json:"Totals"`
		Gets struct {
			OpsPerSecond    *float64 `json:"Ops/sec"`
			HitsPerSecond   *float64 `json:"Hits/sec"`
			MissesPerSecond *float64 `json:"Misses/sec"`
		} `json:"Gets"`
		Sets struct {
			OpsPerSecond *float64 `json:"Ops/sec"`
		} `json:"Sets"`
	} `json:"ALL STATS"`
}

// parseMemtierMetrics extracts only the measured summary fields needed by the
// plugin. Pointer fields distinguish a legitimate zero (such as zero misses)
// from a missing field in Memtier's JSON.
func parseMemtierMetrics(data []byte) (resultRecord, error) {
	var summary memtierSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		return resultRecord{}, errors.New("Memtier summary metrics are unavailable")
	}
	if count := summary.Runtime.UnansweredRequests; count > 0 {
		return resultRecord{}, fmt.Errorf("%w (%d)", errUnansweredRequests, count)
	}

	values := []*float64{
		summary.AllStats.Totals.OpsPerSecond,
		summary.AllStats.Totals.AverageLatency,
		summary.AllStats.Totals.Percentiles.P95,
		summary.AllStats.Totals.Percentiles.P99,
		summary.AllStats.Gets.OpsPerSecond,
		summary.AllStats.Gets.HitsPerSecond,
		summary.AllStats.Gets.MissesPerSecond,
		summary.AllStats.Sets.OpsPerSecond,
	}
	for _, value := range values {
		if value == nil || *value < 0 || math.IsNaN(*value) || math.IsInf(*value, 0) {
			return resultRecord{}, errors.New("Memtier summary metrics are incomplete or invalid")
		}
	}
	if *summary.AllStats.Totals.Percentiles.P95 > *summary.AllStats.Totals.Percentiles.P99 {
		return resultRecord{}, errors.New("Memtier summary percentiles are invalid")
	}

	return resultRecord{Version: 1, Metrics: []metric{
		{Name: "ops_per_second", Unit: "ops/s", Value: *summary.AllStats.Totals.OpsPerSecond},
		{Name: "latency_avg", Unit: "ms", Value: *summary.AllStats.Totals.AverageLatency},
		{Name: "latency_p95", Unit: "ms", Value: *summary.AllStats.Totals.Percentiles.P95},
		{Name: "latency_p99", Unit: "ms", Value: *summary.AllStats.Totals.Percentiles.P99},
		{Name: "get_ops_per_second", Unit: "ops/s", Value: *summary.AllStats.Gets.OpsPerSecond},
		{Name: "set_ops_per_second", Unit: "ops/s", Value: *summary.AllStats.Sets.OpsPerSecond},
		{Name: "get_hits_per_second", Unit: "ops/s", Value: *summary.AllStats.Gets.HitsPerSecond},
		{Name: "get_misses_per_second", Unit: "ops/s", Value: *summary.AllStats.Gets.MissesPerSecond},
	}}, nil
}

func publishMetrics(resultPath, terminationPath string) error {
	file, err := os.Open(resultPath)
	if err != nil {
		return errors.New("could not open the Memtier result file")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxMemtierJSONBytes+1))
	if err != nil || len(data) > maxMemtierJSONBytes {
		return errors.New("could not read the Memtier result within the size limit")
	}
	record, err := parseMemtierMetrics(data)
	if err != nil {
		return err
	}
	return writeTerminationResult(terminationPath, record)
}

func writeTerminationResult(path string, record resultRecord) error {
	if record.Version != 1 || len(record.Metrics) == 0 {
		return errors.New("invalid structured metrics record")
	}
	seen := make(map[[2]string]struct{}, len(record.Metrics))
	for _, m := range record.Metrics {
		key := [2]string{m.Name, m.Unit}
		if _, exists := seen[key]; exists || strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Unit) == "" ||
			m.Value < 0 || math.IsNaN(m.Value) || math.IsInf(m.Value, 0) {
			return errors.New("invalid structured metrics record")
		}
		seen[key] = struct{}{}
	}
	data, err := json.Marshal(record)
	if err != nil || len(data) > maxTerminationBytes {
		return errors.New("structured metrics record exceeds the supported format or size")
	}
	// Kubernetes supplies the file. Validate the record before truncating it.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return errors.New("could not open the termination message file")
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("could not write the termination message file")
	}
	return nil
}
