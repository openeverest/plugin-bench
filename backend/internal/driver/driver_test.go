package driver

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func exampleSpec() RunnerSpec {
	return RunnerSpec{
		Command: []string{"runner"}, Args: []string{"--benchmark"},
		Env:             map[string]string{"DURATION": "60"},
		SecretData:      map[string][]byte{"password": []byte("credential-value")},
		SecretEnv:       map[string]string{"PASSWORD": "password"},
		SensitiveValues: []string{"derived-credential-value"},
		Snapshot:        OptionSnapshot{Database: "app", Options: json.RawMessage(`{"duration":60}`)},
	}
}

func TestRunnerSpecCloneOwnsMutableValues(t *testing.T) {
	original := exampleSpec()
	before := exampleSpec()
	clone := original.Clone()
	if !reflect.DeepEqual(clone, original) {
		t.Fatal("clone changed the workload")
	}
	original.Command[0] = "changed"
	original.Args[0] = "changed"
	original.Env["DURATION"] = "1"
	original.SecretData["password"][0] = 'x'
	original.SecretData["new"] = []byte("new")
	original.SecretEnv["PASSWORD"] = "new"
	original.SensitiveValues[0] = "changed"
	original.Snapshot.Options[0] = 'x'
	if !reflect.DeepEqual(clone, before) {
		t.Fatal("mutating the source changed the clone")
	}
	other := clone.Clone()
	other.SecretData["password"][0] = 'y'
	other.Env["DURATION"] = "2"
	other.Snapshot.Options[0] = 'y'
	if !reflect.DeepEqual(clone, before) {
		t.Fatal("independent clones share mutable values")
	}
}

func TestRunnerSpecClonePreservesNilAndEmptyValues(t *testing.T) {
	for _, spec := range []RunnerSpec{
		{},
		{Command: []string{}, Args: []string{}, Env: map[string]string{}, SecretData: map[string][]byte{"nil": nil, "empty": {}}, SecretEnv: map[string]string{}, SensitiveValues: []string{}, Snapshot: OptionSnapshot{Options: json.RawMessage{}}},
	} {
		if !reflect.DeepEqual(spec.Clone(), spec) {
			t.Fatal("clone changed nil or empty values")
		}
	}
}

func TestRunnerSpecValidation(t *testing.T) {
	for _, test := range []struct {
		name      string
		change    func(*RunnerSpec)
		wantError bool
	}{
		{"valid", func(*RunnerSpec) {}, false},
		{"default entrypoint", func(s *RunnerSpec) { s.Command = nil; s.Args = nil }, false},
		{"no secrets", func(s *RunnerSpec) { s.SecretData = nil; s.SecretEnv = nil }, false},
		{"missing Secret key", func(s *RunnerSpec) { s.SecretEnv["PASSWORD"] = "missing" }, true},
		{"blank Secret reference", func(s *RunnerSpec) { s.SecretEnv["PASSWORD"] = "" }, true},
		{"duplicate environment name", func(s *RunnerSpec) { s.Env["PASSWORD"] = "credential-value" }, true},
		{"blank environment name", func(s *RunnerSpec) { s.Env[""] = "credential-value" }, true},
		{"blank secret environment name", func(s *RunnerSpec) { s.SecretEnv[""] = "password" }, true},
		{"blank secret key", func(s *RunnerSpec) { s.SecretData[""] = []byte("credential-value") }, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := exampleSpec()
			test.change(&spec)
			before := spec.Clone()
			err := spec.Validate()
			if (err != nil) != test.wantError {
				t.Fatalf("Validate() = %v; wantError %v", err, test.wantError)
			}
			if err != nil && strings.Contains(err.Error(), "credential-value") {
				t.Fatal("validation error contains a secret")
			}
			if !reflect.DeepEqual(spec, before) {
				t.Fatal("validation mutated the workload")
			}
		})
	}
}

func TestDriverErrorCategories(t *testing.T) {
	seen := make(map[string]bool)
	for _, kind := range []ErrorKind{InvalidOptions, DriverSelection, UnsupportedConnection, InvalidCredentials} {
		err := &Error{Kind: kind, Message: "a safe explanation"}
		if err.Kind != kind || !strings.Contains(err.Error(), "a safe explanation") {
			t.Fatal("error lost category or safe message")
		}
		if seen[err.Error()] {
			t.Fatal("error categories have indistinguishable descriptions")
		}
		seen[err.Error()] = true
		if got := fmt.Sprint(&Error{Kind: kind}); got == "" {
			t.Fatal("empty error description")
		}
	}
}
