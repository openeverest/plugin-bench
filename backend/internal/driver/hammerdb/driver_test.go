package hammerdb

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

func TestDriverSupportsDatabaseTypes(t *testing.T) {
	d := New()
	if d.ID() != "hammerdb" {
		t.Fatalf("ID() = %q; want hammerdb", d.ID())
	}
	for _, databaseType := range []string{"postgresql", "PostgreSQL", " MYSQL "} {
		if !d.Supports(driver.TargetInfo{DatabaseType: databaseType}) {
			t.Errorf("database type %q should be supported", databaseType)
		}
	}
	for _, databaseType := range []string{"", "mariadb", "mongodb", "postgres"} {
		if d.Supports(driver.TargetInfo{DatabaseType: databaseType}) {
			t.Errorf("database type %q should not be supported", databaseType)
		}
	}
}

func TestPrepareResolvesBothEnginesAndRetainsOnlySafeEffectiveOptions(t *testing.T) {
	for _, test := range []struct {
		name, databaseType, uri, port string
		options                       string
		wantOptions                   Options
		wantDatabase                  string
		wantTLS                       string
	}{
		{"PostgreSQL defaults", " PostgreSQL ", "postgresql://u:uri-secret%21@pg.example.com/app?sslmode=require", "5432", `{}`, defaultOptions(), "requested_db", "require"},
		{"MySQL overrides", "MYSQL", "mysql://u:uri-secret%21@mysql.example.com/app?tls=false", "3306", `{"warehouses":4,"virtualUsers":2,"rampupMinutes":3,"durationMinutes":5,"initialize":true}`, Options{Warehouses: 4, VirtualUsers: 2, RampupMinutes: 3, DurationMinutes: 5, Initialize: true}, "requested_db", "false"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := driver.PrepareInput{
				Target: driver.TargetInfo{DatabaseType: test.databaseType},
				Connection: driver.ConnectionDetails{
					Host: "db.example.com", Port: test.port, Username: "bench-user",
					Password: "structured-password", Database: "upstream_db", URI: test.uri,
					CACertificate: "ca-certificate-sentinel",
				},
				RequestedDatabase: " requested_db ", Options: json.RawMessage(test.options),
			}
			originalOptions := append(json.RawMessage(nil), input.Options...)
			spec, err := New().Prepare(input)
			if err != nil {
				t.Fatalf("Prepare() error = %v", err)
			}
			if err := spec.Validate(); err != nil {
				t.Fatalf("RunnerSpec.Validate() error = %v", err)
			}
			if string(input.Options) != string(originalOptions) {
				t.Fatal("Prepare mutated the caller's options")
			}
			if spec.Snapshot.Database != test.wantDatabase || spec.Env["DB_NAME"] != test.wantDatabase {
				t.Fatal("request database did not take precedence")
			}
			var snapshot Options
			if err := json.Unmarshal(spec.Snapshot.Options, &snapshot); err != nil {
				t.Fatalf("decode snapshot: %v", err)
			}
			if snapshot != test.wantOptions {
				t.Fatalf("snapshot options = %+v; want %+v", snapshot, test.wantOptions)
			}
			if test.databaseType == "MYSQL" {
				if spec.Env["MYSQL_SSL"] != test.wantTLS {
					t.Fatal("MySQL TLS mode was not forwarded")
				}
				if _, present := spec.Env["PGSSLMODE"]; present {
					t.Fatal("PostgreSQL TLS setting appeared in MySQL environment")
				}
			} else if spec.Env["PGSSLMODE"] != test.wantTLS {
				t.Fatal("PostgreSQL SSL mode was not forwarded")
			}
			if string(spec.SecretData["DB_PASSWORD"]) != "structured-password" || spec.SecretEnv["DB_PASSWORD"] != "DB_PASSWORD" {
				t.Fatal("password was not placed in the Secret mapping")
			}
			if strings.Contains(string(spec.Snapshot.Options), "password") || strings.Contains(string(spec.Snapshot.Options), "uri-secret") || strings.Contains(spec.Env["DB_USER"], "password") {
				t.Fatal("credentials appeared in public runner settings or snapshot")
			}
			publicEnv, err := json.Marshal(spec.Env)
			if err != nil || strings.Contains(string(publicEnv), "ca-certificate-sentinel") {
				t.Fatal("CA material was copied into the runner environment")
			}
			if !reflect.DeepEqual(spec.SensitiveValues, []string{test.uri}) {
				t.Fatal("original credential URI was not included as a redaction candidate")
			}
			input.Options[0] = 'x'
			if !json.Valid(spec.Snapshot.Options) {
				t.Fatal("snapshot aliases caller-owned options JSON")
			}
		})
	}
}

func TestPrepareRejectsUnsupportedAndUnsafeInputs(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*driver.PrepareInput)
		kind   driver.ErrorKind
	}{
		{"unsupported target", func(i *driver.PrepareInput) { i.Target.DatabaseType = "mongodb" }, driver.UnsupportedConnection},
		{"unknown option", func(i *driver.PrepareInput) { i.Options = json.RawMessage(`{"credential-sentinel":true}`) }, driver.InvalidOptions},
		{"invalid options", func(i *driver.PrepareInput) { i.Options = json.RawMessage(`{"warehouses":1,"virtualUsers":2}`) }, driver.InvalidOptions},
		{"invalid connection", func(i *driver.PrepareInput) { i.Connection.Port = "bad-port-secret" }, driver.InvalidCredentials},
		{"unsupported TLS verification", func(i *driver.PrepareInput) {
			i.Connection.URI = "mysql://u:uri-secret%21@host/app?ssl-mode=VERIFY_IDENTITY"
		}, driver.UnsupportedConnection},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := driver.PrepareInput{
				Target: driver.TargetInfo{DatabaseType: "mysql"}, Options: json.RawMessage(`{}`),
				Connection: driver.ConnectionDetails{
					Host: "mysql.example.com", Port: "3306", Username: "bench-user",
					Password: "connection-password-secret", Database: "bench_db",
				},
			}
			test.change(&input)
			spec, err := New().Prepare(input)
			var problem *driver.Error
			if !errors.As(err, &problem) || problem.Kind != test.kind {
				t.Fatalf("Prepare() error = %v; want kind %q", err, test.kind)
			}
			if !reflect.DeepEqual(spec, driver.RunnerSpec{}) {
				t.Fatal("failed preparation returned a runner specification")
			}
			for _, secret := range []string{"credential-sentinel", "connection-password-secret", "bad-port-secret", "uri-secret!", "uri-secret%21"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("preparation error exposed submitted credentials or options")
				}
			}
		})
	}
}

func TestPrepareReturnsIndependentSpecificationsForConcurrentCalls(t *testing.T) {
	const calls = 8
	var wait sync.WaitGroup
	errs := make(chan error, calls)
	for range calls {
		wait.Add(1)
		go func() {
			defer wait.Done()
			spec, err := New().Prepare(driver.PrepareInput{
				Target: driver.TargetInfo{DatabaseType: "postgresql"}, Options: json.RawMessage(`{}`),
				Connection: driver.ConnectionDetails{
					Host: "pg.example.com", Port: "5432", Username: "bench-user",
					Password: "password-secret", Database: "bench_db",
				},
			})
			if err != nil {
				errs <- err
				return
			}
			if err := spec.Validate(); err != nil {
				errs <- err
				return
			}
			if spec.Env["DB_TYPE"] != "postgresql" || spec.Env["BENCH_WORKLOAD"] != "tproc-c" {
				errs <- errors.New("concurrent Prepare returned incorrect environment")
			}
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestPrepareRunnerMapsPostgreSQLAndMySQLEnvironments(t *testing.T) {
	for _, test := range []struct {
		name       string
		connection resolvedConnection
		options    Options
		wantEnv    map[string]string
	}{
		{
			name: "PostgreSQL",
			connection: resolvedConnection{
				DatabaseType: "postgresql", Host: "pg.example.com", Port: 5432,
				Database: "bench_db", Username: "bench-user", Password: "password-sentinel",
				PGSSLMode: "require",
			},
			options: Options{Warehouses: 4, VirtualUsers: 2, RampupMinutes: 3, DurationMinutes: 5, Initialize: true},
			wantEnv: map[string]string{
				"DB_TYPE": "postgresql", "DB_HOST": "pg.example.com", "DB_PORT": "5432",
				"DB_NAME": "bench_db", "DB_USER": "bench-user", "PGSSLMODE": "require",
				"BENCH_WORKLOAD": "tproc-c", "BENCH_WAREHOUSES": "4", "BENCH_VIRTUAL_USERS": "2",
				"BENCH_RAMPUP_MINUTES": "3", "BENCH_DURATION_MINUTES": "5", "BENCH_INITIALIZE": "true",
			},
		},
		{
			name: "MySQL",
			connection: resolvedConnection{
				DatabaseType: "mysql", Host: "mysql.example.com", Port: 3306,
				Database: "bench_db", Username: "bench-user", Password: "password-sentinel",
				MySQLSSL: false,
			},
			options: defaultOptions(),
			wantEnv: map[string]string{
				"DB_TYPE": "mysql", "DB_HOST": "mysql.example.com", "DB_PORT": "3306",
				"DB_NAME": "bench_db", "DB_USER": "bench-user", "MYSQL_SSL": "false",
				"BENCH_WORKLOAD": "tproc-c", "BENCH_WAREHOUSES": "1", "BENCH_VIRTUAL_USERS": "1",
				"BENCH_RAMPUP_MINUTES": "0", "BENCH_DURATION_MINUTES": "1", "BENCH_INITIALIZE": "false",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec, err := prepareRunner(test.connection, test.options)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(spec.Env, test.wantEnv) {
				t.Fatalf("runner environment = %#v; want %#v", spec.Env, test.wantEnv)
			}
			if err := spec.Validate(); err != nil {
				t.Fatalf("RunnerSpec.Validate() error = %v", err)
			}
			if len(spec.Command) != 0 || len(spec.Args) != 0 {
				t.Fatal("runner image entrypoint was overridden")
			}
			if !reflect.DeepEqual(spec.SecretData, map[string][]byte{"DB_PASSWORD": []byte("password-sentinel")}) ||
				!reflect.DeepEqual(spec.SecretEnv, map[string]string{"DB_PASSWORD": "DB_PASSWORD"}) {
				t.Fatal("database password was not mapped through the per-run Secret")
			}
			var snapshot Options
			if err := json.Unmarshal(spec.Snapshot.Options, &snapshot); err != nil {
				t.Fatalf("decode effective options snapshot: %v", err)
			}
			if snapshot != test.options || spec.Snapshot.Database != test.connection.Database {
				t.Fatal("snapshot does not contain the effective options and selected database")
			}
		})
	}
}
