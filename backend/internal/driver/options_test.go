package driver

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDecodeStrictOptionsAppliesDefaultsAndPreservesExplicitValues(t *testing.T) {
	type options struct {
		Count      int  `json:"count"`
		Initialize bool `json:"initialize"`
	}
	defaults := options{Count: 10, Initialize: true}
	for _, test := range []struct {
		name, input string
		want        options
	}{
		{"defaults", `{}`, defaults},
		{"partial", `{"count":4}`, options{Count: 4, Initialize: true}},
		{"explicit false", `{"initialize":false}`, options{Count: 10}},
		{"explicit zero reaches driver validation", `{"count":0}`, options{Initialize: true}},
		{"negative reaches driver validation", `{"count":-1}`, options{Count: -1, Initialize: true}},
		{"whitespace", " \n {\"count\":2} \t", options{Count: 2, Initialize: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := DecodeStrictOptions(json.RawMessage(test.input), defaults, "example")
			if err != nil || got != test.want {
				t.Fatalf("DecodeStrictOptions() = %+v, %v; want %+v", got, err, test.want)
			}
		})
	}
	if defaults != (options{Count: 10, Initialize: true}) {
		t.Fatal("decoding mutated the driver's defaults")
	}
}

func TestDecodeStrictOptionsRejectsInvalidJSONWithoutEchoingInput(t *testing.T) {
	type options struct {
		Count      int  `json:"count"`
		Initialize bool `json:"initialize"`
	}
	const invalidObject = "options must be a JSON object containing supported example fields with valid types"
	for _, test := range []struct {
		name, input, message string
	}{
		{"missing", "", invalidObject},
		{"whitespace", " \n ", invalidObject},
		{"null object", "null", invalidObject},
		{"array", "[]", invalidObject},
		{"string", `"credential-sentinel"`, invalidObject},
		{"boolean", "true", invalidObject},
		{"number", "1", invalidObject},
		{"malformed", "{", invalidObject},
		{"unknown field", `{"credential-sentinel":1}`, invalidObject},
		{"wrong type", `{"count":"credential-sentinel"}`, invalidObject},
		{"second object", "{} {}", "options must contain exactly one JSON object"},
		{"trailing data", "{} credential-sentinel", "options must contain exactly one JSON object"},
		{"null integer", `{"count":null}`, "example option fields must not be null"},
		{"null boolean", `{"initialize":null}`, "example option fields must not be null"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := DecodeStrictOptions(json.RawMessage(test.input), options{Count: 10, Initialize: true}, "example")
			var problem *Error
			if !errors.As(err, &problem) || problem.Kind != InvalidOptions || problem.Message != test.message {
				t.Fatalf("DecodeStrictOptions() error = %v; want InvalidOptions with message %q", err, test.message)
			}
			if got != (options{}) {
				t.Fatal("invalid input returned a usable configuration")
			}
			if strings.Contains(err.Error(), "credential-sentinel") {
				t.Fatal("error echoed submitted data")
			}
		})
	}
}
