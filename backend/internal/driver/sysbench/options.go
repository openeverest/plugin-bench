package sysbench

import (
	"encoding/json"
	"fmt"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

// Options contains settings for the runner's fixed oltp_read_write workload.
// Resources and placement remain shared execution settings.
type Options struct {
	DurationSeconds int  `json:"durationSeconds"`
	Threads         int  `json:"threads"`
	Tables          int  `json:"tables"`
	TableSize       int  `json:"tableSize"`
	Initialize      bool `json:"initialize"`
}

func defaultOptions() Options {
	return Options{DurationSeconds: 60, Threads: 1, Tables: 1, TableSize: 10000}
}

func (o Options) Validate() error {
	const maximum = 1<<31 - 1
	for _, field := range []struct {
		name  string
		value int
	}{
		{"durationSeconds", o.DurationSeconds},
		{"threads", o.Threads},
		{"tables", o.Tables},
		{"tableSize", o.TableSize},
	} {
		if field.value < 1 || field.value > maximum {
			return invalidOptions(fmt.Sprintf("%s must be an integer between 1 and 2147483647", field.name))
		}
	}
	return nil
}

func decodeOptions(raw json.RawMessage) (Options, error) {
	options, err := driver.DecodeStrictOptions(raw, defaultOptions(), "sysbench")
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
