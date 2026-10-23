package sysbench

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

func assertErrorKind(t *testing.T, err error, kind driver.ErrorKind) {
	t.Helper()
	var problem *driver.Error
	if !errors.As(err, &problem) || problem.Kind != kind {
		t.Fatalf("error = %v; want driver error kind %q", err, kind)
	}
}

func TestDecodeOptionsAppliesDefaults(t *testing.T) {
	for _, test := range []struct {
		name, input string
		want        Options
	}{
		{"empty object", `{}`, Options{DurationSeconds: 60, Threads: 1, Tables: 1, TableSize: 10000}},
		{"partial options", `{"threads":4}`, Options{DurationSeconds: 60, Threads: 4, Tables: 1, TableSize: 10000}},
		{"explicit false", `{"initialize":false}`, Options{DurationSeconds: 60, Threads: 1, Tables: 1, TableSize: 10000}},
		{"explicit settings", `{"durationSeconds":30,"threads":4,"tables":2,"tableSize":500,"initialize":true}`, Options{DurationSeconds: 30, Threads: 4, Tables: 2, TableSize: 500, Initialize: true}},
		{"reuse keeps table settings", `{"tables":3,"tableSize":2000,"initialize":false}`, Options{DurationSeconds: 60, Threads: 1, Tables: 3, TableSize: 2000}},
		{"minimum integers", `{"durationSeconds":1,"threads":1,"tables":1,"tableSize":1}`, Options{DurationSeconds: 1, Threads: 1, Tables: 1, TableSize: 1}},
		{"maximum integers", `{"durationSeconds":2147483647,"threads":2147483647,"tables":2147483647,"tableSize":2147483647}`, Options{DurationSeconds: 2147483647, Threads: 2147483647, Tables: 2147483647, TableSize: 2147483647}},
		{"whitespace", " \n {\"tableSize\":2} \t", Options{DurationSeconds: 60, Threads: 1, Tables: 1, TableSize: 2}},
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
		"", " \n ", "null", "[]", `[{}]`, `"credential-sentinel"`, "true", "1", "{", "{} {}", "{} garbage", "{} null",
		`{"thread":4}`, `{"credential-sentinel":1}`, `{"threads":"credential-sentinel"}`,
		`{"clients":4}`, `{"scale":1}`, `{"workload":"oltp_read_only"}`, `{"resources":{}}`,
		`{"initialize":null}`, `{"initialize":"true"}`, `{"initialize":1}`, `{"initialize":[]}`, `{"initialize":{}}`,
	}
	for _, field := range []string{"durationSeconds", "threads", "tables", "tableSize"} {
		for _, value := range []string{"0", "-1", "2147483648", "9223372036854775808", "1.5", `"1"`, "true", "null", "[]", "{}", "1e1000"} {
			inputs = append(inputs, fmt.Sprintf(`{"%s":%s}`, field, value))
		}
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			got, err := decodeOptions(json.RawMessage(input))
			assertErrorKind(t, err, driver.InvalidOptions)
			if got != (Options{}) {
				t.Fatal("invalid options returned a usable configuration")
			}
			if strings.Contains(err.Error(), "credential-sentinel") {
				t.Fatal("options error echoed submitted data")
			}
		})
	}
}

func TestOptionsValidateRejectsInvalidTypedValues(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Options)
	}{
		{"duration", func(o *Options) { o.DurationSeconds = 0 }},
		{"threads", func(o *Options) { o.Threads = -1 }},
		{"tables", func(o *Options) { o.Tables = 0 }},
		{"table size", func(o *Options) { o.TableSize = -1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := defaultOptions()
			test.change(&options)
			assertErrorKind(t, options.Validate(), driver.InvalidOptions)
		})
	}
}
