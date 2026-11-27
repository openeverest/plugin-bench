package valkey

import (
	"encoding/json"
	"fmt"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

// Keep these workload limits aligned with runners/valkey/config.go.
const (
	maxTotalClients     = 128
	maxSeedPayloadBytes = 64 * 1024 * 1024
)

// Options contains the workload settings for the Valkey runner.
type Options struct {
	Duration         int `json:"duration"`
	Threads          int `json:"threads"`
	ClientsPerThread int `json:"clientsPerThread"`
	KeyCount         int `json:"keyCount"`
	ValueSize        int `json:"valueSize"`
}

func defaultOptions() Options {
	return Options{
		Duration:         30,
		Threads:          2,
		ClientsPerThread: 10,
		KeyCount:         10_000,
		ValueSize:        256,
	}
}

func (o Options) Validate() error {
	for _, field := range []struct {
		name           string
		value, maximum int
	}{
		{"duration", o.Duration, 300},
		{"threads", o.Threads, maxTotalClients},
		{"clientsPerThread", o.ClientsPerThread, maxTotalClients},
		{"keyCount", o.KeyCount, 100_000},
		{"valueSize", o.ValueSize, 16_384},
	} {
		if field.value < 1 || field.value > field.maximum {
			return invalidOptions(fmt.Sprintf("%s must be an integer between 1 and %d", field.name, field.maximum))
		}
	}
	if o.Threads*o.ClientsPerThread > maxTotalClients {
		return invalidOptions("threads times clientsPerThread must not exceed 128 total clients")
	}
	if int64(o.KeyCount)*int64(o.ValueSize) > maxSeedPayloadBytes {
		return invalidOptions("keyCount times valueSize must not exceed 64 MiB")
	}
	return nil
}

func decodeOptions(raw json.RawMessage) (Options, error) {
	options, err := driver.DecodeStrictOptions(raw, defaultOptions(), "valkey")
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
