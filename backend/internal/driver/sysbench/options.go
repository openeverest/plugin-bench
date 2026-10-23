package sysbench

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

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
	const invalidObject = "options must be a JSON object containing supported sysbench fields with valid types"

	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return Options{}, invalidOptions(invalidObject)
	}
	options := defaultOptions()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&options); err != nil {
		// Decoder errors can quote arbitrary submitted field names or values.
		return Options{}, invalidOptions(invalidObject)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Options{}, invalidOptions("options must contain exactly one JSON object")
	}
	// encoding/json leaves scalar defaults unchanged for null. Reject supplied
	// null fields explicitly so they cannot be mistaken for omitted options.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Options{}, invalidOptions(invalidObject)
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return Options{}, invalidOptions("sysbench option fields must not be null")
		}
	}
	if err := options.Validate(); err != nil {
		return Options{}, err
	}
	return options, nil
}

func invalidOptions(message string) error {
	return &driver.Error{Kind: driver.InvalidOptions, Message: message}
}
