package valkey

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

func TestDecodeOptions(t *testing.T) {
	for _, tt := range []struct {
		name, raw string
		want      Options
	}{
		{"defaults", `{}`, Options{30, 2, 10, 10_000, 256}},
		{"partial override", `{"duration":45}`, Options{45, 2, 10, 10_000, 256}},
		{"minimums", `{"duration":1,"threads":1,"clientsPerThread":1,"keyCount":1,"valueSize":1}`, Options{1, 1, 1, 1, 1}},
		{"maximum threads and payload", `{"duration":300,"threads":128,"clientsPerThread":1,"keyCount":4096,"valueSize":16384}`, Options{300, 128, 1, 4096, 16_384}},
		{"maximum keys and clients per thread", `{"threads":1,"clientsPerThread":128,"keyCount":100000}`, Options{30, 1, 128, 100_000, 256}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeOptions(json.RawMessage(tt.raw))
			if err != nil || got != tt.want {
				t.Fatalf("decodeOptions() = %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

func TestDecodeOptionsRejectsInvalidInput(t *testing.T) {
	for _, tt := range []struct {
		name, raw, message string
	}{
		{"duration too short", `{"duration":0}`, "duration"},
		{"duration too long", `{"duration":301}`, "duration"},
		{"zero threads", `{"threads":0}`, "threads"},
		{"too many threads", `{"threads":129}`, "threads"},
		{"zero clients", `{"clientsPerThread":0}`, "clientsPerThread"},
		{"too many clients per thread", `{"clientsPerThread":129}`, "clientsPerThread"},
		{"zero keys", `{"keyCount":0}`, "keyCount"},
		{"too many keys", `{"keyCount":100001}`, "keyCount"},
		{"zero value size", `{"valueSize":0}`, "valueSize"},
		{"value too large", `{"valueSize":16385}`, "valueSize"},
		{"too many total clients", `{"threads":3,"clientsPerThread":43}`, "128 total clients"},
		{"payload too large", `{"keyCount":4097,"valueSize":16384}`, "64 MiB"},
		{"unknown field", `{"password":"example"}`, "supported valkey fields"},
		{"fractional number", `{"duration":1.5}`, "valid types"},
		{"numeric string", `{"duration":"30"}`, "valid types"},
		{"null field", `{"duration":null}`, "must not be null"},
		{"trailing object", `{} {}`, "exactly one JSON object"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeOptions(json.RawMessage(tt.raw))
			var optionErr *driver.Error
			if !errors.As(err, &optionErr) || optionErr.Kind != driver.InvalidOptions ||
				!strings.Contains(optionErr.Message, tt.message) {
				t.Fatalf("decodeOptions() error = %v; want invalid options containing %q", err, tt.message)
			}
		})
	}
}
