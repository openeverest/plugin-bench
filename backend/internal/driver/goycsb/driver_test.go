package goycsb

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
	"go.mongodb.org/mongo-driver/x/mongo/driver/connstring"
)

func TestDriverSupportsDatabaseType(t *testing.T) {
	d := New()
	if d.ID() != "go-ycsb" {
		t.Fatalf("ID() = %q; want go-ycsb", d.ID())
	}
	for _, databaseType := range []string{"mongodb", "MongoDB", " MONGODB "} {
		if !d.Supports(driver.TargetInfo{DatabaseType: databaseType, Provider: "unfamiliar-provider"}) {
			t.Errorf("MongoDB type %q was not supported", databaseType)
		}
	}
	for _, databaseType := range []string{"", "mysql", "postgresql"} {
		if d.Supports(driver.TargetInfo{DatabaseType: databaseType, Provider: "mongodb-provider"}) {
			t.Errorf("non-MongoDB type %q was supported", databaseType)
		}
	}
}

func TestPrepareMapsRunnerContractAndEffectiveSnapshot(t *testing.T) {
	input := driver.PrepareInput{
		Target: driver.TargetInfo{DatabaseType: " MongoDB ", Provider: "new-mongodb-provider"},
		Connection: driver.ConnectionDetails{
			URI: "mongodb://bench:p%40ss%2B%25%2F@mongo/admin?ssl=false",
		},
		RequestedDatabase: " ycsb ",
		Options:           json.RawMessage("{\"collection\":\"bench_records\",\"recordCount\":2500,\"operationCount\":5000,\"threads\":4,\"initialize\":true}"),
	}
	before := input
	before.Options = bytes.Clone(input.Options)

	spec, err := New().Prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("Prepare returned an invalid runner spec: %v", err)
	}
	if !reflect.DeepEqual(input, before) {
		t.Fatal("Prepare mutated caller-owned input")
	}
	if len(spec.Command) != 0 || len(spec.Args) != 0 {
		t.Fatal("Prepare replaced the runner image entrypoint")
	}

	wantEnv := map[string]string{
		"BENCH_COLLECTION":      "bench_records",
		"BENCH_RECORD_COUNT":    "2500",
		"BENCH_OPERATION_COUNT": "5000",
		"BENCH_THREADS":         "4",
		"BENCH_INITIALIZE":      "true",
	}
	if !reflect.DeepEqual(spec.Env, wantEnv) {
		t.Fatalf("Env = %v; want %v", spec.Env, wantEnv)
	}
	if !reflect.DeepEqual(spec.SecretEnv, map[string]string{"MONGODB_URI": "MONGODB_URI"}) {
		t.Fatalf("SecretEnv = %v; want MONGODB_URI mapped to its Secret key", spec.SecretEnv)
	}
	if len(spec.SecretData) != 1 {
		t.Fatalf("SecretData has %d entries; want only the MongoDB URI", len(spec.SecretData))
	}
	secretURI := string(spec.SecretData["MONGODB_URI"])
	effective, err := connstring.ParseAndValidate(secretURI)
	if err != nil || effective.Database != benchmarkDatabase || effective.AuthSource != "admin" {
		t.Fatal("runner Secret does not target ycsb while preserving admin authentication")
	}
	if effective.Username != "bench" || effective.Password != "p@ss+%/" || !strings.Contains(secretURI, "ssl=false") {
		t.Fatal("runner Secret lost URI credentials or connection options")
	}

	var snapshotOptions Options
	if err := json.Unmarshal(spec.Snapshot.Options, &snapshotOptions); err != nil {
		t.Fatal(err)
	}
	wantOptions := Options{Collection: "bench_records", RecordCount: 2500, OperationCount: 5000, Threads: 4, Initialize: true}
	if snapshotOptions != wantOptions || spec.Snapshot.Database != benchmarkDatabase {
		t.Fatalf("snapshot = database %q, options %+v; want ycsb and %+v", spec.Snapshot.Database, snapshotOptions, wantOptions)
	}

	public, err := json.Marshal(struct {
		Env      map[string]string
		Snapshot driver.OptionSnapshot
	}{spec.Env, spec.Snapshot})
	if err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range []string{"bench:p%40ss%2B%25%2F", "p@ss+%/", "mongodb://bench:"} {
		if bytes.Contains(public, []byte(sensitive)) {
			t.Fatalf("credential representation %q escaped into ordinary workload fields or snapshot", sensitive)
		}
	}
	if !containsString(spec.SensitiveValues, input.Connection.URI) || !containsString(spec.SensitiveValues, "p@ss+%/") ||
		!containsString(spec.SensitiveValues, url.QueryEscape("p@ss+%/")) || !containsString(spec.SensitiveValues, url.PathEscape("p@ss+%/")) {
		t.Fatal("URI or password representations were not included for output redaction")
	}
}

func TestPrepareUsesDefaultsInRunnerAndSnapshot(t *testing.T) {
	uri := "mongodb://mongo"
	spec, err := New().Prepare(driver.PrepareInput{
		Target:     driver.TargetInfo{DatabaseType: "mongodb"},
		Connection: driver.ConnectionDetails{URI: uri},
		Options:    json.RawMessage("{}"),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantOptions := defaultOptions()
	wantEnv := map[string]string{
		"BENCH_COLLECTION":      wantOptions.Collection,
		"BENCH_RECORD_COUNT":    "10000",
		"BENCH_OPERATION_COUNT": "20000",
		"BENCH_THREADS":         "1",
		"BENCH_INITIALIZE":      "false",
	}
	if !reflect.DeepEqual(spec.Env, wantEnv) {
		t.Fatalf("Env = %v; want defaults %v", spec.Env, wantEnv)
	}
	var got Options
	if err := json.Unmarshal(spec.Snapshot.Options, &got); err != nil {
		t.Fatal(err)
	}
	if got != wantOptions || spec.Snapshot.Database != benchmarkDatabase {
		t.Fatalf("snapshot = database %q, options %+v; want ycsb and %+v", spec.Snapshot.Database, got, wantOptions)
	}
	if !containsString(spec.SensitiveValues, uri) {
		t.Fatal("original URI was not retained for output redaction")
	}
}

func TestPrepareRejectsInvalidInput(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*driver.PrepareInput)
		kind   driver.ErrorKind
	}{
		{"unsupported target", func(i *driver.PrepareInput) { i.Target.DatabaseType = "mysql" }, driver.UnsupportedConnection},
		{"unknown option", func(i *driver.PrepareInput) { i.Options = json.RawMessage("{\"credential-sentinel\":1}") }, driver.InvalidOptions},
		{"invalid option", func(i *driver.PrepareInput) { i.Options = json.RawMessage("{\"threads\":0}") }, driver.InvalidOptions},
		{"other requested database", func(i *driver.PrepareInput) { i.RequestedDatabase = "other" }, driver.InvalidOptions},
		{"missing URI", func(i *driver.PrepareInput) { i.Connection.URI = "" }, driver.InvalidCredentials},
		{"invalid URI", func(i *driver.PrepareInput) { i.Connection.URI = "mongodb://mongo:99999" }, driver.InvalidCredentials},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := driver.PrepareInput{
				Target:            driver.TargetInfo{DatabaseType: "mongodb"},
				Connection:        driver.ConnectionDetails{URI: "mongodb://user:credential-sentinel@mongo/admin"},
				RequestedDatabase: "ycsb",
				Options:           json.RawMessage("{}"),
			}
			test.change(&input)
			spec, err := New().Prepare(input)
			var problem *driver.Error
			if !errors.As(err, &problem) || problem.Kind != test.kind {
				t.Fatalf("error = %v; want driver error kind %q", err, test.kind)
			}
			if !reflect.DeepEqual(spec, driver.RunnerSpec{}) {
				t.Fatal("failed preparation returned a workload")
			}
			if strings.Contains(err.Error(), "credential-sentinel") {
				t.Fatal("preparation error exposed submitted credentials")
			}
		})
	}
}

func TestPrepareReturnsIndependentRunnerSpecs(t *testing.T) {
	input := driver.PrepareInput{
		Target:     driver.TargetInfo{DatabaseType: "mongodb"},
		Connection: driver.ConnectionDetails{URI: "mongodb://user:credential-sentinel@mongo/admin"},
		Options:    json.RawMessage("{\"threads\":2}"),
	}
	first, err := New().Prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New().Prepare(input)
	if err != nil {
		t.Fatal(err)
	}

	first.Env["BENCH_THREADS"] = "changed"
	first.SecretData["MONGODB_URI"][0] = 'x'
	first.SecretEnv["MONGODB_URI"] = "changed"
	first.Snapshot.Options[0] = 'x'
	first.SensitiveValues[0] = "changed"

	if second.Env["BENCH_THREADS"] != "2" || string(second.SecretData["MONGODB_URI"]) == string(first.SecretData["MONGODB_URI"]) ||
		second.SecretEnv["MONGODB_URI"] != "MONGODB_URI" || !json.Valid(second.Snapshot.Options) || second.SensitiveValues[0] == "changed" {
		t.Fatal("separate preparations share mutable data")
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestPrepareSuppliesTLSCertificateAuthorityThroughSecret(t *testing.T) {
	ca := testCACertificate(t)
	spec, err := New().Prepare(driver.PrepareInput{
		Target:     driver.TargetInfo{DatabaseType: "mongodb"},
		Connection: driver.ConnectionDetails{URI: "mongodb://mongo/admin?tls=true", CACertificate: ca},
		Options:    json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("Prepare returned an invalid runner spec: %v", err)
	}
	if string(spec.SecretData["MONGODB_TLS_CA"]) != ca || spec.SecretEnv["MONGODB_TLS_CA"] != "MONGODB_TLS_CA" {
		t.Fatal("the provider CA must reach the runner through the per-run Secret")
	}
	if _, exists := spec.Env["MONGODB_TLS_CA"]; exists || bytes.Contains(spec.Snapshot.Options, []byte("CERTIFICATE")) {
		t.Fatal("the provider CA must stay out of literal settings and the saved snapshot")
	}
}
