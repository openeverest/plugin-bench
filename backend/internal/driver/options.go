package driver

import (
	"bytes"
	"encoding/json"
	"io"
)

// DecodeStrictOptions decodes one JSON object over a driver's default options
// struct. It rejects unknown fields, invalid types, trailing data, and null
// fields without echoing submitted data. Drivers validate option values afterward.
func DecodeStrictOptions[T any](raw json.RawMessage, defaults T, toolName string) (T, error) {
	var zero T
	invalid := func(message string) (T, error) {
		return zero, &Error{Kind: InvalidOptions, Message: message}
	}
	invalidObject := "options must be a JSON object containing supported " + toolName + " fields with valid types"
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return invalid(invalidObject)
	}
	options := defaults
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&options); err != nil {
		// Decoder errors can quote arbitrary submitted field names or values.
		return invalid(invalidObject)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return invalid("options must contain exactly one JSON object")
	}
	// encoding/json leaves scalar defaults unchanged for null. Inspect supplied
	// fields so null cannot be mistaken for an omitted option.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return invalid(invalidObject)
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return invalid(toolName + " option fields must not be null")
		}
	}
	return options, nil
}
