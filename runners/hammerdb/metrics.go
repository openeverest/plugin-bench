package main

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const (
	terminationLogPath  = "/dev/termination-log"
	maxTerminationBytes = 4096
	maxSummaryLineBytes = 4096
)

// These types represent the shared version-1 runner result format.
// They are defined locally so this standalone runner does not depend
// on backend packages.
type metric struct {
	Name  string  `json:"name"`
	Unit  string  `json:"unit"`
	Value float64 `json:"value"`
}

type resultRecord struct {
	Version int      `json:"version"`
	Metrics []metric `json:"metrics"`
}

// summaryCollector observes only measured stdout. The caller supplies the
// authorized engine; results from another engine or duplicate summaries fail.
type summaryCollector struct {
	engine                                string
	line                                  []byte
	discarding, invalid, haveResult       bool
	nopm, tpm                             float64
	timing                                []byte
	timingOpen, timingSeen, timingInvalid bool
	timingJob                             string
	latencies                             []metric
}

var throughputLine = regexp.MustCompile(`^(?:Vuser 1:\s*)?TEST RESULT : System achieved (\S+) NOPM from (\S+) (PostgreSQL|MySQL) TPM$`)

func (s *summaryCollector) Write(data []byte) (int, error) {
	if len(data) > 0 && s.line == nil {
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
			if s.timingOpen {
				s.timingInvalid = true
			} else {
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
	if strings.HasPrefix(line, "RUNNER TIMING BEGIN ") {
		if s.timingSeen {
			s.timingInvalid = true
		}
		s.timingSeen, s.timingOpen = true, true
		s.timingJob = strings.TrimPrefix(line, "RUNNER TIMING BEGIN ")
		return
	}
	if line == "RUNNER TIMING END" {
		if !s.timingOpen {
			s.timingInvalid = true
		}
		s.timingOpen = false
		return
	}
	if s.timingOpen {
		if len(s.timing)+len(line)+1 > 16384 {
			s.timingInvalid = true
			return
		}
		s.timing = append(s.timing, line...)
		s.timing = append(s.timing, '\n')
		return
	}
	if !strings.Contains(line, "TEST RESULT") {
		return
	}
	match := throughputLine.FindStringSubmatch(line)
	if s.haveResult || match == nil {
		s.invalid = true
		return
	}
	expected := map[string]string{"postgresql": "PostgreSQL", "mysql": "MySQL"}[s.engine]
	if expected == "" || match[3] != expected {
		s.invalid = true
		return
	}
	var ok bool
	s.nopm, ok = summaryNumber(match[1])
	if !ok {
		s.invalid = true
	}
	s.tpm, ok = summaryNumber(match[2])
	if !ok {
		s.invalid = true
	}
	s.haveResult = true
}

func summaryNumber(text string) (float64, bool) {
	value, err := strconv.ParseFloat(text, 64)
	return value, err == nil && value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// flush consumes a final unterminated line before timing and result collection.
func (s *summaryCollector) flush() {
	if len(s.line) > 0 && !s.discarding {
		s.parseLine()
		s.line = s.line[:0]
	}
}

func (s *summaryCollector) result() (resultRecord, error) {
	if s.invalid || !s.haveResult {
		return resultRecord{}, errors.New("HammerDB summary metrics are unavailable")
	}
	metrics := []metric{
		{Name: "nopm", Unit: "orders/min", Value: s.nopm},
		{Name: "tpm", Unit: "tx/min", Value: s.tpm},
	}
	if !s.timingOpen && !s.timingInvalid {
		metrics = append(metrics, s.latencies...)
	}
	return resultRecord{Version: 1, Metrics: metrics}, nil
}

// Timing is optional and must belong to the completed measured job. Parse the
// pinned Jobs JSON strings without allowing malformed timing to discard throughput.
func (s *summaryCollector) collectTiming(job string) {
	if !s.timingSeen || s.timingOpen || s.timingInvalid || s.timingJob != job {
		return
	}
	var rows map[string]struct {
		Average string `json:"avg_ms"`
	}
	if json.Unmarshal(s.timing, &rows) != nil {
		return
	}
	var metrics []metric
	for _, entry := range []struct{ operation, name string }{
		{"NEWORD", "latency_new_order_avg"}, {"PAYMENT", "latency_payment_avg"},
		{"DELIVERY", "latency_delivery_avg"}, {"OSTAT", "latency_order_status_avg"}, {"SLEV", "latency_stock_level_avg"},
	} {
		row, exists := rows[entry.operation]
		if !exists {
			continue
		}
		value, ok := summaryNumber(row.Average)
		if !ok {
			return
		}
		metrics = append(metrics, metric{Name: entry.name, Unit: "ms", Value: value})
	}
	s.latencies = metrics
}

func writeTerminationResult(path string, record resultRecord) error {
	if record.Version != 1 || len(record.Metrics) == 0 {
		return errors.New("invalid structured metrics record")
	}
	seen := make(map[[2]string]struct{}, len(record.Metrics))
	for _, m := range record.Metrics {
		key := [2]string{m.Name, m.Unit}
		if _, exists := seen[key]; exists || strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Unit) == "" || m.Value < 0 || math.IsNaN(m.Value) || math.IsInf(m.Value, 0) {
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
