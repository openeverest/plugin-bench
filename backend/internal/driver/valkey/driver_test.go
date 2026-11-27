package valkey

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

func TestPrepareRunnerSpec(t *testing.T) {
	cert, key := testCertificatePair(t)
	for _, mode := range []struct {
		name, scheme, tls, ca, cert, key string
	}{
		{name: "plaintext", scheme: "redis", tls: "false"},
		{name: "tls", scheme: "rediss", tls: "true", ca: cert},
		{name: "mtls", scheme: "rediss", tls: "true", ca: cert, cert: cert, key: key},
	} {
		t.Run(mode.name, func(t *testing.T) {
			const password = "test pass+word"
			uri := mode.scheme + "://default:test+pass%2Bword@cache:6379"
			input := driver.PrepareInput{
				Target: driver.TargetInfo{DatabaseType: "valkey", Provider: "valkey"},
				Connection: driver.ConnectionDetails{
					Host: "cache", Port: "6379", Username: "default", Password: password, URI: uri,
					CACertificate: mode.ca, ClientCertificate: mode.cert, ClientKey: mode.key,
				},
				Options: json.RawMessage(`{"duration":300,"keyCount":100000}`),
			}
			d := New()
			spec, err := d.Prepare(input)
			if err != nil {
				t.Fatal(err)
			}
			if err := spec.Validate(); err != nil {
				t.Fatal(err)
			}
			wantEnv := map[string]string{
				"VALKEY_HOST": "cache", "VALKEY_PORT": "6379", "VALKEY_USERNAME": "default", "VALKEY_TLS": mode.tls,
				"BENCH_DURATION": "300", "BENCH_THREADS": "2", "BENCH_CLIENTS_PER_THREAD": "10",
				"BENCH_KEY_COUNT": "100000", "BENCH_VALUE_SIZE": "256",
			}
			if d.ID() != "valkey" || !maps.Equal(spec.Env, wantEnv) {
				t.Fatal("runner ID or environment differs from the contract")
			}
			wantSecrets := map[string]string{"VALKEY_PASSWORD": password}
			if mode.ca != "" {
				wantSecrets["VALKEY_TLS_CA"] = mode.ca
			}
			if mode.cert != "" {
				wantSecrets["VALKEY_TLS_CERT"], wantSecrets["VALKEY_TLS_KEY"] = mode.cert, mode.key
			}
			if len(spec.SecretData) != len(wantSecrets) || len(spec.SecretEnv) != len(wantSecrets) {
				t.Fatal("unexpected number of secret entries")
			}
			for name, value := range wantSecrets {
				if string(spec.SecretData[name]) != value || spec.SecretEnv[name] != name {
					t.Errorf("missing or incorrect secret mapping for %s", name)
				}
			}
			const wantOptions = `{"duration":300,"threads":2,"clientsPerThread":10,"keyCount":100000,"valueSize":256}`
			if spec.MinimumDuration != 300*time.Second || spec.Snapshot.Database != "0" || string(spec.Snapshot.Options) != wantOptions {
				t.Fatal("incorrect minimum duration or public snapshot")
			}
			if !slices.Contains(spec.SensitiveValues, uri) || !slices.Contains(spec.SensitiveValues, "test+pass%2Bword") {
				t.Fatal("missing credential redaction representations")
			}
		})
	}
}

func TestPrepareRejectsUnsupportedTarget(t *testing.T) {
	_, err := New().Prepare(driver.PrepareInput{Target: driver.TargetInfo{DatabaseType: "valkey", Provider: "other"}})
	checkConnectionError(t, err, driver.UnsupportedConnection)
}
