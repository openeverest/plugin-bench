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
	spec := driver.RunnerSpec{
		Env: map[string]string{
			"PGHOST":           connection.Host,
			"PGPORT":           strconv.Itoa(connection.Port),
			"PGDATABASE":       connection.Database,
			"PGUSER":           connection.Username,
			"BENCH_DURATION":   strconv.Itoa(options.DurationSeconds),
			"BENCH_CLIENTS":    strconv.Itoa(options.Clients),
			"BENCH_THREADS":    strconv.Itoa(options.Threads),
			"BENCH_SCALE":      strconv.Itoa(options.Scale),
			"BENCH_INITIALIZE": strconv.FormatBool(options.Initialize),
		},
		// Only secrets belong here: shared output redaction treats every
		// SecretData value as sensitive, including short strings and numbers.
		SecretData: map[string][]byte{"PGPASSWORD": []byte(connection.Password)},
		SecretEnv:  map[string]string{"PGPASSWORD": "PGPASSWORD"},
		Snapshot:   driver.OptionSnapshot{Database: connection.Database, Options: snapshot},
	}
	if strings.TrimSpace(connection.URI) != "" {
		// The original URI can embed an encoded password; redacting SecretData
		// values alone would not necessarily cover that representation.
		spec.SensitiveValues = []string{connection.URI}
	}
	return spec, nil
}
