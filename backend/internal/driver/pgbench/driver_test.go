package pgbench

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
	if d.ID() != "pgbench" {
		t.Fatalf("ID() = %q", d.ID())
	}
	for _, databaseType := range []string{"postgresql", "PostgreSQL", " POSTGRESQL "} {
		if !d.Supports(driver.TargetInfo{DatabaseType: databaseType, Provider: "unfamiliar-provider"}) {
			t.Fatalf("PostgreSQL type %q was not supported", databaseType)
		}
	}
	for _, databaseType := range []string{"", "mysql", "mongodb"} {
		if d.Supports(driver.TargetInfo{DatabaseType: databaseType, Provider: "provider-percona-postgresql"}) {
			t.Fatalf("non-PostgreSQL type %q was supported", databaseType)
		}
	}
}

func TestPrepareMapsRunnerContractAndEffectiveSnapshot(t *testing.T) {
	input := driver.PrepareInput{
		Target:     driver.TargetInfo{DatabaseType: "PostgreSQL", Provider: "new-postgresql-provider"},
		Connection: validConnectionDetails(), RequestedDatabase: " custom ",
		Options: json.RawMessage(`{"clients":4,"threads":2,"initialize":true}`),
	}
	input.Connection.URI = "postgresql://bench-user:credential-sentinel@postgres.example.com/app"
	beforeOptions := bytes.Clone(input.Options)
	d := New()
	spec, err := d.Prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(input.Options, beforeOptions) {
		t.Fatal("Prepare mutated caller options")
	}
	if len(spec.Command) != 0 || len(spec.Args) != 0 {
		t.Fatal("Prepare replaced the runner image entrypoint")
	}
	wantEnv := map[string]string{"BENCH_DURATION": "60", "BENCH_CLIENTS": "4", "BENCH_THREADS": "2", "BENCH_SCALE": "1", "BENCH_INITIALIZE": "true"}
	if !reflect.DeepEqual(spec.Env, wantEnv) {
		t.Fatalf("Env = %v; want %v", spec.Env, wantEnv)
	}
	wantSecrets := map[string]string{"PGHOST": "postgres.example.com", "PGPORT": "5432", "PGDATABASE": "custom", "PGUSER": "bench-user", "PGPASSWORD": "credential-sentinel"}
	if len(spec.SecretData) != len(wantSecrets) || len(spec.SecretEnv) != len(wantSecrets) {
		t.Fatal("unexpected credential mapping")
	}
	for key, value := range wantSecrets {
		if string(spec.SecretData[key]) != value || spec.SecretEnv[key] != key {
			t.Fatalf("incorrect Secret mapping for %s", key)
		}
	}
	var snapshot Options
	if err := json.Unmarshal(spec.Snapshot.Options, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot != (Options{DurationSeconds: 60, Clients: 4, Threads: 2, Scale: 1, Initialize: true}) || spec.Snapshot.Database != "custom" {
		t.Fatalf("snapshot does not contain effective options: %+v", snapshot)
	}
	public, err := json.Marshal(struct {
		Env           map[string]string
		Command, Args []string
		Snapshot      driver.OptionSnapshot
	}{spec.Env, spec.Command, spec.Args, spec.Snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(public, []byte(input.Connection.Password)) || bytes.Contains(public, []byte(input.Connection.URI)) {
		t.Fatal("credentials escaped into non-secret workload fields or snapshot")
	}
	if !reflect.DeepEqual(spec.SensitiveValues, []string{input.Connection.URI}) {
		t.Fatal("additional credential URI was not retained for redaction")
	}
	// Returned data must not alias the input JSON or be reused by later preparations.
	input.Options[0] = 'x'
	if !json.Valid(spec.Snapshot.Options) {
		t.Fatal("snapshot aliases caller JSON")
	}
	spec.SecretData["PGPASSWORD"][0] = 'x'
	spec.SecretEnv["PGPASSWORD"] = "changed"
	spec.Env["BENCH_CLIENTS"] = "99"
	spec.Snapshot.Options[0] = 'x'
	spec.SensitiveValues[0] = "changed"
	input.Options = beforeOptions
	again, err := d.Prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	if string(again.SecretData["PGPASSWORD"]) != input.Connection.Password || again.Env["BENCH_CLIENTS"] != "4" || again.SecretEnv["PGPASSWORD"] != "PGPASSWORD" || !json.Valid(again.Snapshot.Options) || again.SensitiveValues[0] != input.Connection.URI {
		t.Fatal("preparations share mutable state")
	}
}

func TestPrepareRejectsInvalidInput(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*driver.PrepareInput)
		kind   driver.ErrorKind
	}{
		{"unsupported target", func(i *driver.PrepareInput) { i.Target.DatabaseType = "mongodb" }, driver.UnsupportedConnection},
		{"bad options", func(i *driver.PrepareInput) { i.Options = json.RawMessage(`{"credential-sentinel":1}`) }, driver.InvalidOptions},
		{"bad connection", func(i *driver.PrepareInput) { i.Connection.Port = "credential-sentinel" }, driver.InvalidCredentials},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := driver.PrepareInput{Target: driver.TargetInfo{DatabaseType: "postgresql"}, Connection: validConnectionDetails(), Options: json.RawMessage(`{}`)}
			test.change(&input)
			spec, err := New().Prepare(input)
			assertErrorKind(t, err, test.kind)
			if !reflect.DeepEqual(spec, driver.RunnerSpec{}) {
				t.Fatal("failed preparation returned a workload")
			}
			if strings.Contains(err.Error(), "credential-sentinel") {
				t.Fatal("preparation error leaked input")
			}
		})
	}
}

func TestPrepareDefaultSnapshotAndRedaction(t *testing.T) {
	spec, err := New().Prepare(driver.PrepareInput{Target: driver.TargetInfo{DatabaseType: "postgresql"}, Connection: validConnectionDetails(), Options: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(spec.Snapshot.Options, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 5 || snapshot["initialize"] != false || snapshot["durationSeconds"] != float64(60) {
		t.Fatal("defaulted options were omitted from snapshot")
	}
	if len(spec.SensitiveValues) != 0 {
		t.Fatal("SecretData values were unnecessarily repeated in SensitiveValues")
	}
}
