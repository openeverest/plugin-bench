// Package pgbench prepares PostgreSQL benchmark workloads for the pgbench image.
package pgbench

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

type pgbenchDriver struct{}

// New returns a stateless driver that can be shared across concurrent runs.
func New() driver.Driver { return pgbenchDriver{} }

func (pgbenchDriver) ID() string { return "pgbench" }

func (pgbenchDriver) Supports(target driver.TargetInfo) bool {
	return strings.EqualFold(strings.TrimSpace(target.DatabaseType), "postgresql")
}

func (d pgbenchDriver) Prepare(input driver.PrepareInput) (driver.RunnerSpec, error) {
	if !d.Supports(input.Target) {
		return driver.RunnerSpec{}, &driver.Error{Kind: driver.UnsupportedConnection, Message: "pgbench requires a PostgreSQL target"}
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

// prepareRunner owns all returned collections. Its inputs have already passed
// driver validation; no Kubernetes objects or external operations are needed.
func prepareRunner(connection resolvedConnection, options Options) (driver.RunnerSpec, error) {
	snapshot, err := json.Marshal(options)
	if err != nil {
		return driver.RunnerSpec{}, invalidOptions("could not encode effective pgbench options")
	}
	secrets := map[string][]byte{
		"PGHOST":     []byte(connection.Host),
		"PGPORT":     []byte(strconv.Itoa(connection.Port)),
		"PGDATABASE": []byte(connection.Database),
		"PGUSER":     []byte(connection.Username),
		"PGPASSWORD": []byte(connection.Password),
	}
	secretEnv := make(map[string]string, len(secrets))
	for key := range secrets {
		secretEnv[key] = key
	}
	spec := driver.RunnerSpec{
		Env: map[string]string{
			"BENCH_DURATION":   strconv.Itoa(options.DurationSeconds),
			"BENCH_CLIENTS":    strconv.Itoa(options.Clients),
			"BENCH_THREADS":    strconv.Itoa(options.Threads),
			"BENCH_SCALE":      strconv.Itoa(options.Scale),
			"BENCH_INITIALIZE": strconv.FormatBool(options.Initialize),
		},
		SecretData: secrets,
		SecretEnv:  secretEnv,
		Snapshot:   driver.OptionSnapshot{Database: connection.Database, Options: snapshot},
	}
	if strings.TrimSpace(connection.URI) != "" {
		// The original URI can embed an encoded password; redacting SecretData
		// values alone would not necessarily cover that representation.
		spec.SensitiveValues = []string{connection.URI}
	}
	return spec, nil
}
