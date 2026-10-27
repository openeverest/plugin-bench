package pgbench

import (
	"encoding/json"
	"fmt"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

// Options contains only pgbench settings. Resources and placement are shared
// execution settings and are not interpreted by this driver.
type Options struct {
	DurationSeconds int  `json:"durationSeconds"`
	Clients         int  `json:"clients"`
	Threads         int  `json:"threads"`
	Scale           int  `json:"scale"`
	Initialize      bool `json:"initialize"`
}

func defaultOptions() Options {
	return Options{DurationSeconds: 60, Clients: 1, Threads: 1, Scale: 1}
}

func (o Options) Validate() error {
	const maximum = 1<<31 - 1
	for _, field := range []struct {
		name  string
		value int
	}{
		{"durationSeconds", o.DurationSeconds},
		{"clients", o.Clients},
		{"threads", o.Threads},
		{"scale", o.Scale},
	} {
		if field.value < 1 || field.value > maximum {
			return invalidOptions(fmt.Sprintf("%s must be an integer between 1 and 2147483647", field.name))
		}
	}
	if o.Threads > o.Clients {
		return invalidOptions("threads cannot exceed clients")
	}
	return nil
}

func decodeOptions(raw json.RawMessage) (Options, error) {
	options, err := driver.DecodeStrictOptions(raw, defaultOptions(), "pgbench")
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
