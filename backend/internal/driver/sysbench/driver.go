// Package sysbench prepares MySQL-compatible workloads for the Sysbench runner image.
package sysbench

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

type sysbenchDriver struct {
	id           string
	databaseType string
	provider     string
}

// New returns a stateless driver that can be shared across concurrent runs.
func New() driver.Driver {
	return sysbenchDriver{id: "sysbench", databaseType: "mysql"}
}

// NewMariaDB returns the MariaDB-facing driver, using the same Sysbench runner.
func NewMariaDB() driver.Driver {
	return sysbenchDriver{id: "mariadb", databaseType: "mariadb", provider: "mariadb"}
}

func (d sysbenchDriver) ID() string { return d.id }

func (d sysbenchDriver) Supports(target driver.TargetInfo) bool {
	databaseType := strings.TrimSpace(target.DatabaseType)
	if strings.EqualFold(databaseType, d.databaseType) {
		return true
	}
	// The current OpenEverest MariaDB provider reports its engine as "mysql".
	// Require the provider identity as well, so the MariaDB driver doesn't
	// become eligible for ordinary MySQL targets.
	return d.provider != "" && strings.EqualFold(databaseType, "mysql") &&
		strings.EqualFold(strings.TrimSpace(target.Provider), d.provider)
}

func (d sysbenchDriver) Prepare(input driver.PrepareInput) (driver.RunnerSpec, error) {
	if !d.Supports(input.Target) {
		return driver.RunnerSpec{}, &driver.Error{
			Kind:    driver.UnsupportedConnection,
			Message: d.id + " requires a " + d.databaseType + " target",
		}
	}
	options, err := decodeOptions(input.Options, d.id)
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
