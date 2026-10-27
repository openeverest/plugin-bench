// Package sysbench prepares MySQL workloads for the sysbench runner image.
package sysbench

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

type sysbenchDriver struct{}

// New returns a stateless driver that can be shared across concurrent runs.
func New() driver.Driver { return sysbenchDriver{} }

func (sysbenchDriver) ID() string { return "sysbench" }

func (sysbenchDriver) Supports(target driver.TargetInfo) bool {
	return strings.EqualFold(strings.TrimSpace(target.DatabaseType), "mysql")
}

func (d sysbenchDriver) Prepare(input driver.PrepareInput) (driver.RunnerSpec, error) {
	if !d.Supports(input.Target) {
		return driver.RunnerSpec{}, &driver.Error{Kind: driver.UnsupportedConnection, Message: "sysbench requires a MySQL target"}
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
		return driver.RunnerSpec{}, invalidOptions("could not encode effective sysbench options")
	}
	spec := driver.RunnerSpec{
		Env: map[string]string{
			"MYSQL_HOST":     connection.Host,
			"MYSQL_PORT":     strconv.Itoa(connection.Port),
			"MYSQL_DATABASE": connection.Database,

			"MYSQL_USER":       connection.Username,
			"BENCH_DURATION":   strconv.Itoa(options.DurationSeconds),
			"BENCH_THREADS":    strconv.Itoa(options.Threads),
			"BENCH_TABLES":     strconv.Itoa(options.Tables),
			"BENCH_TABLE_SIZE": strconv.Itoa(options.TableSize),
			"BENCH_INITIALIZE": strconv.FormatBool(options.Initialize),
		},
		// Every SecretData value becomes an output redaction candidate.
		SecretData: map[string][]byte{"MYSQL_PASSWORD": []byte(connection.Password)},
		SecretEnv:  map[string]string{"MYSQL_PASSWORD": "MYSQL_PASSWORD"},
		Snapshot:   driver.OptionSnapshot{Database: connection.Database, Options: snapshot},
	}
	if strings.TrimSpace(connection.URI) != "" {
		// The URI may contain an encoded representation of the password.
		spec.SensitiveValues = []string{connection.URI}
	}
	return spec, nil
}
