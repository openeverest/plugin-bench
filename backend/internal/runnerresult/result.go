// Package runnerresult decodes versioned metrics records emitted by benchmark runners.
package runnerresult

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
)

// Kubernetes limits an individual container's termination message to 4 KiB.
const maxRecordBytes = 4096

// Metric is a named numeric measurement. Names and units are supplied by the
// runner; shared backend code does not interpret their tool-specific meaning.
type Metric struct {
	Name  string  `json:"name"`
	Unit  string  `json:"unit"`
	Value float64 `json:"value"`
}

// Decode accepts one version-1 JSON record and returns its metrics in order.
// Invalid records return no partial metrics. Errors never include the message,
// since even malformed runner metadata may contain credentials.
func Decode(message string) ([]Metric, error) {
	if len(message) > maxRecordBytes {
		return nil, errors.New("runner metrics record exceeds 4096 bytes")
	}
	var record struct {
		Version int `json:"version"`
		Metrics []struct {
			Name  string   `json:"name"`
			Unit  string   `json:"unit"`
			Value *float64 `json:"value"`
		} `json:"metrics"`
	}
	decoder := json.NewDecoder(strings.NewReader(message))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return nil, errors.New("invalid runner metrics record")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("runner metrics message must contain exactly one JSON record")
	}
	if record.Version != 1 {
		return nil, errors.New("unsupported runner metrics version")
	}
	if len(record.Metrics) == 0 {
		return nil, errors.New("runner metrics record must contain metrics")
	}
	metrics := make([]Metric, 0, len(record.Metrics))
	seen := make(map[[2]string]struct{}, len(record.Metrics))
	for _, item := range record.Metrics {
		// A pointer distinguishes a valid numeric zero from missing/null values.
		if strings.TrimSpace(item.Name) == "" || strings.TrimSpace(item.Unit) == "" || item.Value == nil {
			return nil, errors.New("runner metric requires a name, unit, and numeric value")
		}
		if math.IsNaN(*item.Value) || math.IsInf(*item.Value, 0) {
			return nil, errors.New("runner metric value must be finite")
		}
		key := [2]string{item.Name, item.Unit}
		if _, exists := seen[key]; exists {
			return nil, errors.New("duplicate runner metric name and unit")
		}
		seen[key] = struct{}{}
		metrics = append(metrics, Metric{Name: item.Name, Unit: item.Unit, Value: *item.Value})
	}
	return metrics, nil
}
