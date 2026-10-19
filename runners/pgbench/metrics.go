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

// These wire types stay local to the runner's independent Go module. The
// backend decoder checks the same contract using testdata/runner-result-v1.json.
type metric struct {
	Name  string  `json:"name"`
	Unit  string  `json:"unit"`
	Value float64 `json:"value"`
}

type resultRecord struct {
	Version int      `json:"version"`
	Metrics []metric `json:"metrics"`
}

// summaryCollector observes stdout without retaining the full log. Oversized
// lines are discarded until their newline. Write always consumes all input,
// so metrics parsing cannot interrupt pgbench or the normal log stream.
type summaryCollector struct {
	line                 []byte
	discarding, invalid  bool
	tps, latency         float64
	haveTPS, haveLatency bool
}

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
			continue
		}
		s.line = append(s.line, b)
	}
	return len(data), nil
}

func (s *summaryCollector) parseLine() {
	line := strings.TrimSpace(string(s.line))
	if text, ok := strings.CutPrefix(line, "tps = "); ok {
		if s.haveTPS {
			s.invalid = true
		}
		value, suffixOK := strings.CutSuffix(text, " (without initial connection time)")
		s.tps, s.haveTPS = summaryNumber(value)
		if !suffixOK || !s.haveTPS {
			s.invalid = true
		}
	} else if text, ok := strings.CutPrefix(line, "latency average = "); ok {
		if s.haveLatency {
			s.invalid = true
		}
		text = strings.TrimSuffix(text, " (including failures)")
		value, suffixOK := strings.CutSuffix(text, " ms")
		s.latency, s.haveLatency = summaryNumber(value)
		if !suffixOK || !s.haveLatency {
			s.invalid = true
		}
	}
}

func summaryNumber(text string) (float64, bool) {
	value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	return value, err == nil && value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// result is called after the subprocess exits and stdout has been drained.
func (s *summaryCollector) result() (resultRecord, error) {
	if len(s.line) != 0 && !s.discarding {
		s.parseLine()
		s.line = s.line[:0]
	}
	if s.invalid || !s.haveTPS || !s.haveLatency {
		return resultRecord{}, errors.New("pgbench summary metrics are unavailable")
	}
	return resultRecord{Version: 1, Metrics: []metric{
		{Name: "tps", Unit: "tx/s", Value: s.tps},
		{Name: "latency_avg", Unit: "ms", Value: s.latency},
	}}, nil
}

func writeTerminationResult(path string, record resultRecord) error {
	if record.Version != 1 || len(record.Metrics) == 0 {
		return errors.New("invalid structured metrics record")
	}
	seen := make(map[[2]string]struct{}, len(record.Metrics))
	for _, m := range record.Metrics {
		key := [2]string{m.Name, m.Unit}
		if _, exists := seen[key]; exists || strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Unit) == "" || math.IsNaN(m.Value) || math.IsInf(m.Value, 0) {
			return errors.New("invalid structured metrics record")
		}
		seen[key] = struct{}{}
	}
	data, err := json.Marshal(record)
	if err != nil || len(data) > maxTerminationBytes {
		return errors.New("structured metrics record exceeds the supported format or size")
	}
	// Kubernetes supplies this file. Do not create a replacement when it is
	// absent, and validate the entire record before truncating its contents.
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
