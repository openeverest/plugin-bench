// Package hammerdb prepares TPROC-C workloads for the HammerDB runner image.
package hammerdb

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

type hammerdbDriver struct{}

// New returns a stateless driver that can be shared across concurrent runs.
func New() driver.Driver { return hammerdbDriver{} }

func (hammerdbDriver) ID() string { return "hammerdb" }

func (hammerdbDriver) Supports(target driver.TargetInfo) bool {
	switch strings.ToLower(strings.TrimSpace(target.DatabaseType)) {
	case "postgresql", "mysql":
		return true
	default:
		return false
	}
}

func (d hammerdbDriver) Prepare(input driver.PrepareInput) (driver.RunnerSpec, error) {
	if !d.Supports(input.Target) {
		return driver.RunnerSpec{}, &driver.Error{
			Kind:    driver.UnsupportedConnection,
			Message: "HammerDB requires a PostgreSQL or MySQL target",
		}
	}
	options, err := decodeOptions(input.Options)
	if err != nil {
		return driver.RunnerSpec{}, err
	}
	connection, err := resolveConnection(input.Target, input.RequestedDatabase, input.Connection)
	if err != nil {
		return driver.RunnerSpec{}, err
	}
	return prepareRunner(connection, options)
}

// prepareRunner owns all returned collections. Inputs have already passed
// driver validation; preparation performs no Kubernetes or database operations.
func prepareRunner(connection resolvedConnection, options Options) (driver.RunnerSpec, error) {
	snapshot, err := json.Marshal(options)
	if err != nil {
		return driver.RunnerSpec{}, invalidOptions("could not encode effective HammerDB options")
	}
	spec := driver.RunnerSpec{
		Env: map[string]string{
			"DB_TYPE":                connection.DatabaseType,
			"DB_HOST":                connection.Host,
			"DB_PORT":                strconv.Itoa(connection.Port),
			"DB_NAME":                connection.Database,
			"DB_USER":                connection.Username,
			"BENCH_WORKLOAD":         "tproc-c",
			"BENCH_WAREHOUSES":       strconv.Itoa(options.Warehouses),
			"BENCH_VIRTUAL_USERS":    strconv.Itoa(options.VirtualUsers),
			"BENCH_RAMPUP_MINUTES":   strconv.Itoa(options.RampupMinutes),
			"BENCH_DURATION_MINUTES": strconv.Itoa(options.DurationMinutes),
			"BENCH_INITIALIZE":       strconv.FormatBool(options.Initialize),
		},
		SecretData: map[string][]byte{"DB_PASSWORD": []byte(connection.Password)},
		SecretEnv:  map[string]string{"DB_PASSWORD": "DB_PASSWORD"},
		Snapshot:   driver.OptionSnapshot{Database: connection.Database, Options: snapshot},
	}
	if connection.DatabaseType == "postgresql" {
		spec.Env["PGSSLMODE"] = connection.PGSSLMode
	} else {
		spec.Env["MYSQL_SSL"] = strconv.FormatBool(connection.MySQLSSL)
	}
	if strings.TrimSpace(connection.URI) != "" {
		// The URI can contain an encoded representation of the password.
		spec.SensitiveValues = []string{connection.URI}
	}
	return spec, nil
}
