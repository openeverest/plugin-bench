package main

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"strconv"
	"strings"
)

const (
	terminationLogPath  = "/dev/termination-log"
	maxTerminationBytes = 4096
	maxSummaryLineBytes = 4096
)

// These types represent the shared version-1 runner result format.
// They stay local so this standalone runner does not depend on backend packages.
type metric struct {
	Name  string  `json:"name"`
	Unit  string  `json:"unit"`
	Value float64 `json:"value"`
}

type resultRecord struct {
	Version int      `json:"version"`
	Metrics []metric `json:"metrics"`
}

// summaryCollector consumes one measured phase's stdout. The caller excludes
// load output and checks workload success before using these optional metrics.
type summaryCollector struct {
	line                          []byte
	discarding, invalid, finished bool
	haveTotal                     bool
	ops, latency                  float64
}

// Write always consumes its input so metrics parsing cannot interrupt logging.
func (s *summaryCollector) Write(data []byte) (int, error) {
	if len(data) != 0 && s.line == nil {
		s.line = make([]byte, 0, maxSummaryLineBytes)
	}
	for _, b := range data {
		if b == '\n' {
			if !s.discarding {
				s.parseLine()
			}
			s.line = s.line[:0]
			s.discarding = false
			continue
		}
		if s.discarding {
			continue
		}
		if len(s.line) == maxSummaryLineBytes {
			s.line = s.line[:0]
			s.discarding = true
			// A discarded final row could hide malformed or duplicate metrics.
			if s.finished {
				s.invalid = true
			}
			continue
		}
		s.line = append(s.line, b)
	}
	return len(data), nil
}

func (s *summaryCollector) parseLine() {
	line := strings.TrimSpace(string(s.line))
	if strings.HasPrefix(line, "Run finished, takes ") {
		if s.finished {
			s.invalid = true
		}
		s.finished = true
		return
	}
	label, fields, ok := strings.Cut(line, "-")
	if !s.finished || !ok || strings.TrimSpace(label) != "TOTAL" {
		return
	}
	if s.haveTotal {
		s.invalid = true
		return
	}
	s.haveTotal = true
	var haveOPS, haveLatency bool
	for _, field := range strings.Split(fields, ",") {
		key, value, ok := strings.Cut(field, ":")
		if !ok {
			s.invalid = true
			continue
		}
		switch strings.TrimSpace(key) {
		case "OPS":
			parsed, valid := summaryNumber(value)
			if haveOPS || !valid {
				s.invalid = true
			}
			s.ops, haveOPS = parsed, true
		case "Avg(us)":
			parsed, valid := summaryNumber(value)
			if haveLatency || !valid {
				s.invalid = true
			}
			s.latency, haveLatency = parsed, true
		}
	}
	if !haveOPS || !haveLatency {
		s.invalid = true
	}
}

func summaryNumber(text string) (float64, bool) {
	value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	return value, err == nil && value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// result is called once stdout has been drained, including any partial line.
func (s *summaryCollector) result() (resultRecord, error) {
	if len(s.line) != 0 && !s.discarding {
		s.parseLine()
		s.line = s.line[:0]
	}
	if s.invalid || !s.finished || !s.haveTotal {
		return resultRecord{}, errors.New("go-ycsb summary metrics are unavailable")
	}
	return resultRecord{Version: 1, Metrics: []metric{
		{Name: "ops_per_second", Unit: "ops/s", Value: s.ops},
		{Name: "latency_avg", Unit: "ms", Value: s.latency / 1000},
	}}, nil
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
	// Kubernetes supplies this file. Validate before truncating, and do not
	// create a replacement if the termination message file is absent.
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
