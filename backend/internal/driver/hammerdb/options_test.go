package hammerdb

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

func TestDecodeOptionsAppliesDefaultsAndOverrides(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		want  Options
	}{
		{"empty object", `{}`, Options{Warehouses: 1, VirtualUsers: 1, DurationMinutes: 1}},
		{"partial options", `{"warehouses":4}`, Options{Warehouses: 4, VirtualUsers: 1, DurationMinutes: 1}},
		{"explicit false", `{"initialize":false}`, Options{Warehouses: 1, VirtualUsers: 1, DurationMinutes: 1}},
		{"all options", `{"warehouses":4,"virtualUsers":2,"rampupMinutes":3,"durationMinutes":5,"initialize":true}`, Options{Warehouses: 4, VirtualUsers: 2, RampupMinutes: 3, DurationMinutes: 5, Initialize: true}},
		{"zero ramp-up", `{"rampupMinutes":0}`, Options{Warehouses: 1, VirtualUsers: 1, DurationMinutes: 1}},
		{"maximum warehouse and user counts", `{"warehouses":2147483647,"virtualUsers":2147483647}`, Options{Warehouses: 2147483647, VirtualUsers: 2147483647, DurationMinutes: 1}},
		{"maximum supported duration", `{"durationMinutes":153722867}`, Options{Warehouses: 1, VirtualUsers: 1, DurationMinutes: 153722867}},
		{"duration boundary with ramp-up", `{"rampupMinutes":153722866,"durationMinutes":1}`, Options{Warehouses: 1, VirtualUsers: 1, RampupMinutes: 153722866, DurationMinutes: 1}},
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
		"", "null", "[]", `"sentinel-secret"`, "true", "1", "{", "{} {}", "{} trailing",
		`{"unknown":1}`, `{"client":2}`, `{"initialize":null}`, `{"initialize":"true"}`,
		`{"warehouses":null}`, `{"virtualUsers":1.5}`, `{"rampupMinutes":-1}`, `{"durationMinutes":0}`,
		`{"warehouses":2147483648}`, `{"durationMinutes":9223372036854775808}`,
		`{"warehouses":"sentinel-secret"}`,
		`{"rampupMinutes":153722867,"durationMinutes":1}`,
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			got, err := decodeOptions(json.RawMessage(input))
			assertInvalidOptions(t, err)
			if got != (Options{}) {
				t.Fatal("invalid options returned a usable configuration")
			}
			if strings.Contains(err.Error(), "sentinel-secret") {
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
		{"zero warehouses", func(o *Options) { o.Warehouses = 0 }},
		{"negative virtual users", func(o *Options) { o.VirtualUsers = -1 }},
		{"users exceed warehouses", func(o *Options) { o.Warehouses, o.VirtualUsers = 1, 2 }},
		{"negative ramp-up", func(o *Options) { o.RampupMinutes = -1 }},
		{"zero duration", func(o *Options) { o.DurationMinutes = 0 }},
		{"duration overflow", func(o *Options) { o.RampupMinutes, o.DurationMinutes = 153722867, 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := defaultOptions()
			test.change(&options)
			assertInvalidOptions(t, options.Validate())
		})
	}
}

func TestOptionsValidateAcceptsLargestDurationThatFits(t *testing.T) {
	options := defaultOptions()
	options.RampupMinutes = int((int64(1<<63-1) / int64(time.Minute)) - 1)
	if err := options.Validate(); err != nil {
		t.Fatalf("Validate() rejected maximum supported combined duration: %v", err)
	}
}

func assertInvalidOptions(t *testing.T, err error) {
	t.Helper()
	var problem *driver.Error
	if !errors.As(err, &problem) || problem.Kind != driver.InvalidOptions {
		t.Fatalf("error = %v; want driver error kind %q", err, driver.InvalidOptions)
	}
}
