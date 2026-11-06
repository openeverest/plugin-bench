package goycsb

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

func assertInvalidOptions(t *testing.T, err error) {
	t.Helper()
	var problem *driver.Error
	if !errors.As(err, &problem) || problem.Kind != driver.InvalidOptions {
		t.Fatalf("error = %v; want driver.InvalidOptions", err)
	}
	if strings.Contains(err.Error(), "credential-sentinel") {
		t.Fatal("options error exposed submitted data")
	}
}

func TestDecodeOptionsAppliesDefaults(t *testing.T) {
	for _, test := range []struct {
		name, input string
		want        Options
	}{
		{"defaults", `{}`, Options{Collection: "usertable", RecordCount: 10000, OperationCount: 20000, Threads: 1}},
		{"partial", `{"threads":4}`, Options{Collection: "usertable", RecordCount: 10000, OperationCount: 20000, Threads: 4}},
		{"explicit", `{"collection":"bench.records","recordCount":500,"operationCount":1000,"threads":2,"initialize":true}`, Options{Collection: "bench.records", RecordCount: 500, OperationCount: 1000, Threads: 2, Initialize: true}},
		{"reuse preserves dataset settings", `{"collection":"existing","recordCount":100,"initialize":false}`, Options{Collection: "existing", RecordCount: 100, OperationCount: 20000, Threads: 1}},
		{"minimum integers", `{"recordCount":1,"operationCount":1,"threads":1}`, Options{Collection: "usertable", RecordCount: 1, OperationCount: 1, Threads: 1}},
		{"maximum integers", `{"recordCount":2147483647,"operationCount":2147483647,"threads":2147483647}`, Options{Collection: "usertable", RecordCount: 2147483647, OperationCount: 2147483647, Threads: 2147483647}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeOptions(json.RawMessage(test.input))
			if err != nil || got != test.want {
				t.Fatalf("decodeOptions() = %+v, %v; want %+v", got, err, test.want)
			}
		})
	}
}

func TestDecodeOptionsRejectsInvalidPayloads(t *testing.T) {
	inputs := []string{
		"", "null", "[]", `"credential-sentinel"`, "true", "1", "{", "{} {}", "{} garbage", "{} null",
		`{"credential-sentinel":1}`, `{"thread":4}`, `{"durationSeconds":60}`, `{"workload":"a"}`,
		`{"collection":1}`, `{"collection":null}`, `{"initialize":null}`, `{"initialize":"true"}`,
		`{"initialize":1}`, `{"threads":3,"operationCount":2}`,
	}
	for _, field := range []string{"recordCount", "operationCount", "threads"} {
		for _, value := range []string{"0", "-1", "2147483648", "9223372036854775808", "1.5", `"credential-sentinel"`, "true", "null", "[]", "{}"} {
			inputs = append(inputs, fmt.Sprintf(`{"%s":%s}`, field, value))
		}
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			got, err := decodeOptions(json.RawMessage(input))
			assertInvalidOptions(t, err)
			if got != (Options{}) {
				t.Fatal("invalid options returned a usable configuration")
			}
		})
	}
}

func TestOptionsValidateCollectionNames(t *testing.T) {
	for _, test := range []struct {
		name       string
		collection string
		valid      bool
	}{
		{"default", "usertable", true},
		{"nested", "bench.records", true},
		{"maximum ASCII namespace", strings.Repeat("a", 230), true},
		{"maximum multibyte namespace", strings.Repeat("é", 115), true},
		{"overlong ASCII namespace", strings.Repeat("a", 231), false},
		{"overlong multibyte namespace", strings.Repeat("é", 116), false},
		{"empty", "", false},
		{"whitespace", " \t ", false},
		{"system collection", "system.credential-sentinel", false},
		{"dollar", "credential-sentinel$", false},
		{"NUL", "credential-sentinel\x00", false},
		{"newline", "credential-sentinel\n", false},
		{"unicode control", "credential-sentinel\u0085", false},
		{"leading dot", ".credential-sentinel", false},
		{"trailing dot", "credential-sentinel.", false},
		{"empty segment", "credential-sentinel..records", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{"collection": test.collection})
			if err != nil {
				t.Fatal(err)
			}
			got, err := decodeOptions(raw)
			if test.valid {
				if err != nil || got.Collection != test.collection {
					t.Fatalf("valid collection was rejected or modified: %v", err)
				}
				return
			}
			assertInvalidOptions(t, err)
			if got != (Options{}) {
				t.Fatal("invalid collection returned a usable configuration")
			}
		})
	}
}

func TestOptionsValidateRejectsInvalidTypedValues(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Options)
	}{
		{"record count", func(o *Options) { o.RecordCount = 0 }},
		{"operation count", func(o *Options) { o.OperationCount = -1 }},
		{"threads", func(o *Options) { o.Threads = 0 }},
		{"threads exceed operations", func(o *Options) { o.Threads = o.OperationCount + 1 }},
		{"collection", func(o *Options) { o.Collection = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := defaultOptions()
			test.change(&options)
			assertInvalidOptions(t, options.Validate())
		})
	}
}
