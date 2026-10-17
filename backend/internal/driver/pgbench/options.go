package pgbench

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

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
	const invalidObject = "options must be a JSON object containing supported pgbench fields with valid types"
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
	// encoding/json leaves a scalar's existing default unchanged for null.
	// Inspect supplied values so null is rejected rather than treated as omission.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Options{}, invalidOptions(invalidObject)
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return Options{}, invalidOptions("pgbench option fields must not be null")
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
