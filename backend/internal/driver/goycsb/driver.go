// Package goycsb prepares MongoDB benchmark workloads for the go-ycsb runner.
package goycsb

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

type goycsbDriver struct{}

// New returns a stateless driver that can be shared across concurrent runs.
func New() driver.Driver { return goycsbDriver{} }

func (goycsbDriver) ID() string { return "go-ycsb" }

func (goycsbDriver) Supports(target driver.TargetInfo) bool {
	return strings.EqualFold(strings.TrimSpace(target.DatabaseType), "mongodb")
}

func (d goycsbDriver) Prepare(input driver.PrepareInput) (driver.RunnerSpec, error) {
	if !d.Supports(input.Target) {
		return driver.RunnerSpec{}, &driver.Error{
			Kind:    driver.UnsupportedConnection,
			Message: "go-ycsb requires a MongoDB target",
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
	return prepareRunner(connection, options)
}

// prepareRunner maps validated configuration to the standalone runner's
// environment contract. Credentials remain in SecretData and out of snapshots.
func prepareRunner(connection resolvedConnection, options Options) (driver.RunnerSpec, error) {
	snapshot, err := json.Marshal(options)
	if err != nil {
		return driver.RunnerSpec{}, invalidOptions("could not encode effective go-ycsb options")
	}
	spec := driver.RunnerSpec{
		Env: map[string]string{
			"BENCH_COLLECTION":      options.Collection,
			"BENCH_RECORD_COUNT":    strconv.Itoa(options.RecordCount),
			"BENCH_OPERATION_COUNT": strconv.Itoa(options.OperationCount),
			"BENCH_THREADS":         strconv.Itoa(options.Threads),
			"BENCH_INITIALIZE":      strconv.FormatBool(options.Initialize),
		},
		SecretData: map[string][]byte{"MONGODB_URI": []byte(connection.URI)},
		SecretEnv:  map[string]string{"MONGODB_URI": "MONGODB_URI"},
		Snapshot:   driver.OptionSnapshot{Database: connection.Database, Options: snapshot},
	}
	if connection.CACertificate != "" {
		spec.SecretData["MONGODB_TLS_CA"] = []byte(connection.CACertificate)
		spec.SecretEnv["MONGODB_TLS_CA"] = "MONGODB_TLS_CA"
	}
	for _, sensitive := range []string{connection.OriginalURI, connection.Password} {
		if sensitive != "" {
			spec.SensitiveValues = append(spec.SensitiveValues, sensitive)
		}
	}
	if connection.Password != "" {
		for _, sensitive := range []string{url.QueryEscape(connection.Password), url.PathEscape(connection.Password)} {
			if sensitive != "" && sensitive != connection.Password {
				spec.SensitiveValues = append(spec.SensitiveValues, sensitive)
			}
		}
	}
	return spec, nil
}
