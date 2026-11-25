package hammerdb

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

// Options contains the TPROC-C settings supported by the HammerDB runner.
// Workload selection, connection settings, resources, and placement are kept
// outside the options object.
type Options struct {
	Warehouses      int  `json:"warehouses"`
	VirtualUsers    int  `json:"virtualUsers"`
	RampupMinutes   int  `json:"rampupMinutes"`
	DurationMinutes int  `json:"durationMinutes"`
	Initialize      bool `json:"initialize"`
}

func defaultOptions() Options {
	return Options{
		Warehouses:      1,
		VirtualUsers:    1,
		RampupMinutes:   0,
		DurationMinutes: 1,
		Initialize:      false,
	}
}

func (o Options) Validate() error {
	const maximum = 1<<31 - 1
	for _, field := range []struct {
		name  string
		value int
		min   int
	}{
		{"warehouses", o.Warehouses, 1},
		{"virtualUsers", o.VirtualUsers, 1},
		{"rampupMinutes", o.RampupMinutes, 0},
		{"durationMinutes", o.DurationMinutes, 1},
	} {
		if field.value < field.min || field.value > maximum {
			return invalidOptions(fmt.Sprintf("%s must be an integer between %d and %d", field.name, field.min, maximum))
		}
	}
	if o.VirtualUsers > o.Warehouses {
		return invalidOptions("virtualUsers must not exceed warehouses")
	}
	// The runner converts the combined warm-up and measurement period to a
	// time.Duration. Check the sum before that conversion can overflow.
	maxMinutes := int64((1<<63 - 1) / int64(time.Minute))
	if int64(o.RampupMinutes)+int64(o.DurationMinutes) > maxMinutes {
		return invalidOptions("rampupMinutes plus durationMinutes exceeds the supported time budget")
	}
	return nil
}

func decodeOptions(raw json.RawMessage) (Options, error) {
	options, err := driver.DecodeStrictOptions(raw, defaultOptions(), "hammerdb")
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
