package sysbench

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

func TestDriverSupportsDatabaseType(t *testing.T) {
	d := New()
	if d.ID() != "sysbench" {
		t.Fatalf("ID() = %q; want sysbench", d.ID())
	}
	for _, databaseType := range []string{"mysql", "MySQL", " MYSQL "} {
		if !d.Supports(driver.TargetInfo{DatabaseType: databaseType, Provider: "unfamiliar-provider"}) {
			t.Fatalf("MySQL type %q was not supported", databaseType)
		}
	}
	for _, databaseType := range []string{"", "postgresql", "mongodb", "mariadb"} {
		if d.Supports(driver.TargetInfo{DatabaseType: databaseType, Provider: "mysql-provider"}) {
			t.Fatalf("non-MySQL type %q was supported", databaseType)
		}
	}
}

func TestPrepareCombinesOptionsAndConnectionResolution(t *testing.T) {
	for _, test := range []struct {
		name, requested, database, uri, rawOptions, wantDatabase string
		wantOptions                                              Options
	}{
		{"defaults", "", "sbtest", "", `{}`, "sbtest", Options{DurationSeconds: 60, Threads: 1, Tables: 1, TableSize: 10000}},
		{"request and overrides", " custom ", "sbtest", "", `{"threads":4,"tables":2,"initialize":true}`, "custom", Options{DurationSeconds: 60, Threads: 4, Tables: 2, TableSize: 10000, Initialize: true}},
		{"URI fallback", "", "", "mysql://sbuser:credential-sentinel@mysql.example.com/uri-db", `{"tableSize":500}`, "uri-db", Options{DurationSeconds: 60, Threads: 1, Tables: 1, TableSize: 500}},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := driver.PrepareInput{
				Target:     driver.TargetInfo{DatabaseType: " MySQL ", Provider: "new-mysql-provider"},
				Connection: validConnectionDetails(), RequestedDatabase: test.requested,
				Options: json.RawMessage(test.rawOptions),
			}
			input.Connection.Database, input.Connection.URI = test.database, test.uri
			before := input
			before.Options = bytes.Clone(input.Options)
			spec, err := New().Prepare(input)
			if err != nil {
				t.Fatal(err)
			}
			if err := spec.Validate(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(input, before) {
				t.Fatal("Prepare mutated caller-owned input")
			}
			var options Options
			if err := json.Unmarshal(spec.Snapshot.Options, &options); err != nil {
				t.Fatal(err)
			}
			if options != test.wantOptions || spec.Snapshot.Database != test.wantDatabase || spec.Env["MYSQL_DATABASE"] != test.wantDatabase {
				t.Fatal("Prepare did not use effective options and the resolved database")
			}
			if string(spec.SecretData["MYSQL_PASSWORD"]) != input.Connection.Password || spec.SecretEnv["MYSQL_PASSWORD"] != "MYSQL_PASSWORD" {
				t.Fatal("Prepare lost the password Secret mapping")
			}
			input.Options[0] = 'x'
			if !json.Valid(spec.Snapshot.Options) {
				t.Fatal("effective snapshot aliases caller-owned JSON")
			}
		})
	}
}

func TestPrepareRejectsInvalidInput(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*driver.PrepareInput)
		kind   driver.ErrorKind
	}{
		{"unsupported target", func(i *driver.PrepareInput) { i.Target.DatabaseType = "postgresql" }, driver.UnsupportedConnection},
		{"missing target type", func(i *driver.PrepareInput) { i.Target.DatabaseType = "" }, driver.UnsupportedConnection},
		{"unknown option", func(i *driver.PrepareInput) { i.Options = json.RawMessage(`{"credential-sentinel":1}`) }, driver.InvalidOptions},
		{"invalid option", func(i *driver.PrepareInput) { i.Options = json.RawMessage(`{"threads":0}`) }, driver.InvalidOptions},
		{"bad connection", func(i *driver.PrepareInput) { i.Connection.Port = "credential-sentinel" }, driver.InvalidCredentials},
		{"missing database", func(i *driver.PrepareInput) { i.Connection.Database = "" }, driver.InvalidOptions},
		{"URI-only credentials", func(i *driver.PrepareInput) {
			i.Connection = driver.ConnectionDetails{URI: "mysql://sbuser:credential-sentinel@mysql.example.com/sbtest"}
		}, driver.UnsupportedConnection},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := driver.PrepareInput{
				Target:     driver.TargetInfo{DatabaseType: "mysql"},
				Connection: validConnectionDetails(), Options: json.RawMessage(`{}`),
			}
			test.change(&input)
			spec, err := New().Prepare(input)
			assertErrorKind(t, err, test.kind)
			if !reflect.DeepEqual(spec, driver.RunnerSpec{}) {
				t.Fatal("failed preparation returned a usable workload")
			}
			if strings.Contains(err.Error(), "credential-sentinel") {
				t.Fatal("preparation error exposed submitted credentials")
			}
		})
	}
}

func TestPrepareRunnerMapsEnvironmentAndSnapshot(t *testing.T) {
	for _, test := range []struct {
		name    string
		options Options
		uri     string
		wantEnv map[string]string
	}{
		{
			name: "defaults", options: defaultOptions(),
			wantEnv: map[string]string{
				"MYSQL_HOST": "mysql.example.com", "MYSQL_PORT": "3306", "MYSQL_DATABASE": "sbtest", "MYSQL_USER": "sbuser",
				"BENCH_DURATION": "60", "BENCH_THREADS": "1", "BENCH_TABLES": "1", "BENCH_TABLE_SIZE": "10000", "BENCH_INITIALIZE": "false",
			},
		},
		{
			name: "explicit settings", options: Options{DurationSeconds: 30, Threads: 4, Tables: 2, TableSize: 500, Initialize: true},
			uri: "mysql://sbuser:credential-sentinel@mysql.example.com/sbtest",
			wantEnv: map[string]string{
				"MYSQL_HOST": "mysql.example.com", "MYSQL_PORT": "3306", "MYSQL_DATABASE": "sbtest", "MYSQL_USER": "sbuser",
				"BENCH_DURATION": "30", "BENCH_THREADS": "4", "BENCH_TABLES": "2", "BENCH_TABLE_SIZE": "500", "BENCH_INITIALIZE": "true",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			connection := resolvedConnection{Host: "mysql.example.com", Port: 3306, Database: "sbtest", Username: "sbuser", Password: "credential-sentinel", URI: test.uri}
			spec, err := prepareRunner(connection, test.options)
			if err != nil {
				t.Fatal(err)
			}
			if err := spec.Validate(); err != nil {
				t.Fatalf("invalid shared runner contract: %v", err)
			}
			if !reflect.DeepEqual(spec.Env, test.wantEnv) {
				t.Fatal("runner environment does not match the sysbench contract")
			}
			if len(spec.Command) != 0 || len(spec.Args) != 0 {
				t.Fatal("runner image entrypoint was overridden")
			}
			if len(spec.SecretData) != 1 || string(spec.SecretData["MYSQL_PASSWORD"]) != connection.Password ||
				!reflect.DeepEqual(spec.SecretEnv, map[string]string{"MYSQL_PASSWORD": "MYSQL_PASSWORD"}) {
				t.Fatal("password was not mapped to the per-run Secret")
			}
			var snapshot Options
			if err := json.Unmarshal(spec.Snapshot.Options, &snapshot); err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(spec.Snapshot.Options, &fields); err != nil {
				t.Fatal(err)
			}
			if snapshot != test.options || spec.Snapshot.Database != connection.Database || len(fields) != 5 || fields["initialize"] == nil {
				t.Fatal("snapshot omitted or changed effective options or database")
			}
			public, err := json.Marshal(struct {
				Env           map[string]string
				Command, Args []string
				Snapshot      driver.OptionSnapshot
			}{spec.Env, spec.Command, spec.Args, spec.Snapshot})
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(public, []byte(connection.Password)) || (test.uri != "" && bytes.Contains(public, []byte(test.uri))) {
				t.Fatal("credentials escaped into ordinary workload fields or snapshot")
			}
			if test.uri == "" {
				if len(spec.SensitiveValues) != 0 {
					t.Fatal("password was unnecessarily repeated in additional redaction values")
				}
			} else if !reflect.DeepEqual(spec.SensitiveValues, []string{test.uri}) {
				t.Fatal("credential URI was not retained for additional redaction")
			}
		})
	}
}

func TestPrepareRunnerOwnsReturnedCollections(t *testing.T) {
	connection := resolvedConnection{
		Host: "mysql.example.com", Port: 3306, Database: "sbtest", Username: "sbuser", Password: "credential-sentinel",
		URI: "mysql://sbuser:credential-sentinel@mysql.example.com/sbtest",
	}
	first, err := prepareRunner(connection, defaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	second, err := prepareRunner(connection, defaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	want := second.Clone()
	first.Env["MYSQL_HOST"] = "changed"
	first.SecretData["MYSQL_PASSWORD"][0] = 'x'
	first.SecretEnv["MYSQL_PASSWORD"] = "changed"
	first.SensitiveValues[0] = "changed"
	first.Snapshot.Options[0] = 'x'
	if !reflect.DeepEqual(second, want) || connection.Password != "credential-sentinel" {
		t.Fatal("prepared workloads share mutable state or modified their input")
	}
}
