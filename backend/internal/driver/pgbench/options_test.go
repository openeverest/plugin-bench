package pgbench

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
	var preparationErr *driver.Error
	if !errors.As(err, &preparationErr) || preparationErr.Kind != kind {
		t.Fatalf("error = %v; want driver error kind %q", err, kind)
	}
}

func TestDecodeOptionsAppliesDefaults(t *testing.T) {
	for _, test := range []struct {
		name, input string
		want        Options
	}{
		{"empty object", `{}`, Options{DurationSeconds: 60, Clients: 1, Threads: 1, Scale: 1}},
		{"partial options", `{"clients":4}`, Options{DurationSeconds: 60, Clients: 4, Threads: 1, Scale: 1}},
		{"explicit false", `{"initialize":false}`, Options{DurationSeconds: 60, Clients: 1, Threads: 1, Scale: 1}},
		{"explicit settings", `{"durationSeconds":30,"clients":4,"threads":2,"scale":10,"initialize":true}`, Options{DurationSeconds: 30, Clients: 4, Threads: 2, Scale: 10, Initialize: true}},
		{"maximum integers", `{"durationSeconds":2147483647,"clients":2147483647,"threads":2147483647,"scale":2147483647}`, Options{DurationSeconds: 2147483647, Clients: 2147483647, Threads: 2147483647, Scale: 2147483647}},
		{"whitespace", " \n {\"scale\":2} \t", Options{DurationSeconds: 60, Clients: 1, Threads: 1, Scale: 2}},
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
		"", " ", "null", "[]", `"credential-sentinel"`, "true", "1", "{", "{} {}", "{} garbage", "{} null",
		`{"client":4}`, `{"credential-sentinel":1}`, `{"clients":"credential-sentinel"}`,
		`{"initialize":null}`, `{"initialize":"true"}`, `{"initialize":1}`, `{"initialize":{}}`,
		`{"clients":1,"threads":2}`, `{"clients":2,"threads":3}`,
	}
	for _, field := range []string{"durationSeconds", "clients", "threads", "scale"} {
		for _, value := range []string{"0", "-1", "2147483648", "1.5", `"1"`, "true", "null", "[]", "{}", "1e1000"} {
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
		{"clients", func(o *Options) { o.Clients = -1 }},
		{"threads", func(o *Options) { o.Threads = 0 }},
		{"scale", func(o *Options) { o.Scale = -1 }},
		{"threads exceed clients", func(o *Options) { o.Threads = 2 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := defaultOptions()
			test.change(&options)
			assertErrorKind(t, options.Validate(), driver.InvalidOptions)
		})
	}
}
