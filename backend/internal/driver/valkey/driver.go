// Package valkey prepares Memtier benchmark workloads for Valkey instances.
package valkey

import (
	"encoding/json"
	"net/url"
	"strconv"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

type valkeyDriver struct{}

// New returns a stateless driver that can be shared across concurrent runs.
func New() driver.Driver { return valkeyDriver{} }

func (valkeyDriver) ID() string { return "valkey" }

func (valkeyDriver) Supports(target driver.TargetInfo) bool {
	return supportsTarget(target)
}

func (d valkeyDriver) Prepare(input driver.PrepareInput) (driver.RunnerSpec, error) {
	if !d.Supports(input.Target) {
		return driver.RunnerSpec{}, &driver.Error{
			Kind:    driver.UnsupportedConnection,
			Message: "Valkey benchmarks require an OpenEverest Valkey target",
		}
	}
	options, err := decodeOptions(input.Options)
	if err != nil {
		return driver.RunnerSpec{}, err
	}
	connection, err := resolveConnection(input.RequestedDatabase, input.Connection)
	if err != nil {
		return driver.RunnerSpec{}, err
	}
	snapshot, err := json.Marshal(options)
	if err != nil {
		return driver.RunnerSpec{}, invalidOptions("could not encode effective Valkey options")
	}
	spec := driver.RunnerSpec{
		MinimumDuration: time.Duration(options.Duration) * time.Second,
		Env: map[string]string{
			"VALKEY_HOST":              connection.Host,
			"VALKEY_PORT":              connection.Port,
			"VALKEY_USERNAME":          connection.Username,
			"VALKEY_TLS":               strconv.FormatBool(connection.TLS),
			"BENCH_DURATION":           strconv.Itoa(options.Duration),
			"BENCH_THREADS":            strconv.Itoa(options.Threads),
			"BENCH_CLIENTS_PER_THREAD": strconv.Itoa(options.ClientsPerThread),
			"BENCH_KEY_COUNT":          strconv.Itoa(options.KeyCount),
			"BENCH_VALUE_SIZE":         strconv.Itoa(options.ValueSize),
		},
		SecretData:      make(map[string][]byte),
		SecretEnv:       make(map[string]string),
		Snapshot:        driver.OptionSnapshot{Database: benchmarkDatabase, Options: snapshot},
		SensitiveValues: []string{input.Connection.URI},
	}
	for name, value := range map[string]string{
		"VALKEY_PASSWORD": connection.Password,
		"VALKEY_TLS_CA":   connection.CACertificate,
		"VALKEY_TLS_CERT": connection.ClientCertificate,
		"VALKEY_TLS_KEY":  connection.ClientKey,
	} {
		if value != "" {
			spec.SecretData[name] = []byte(value)
			spec.SecretEnv[name] = name
		}
	}
	if encoded := url.QueryEscape(connection.Password); encoded != connection.Password {
		spec.SensitiveValues = append(spec.SensitiveValues, encoded)
	}
	return spec, nil
}
