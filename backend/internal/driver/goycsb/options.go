// Package goycsb prepares MongoDB workloads for the go-ycsb runner.
package goycsb

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

const benchmarkDatabase = "ycsb"

// Options contains the configurable settings accepted by the go-ycsb runner.
// Workload A itself remains fixed in the runner image.
type Options struct {
	Collection     string `json:"collection"`
	RecordCount    int    `json:"recordCount"`
	OperationCount int    `json:"operationCount"`
	Threads        int    `json:"threads"`
	Initialize     bool   `json:"initialize"`
}

func defaultOptions() Options {
	return Options{
		Collection:     "usertable",
		RecordCount:    10000,
		OperationCount: 20000,
		Threads:        1,
	}
}

func (o Options) Validate() error {
	// Match the runner's collection rules, including the namespace byte limit.
	if strings.TrimSpace(o.Collection) == "" || strings.HasPrefix(o.Collection, "system.") ||
		strings.Contains(o.Collection, "$") || strings.ContainsFunc(o.Collection, unicode.IsControl) ||
		strings.HasPrefix(o.Collection, ".") || strings.HasSuffix(o.Collection, ".") || strings.Contains(o.Collection, "..") ||
		len(benchmarkDatabase)+1+len(o.Collection) > 235 {
		return invalidOptions("collection must be a non-system collection name without control characters, $, or empty dot segments; namespace must not exceed 235 bytes")
	}
	const maximum = 1<<31 - 1
	for _, field := range []struct {
		name  string
		value int
	}{
		{"recordCount", o.RecordCount},
		{"operationCount", o.OperationCount},
		{"threads", o.Threads},
	} {
		if field.value < 1 || field.value > maximum {
			return invalidOptions(fmt.Sprintf("%s must be an integer between 1 and 2147483647", field.name))
		}
	}
	if o.Threads > o.OperationCount {
		return invalidOptions("threads must not exceed operationCount")
	}
	return nil
}

// decodeOptions applies defaults, strictly decodes the request, and validates
// effective values before they can be passed to the runner.
func decodeOptions(raw json.RawMessage) (Options, error) {
	options, err := driver.DecodeStrictOptions(raw, defaultOptions(), "go-ycsb")
	if err != nil {
		return Options{}, err
	}
	if err := options.Validate(); err != nil {
		return Options{}, err
	}
	return options, nil
}

func invalidOptions(message string) error {
	return &driver.Error{Kind: driver.InvalidOptions, Message: message}
}
