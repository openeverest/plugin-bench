package api

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	"github.com/openeverest/plugin-bench/backend/internal/driver"
	"github.com/openeverest/plugin-bench/backend/internal/driver/goycsb"
	"github.com/openeverest/plugin-bench/backend/internal/everest"
	"github.com/openeverest/plugin-bench/backend/internal/runnerresult"
	"go.mongodb.org/mongo-driver/x/mongo/driver/connstring"
)

const goycsbRunnerImage = "example.com/plugin-bench-go-ycsb:test"
const goycsbRunBody = `{"target":{"k8sCluster":"local","namespace":"dbs","instance":"mongo-1"},"driver":"go-ycsb","options":{}}`

func goycsbCredentials() *everest.Credentials {
	return &everest.Credentials{
		Host: "mongo.example.com", Port: "27017", Username: "mongo-user", Password: "mongo-password",
		Type: "MongoDB", Provider: "percona-server-mongodb",
		URI: "mongodb://mongo-user:mongo-password@mongo.example.com:27017/admin?ssl=false",
	}
}

func newGoYCSBAPI(t *testing.T, credentials *everest.Credentials, runner BenchmarkRunner, defaults coordinator.Resources) *API {
	t.Helper()
	registry, err := driver.NewRegistry(driver.Registration{
		Driver: goycsb.New(), Runner: driver.RunnerConfig{Image: goycsbRunnerImage, ImagePullPolicy: "Never"},
	})
	if err != nil {
		t.Fatal(err)
	}
	checkTarget := func(token, cluster, namespace, instance string) {
		if token != "test-token" || cluster != "local" || namespace != "dbs" || instance != "mongo-1" {
			t.Error("OpenEverest lookup did not use the caller's token and target")
		}
	}
	return NewAPI(func(_ context.Context, token, cluster, namespace, instance string) (*everest.Credentials, error) {
		checkTarget(token, cluster, namespace, instance)
		return credentials, nil
	}, func(_ context.Context, token, cluster, namespace, instance string) error {
		checkTarget(token, cluster, namespace, instance)
		return nil
	}, runner, NewRunStore(), context.Background(), defaults, registry, nil)
}

func getGoYCSBRun(a *API, id, authorization string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	a.RegisterRoutes(mux)
	r := httptest.NewRequest(http.MethodGet, "/api/runs/"+id, nil)
	r.Header.Set("Authorization", authorization)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestGoYCSBRunRetainsEffectiveSettings(t *testing.T) {
	for _, test := range []struct {
		name, database, options, authSource string
		uriOnly                             bool
		wantOptions                         goycsb.Options
	}{
		{"defaults and provider admin path", "", `{}`, "admin", false, goycsb.Options{Collection: "usertable", RecordCount: 10000, OperationCount: 20000, Threads: 1}},
		{"explicit options and URI-only credentials", " ycsb ", `{"collection":"benchmark.records","recordCount":500,"operationCount":1000,"threads":4,"initialize":true}`, "accounts", true, goycsb.Options{Collection: "benchmark.records", RecordCount: 500, OperationCount: 1000, Threads: 4, Initialize: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			credentials := goycsbCredentials()
			if test.uriOnly {
				credentials.Host, credentials.Port, credentials.Username, credentials.Password = "", "", "", ""
				credentials.URI += "&authSource=accounts"
			}
			affinity := testNodeAffinity()
			calls := make(chan coordinator.RunInput, 1)
			a := newGoYCSBAPI(t, credentials, testRunnerFunc(func(_ context.Context, input coordinator.RunInput) (coordinator.Result, error) {
				calls <- input
				return coordinator.Result{Output: "go-ycsb complete"}, nil
			}), coordinator.Resources{CPURequest: "100m", CPULimit: "1"})
			request := CreateRunRequest{
				Target: Target{K8sCluster: "local", Namespace: "dbs", Instance: "mongo-1"},
				Driver: "go-ycsb", Database: test.database, Options: json.RawMessage(test.options),
				Resources: coordinator.Resources{CPURequest: "500m"}, NodeAffinity: affinity,
			}
			body, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			run := awaitAcceptedRun(t, a, submitRun(a, string(body)))
			var input coordinator.RunInput
			select {
			case input = <-calls:
			default:
				t.Fatal("accepted go-ycsb run never reached execution")
			}
			if input.Runner != (driver.RunnerConfig{Image: goycsbRunnerImage, ImagePullPolicy: "Never"}) ||
				input.Spec.SecretEnv["MONGODB_URI"] != "MONGODB_URI" {
				t.Fatal("execution lost the registered image or Secret reference")
			}
			connection, err := connstring.ParseAndValidate(string(input.Spec.SecretData["MONGODB_URI"]))
			if err != nil || connection.Database != "ycsb" || connection.AuthSource != test.authSource || connection.Password != "mongo-password" {
				t.Fatal("execution lost the ycsb target or MongoDB authentication")
			}
			if _, exists := input.Spec.Env["MONGODB_URI"]; exists {
				t.Fatal("credential URI was included in literal environment settings")
			}
			if input.Resources != request.Resources || !reflect.DeepEqual(input.NodeAffinity, affinity) {
				t.Fatal("preparation lost shared resource or affinity overrides")
			}
			var effective goycsb.Options
			if err := json.Unmarshal(input.Spec.Snapshot.Options, &effective); err != nil || effective != test.wantOptions {
				t.Fatalf("execution did not receive effective options: %v", err)
			}
			// Execution data must not alias the completed run's saved settings.
			input.Spec.Snapshot.Options[0] = 'X'
			mutateTestNodeAffinity(input.NodeAffinity)
			w := getGoYCSBRun(a, run.ID, "Bearer test-token")
			var response RunStatusResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusOK || response.Status != RunStatusSucceeded || response.Driver != "go-ycsb" ||
				response.RunnerImage != goycsbRunnerImage || response.Database != "ycsb" || response.Output != "go-ycsb complete" {
				t.Fatalf("unexpected go-ycsb status response: %s", w.Body.String())
			}
			if err := json.Unmarshal(response.Options, &effective); err != nil || effective != test.wantOptions {
				t.Fatalf("GET lost effective options: %v", err)
			}
			if response.Resources != (coordinator.Resources{CPURequest: "500m", CPULimit: "1"}) || !reflect.DeepEqual(&response.NodeAffinity, affinity) {
				t.Fatal("GET lost effective resource defaults or its owned affinity snapshot")
			}
			stored, err := json.Marshal(run)
			if err != nil {
				t.Fatal(err)
			}
			for _, private := range []string{"mongo-password", credentials.URI, "mongo.example.com", "mongo-user", "SecretData", "SecretEnv", "MONGODB_URI"} {
				if strings.Contains(w.Body.String(), private) || bytes.Contains(stored, []byte(private)) {
					t.Fatal("stored run or GET exposed internal preparation data")
				}
			}
		})
	}
}

func TestGoYCSBPreparationErrorsRejectBeforeAdmission(t *testing.T) {
	for _, test := range []struct {
		name, body string
		change     func(*everest.Credentials)
		disabled   bool
		status     int
	}{
		{"unknown option", strings.Replace(goycsbRunBody, `"options":{}`, `"options":{"credential-sentinel":1}`, 1), nil, false, 400},
		{"zero threads", strings.Replace(goycsbRunBody, `"options":{}`, `"options":{"threads":0}`, 1), nil, false, 400},
		{"null collection", strings.Replace(goycsbRunBody, `"options":{}`, `"options":{"collection":null}`, 1), nil, false, 400},
		{"other requested database", strings.Replace(goycsbRunBody, `"driver":"go-ycsb"`, `"driver":"go-ycsb","database":"other"`, 1), nil, false, 400},
		{"disabled driver", goycsbRunBody, nil, true, 400},
		{"unsupported target", goycsbRunBody, func(c *everest.Credentials) { c.Type = "mysql" }, false, 422},
		{"unsupported URI", goycsbRunBody, func(c *everest.Credentials) { c.URI = "mongodb+srv://credential-sentinel.invalid" }, false, 422},
		{"missing URI", goycsbRunBody, func(c *everest.Credentials) { c.URI = "" }, false, 502},
		{"malformed URI", goycsbRunBody, func(c *everest.Credentials) { c.URI = "mongodb://user:credential-sentinel@host:99999" }, false, 502},
		{"missing database type", goycsbRunBody, func(c *everest.Credentials) { c.Type = "" }, false, 502},
		{"invalid TLS CA", goycsbRunBody, func(c *everest.Credentials) { c.CACertificate = "credential-sentinel" }, false, 502},
	} {
		t.Run(test.name, func(t *testing.T) {
			credentials := goycsbCredentials()
			if test.change != nil {
				test.change(credentials)
			}
			var called atomic.Bool
			a := newGoYCSBAPI(t, credentials, testRunnerFunc(func(context.Context, coordinator.RunInput) (coordinator.Result, error) {
				called.Store(true)
				return coordinator.Result{}, nil
			}), coordinator.Resources{})
			if test.disabled {
				a.registry = testRegistry()
			}
			w := submitRun(a, test.body)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := a.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			if w.Code != test.status || called.Load() || len(a.store.runs) != 0 || len(a.runSlots) != 0 {
				t.Fatalf("POST = %d: %s; invalid request was admitted", w.Code, w.Body.String())
			}
			for _, secret := range []string{"credential-sentinel", "mongo-password", credentials.URI} {
				if secret != "" && strings.Contains(w.Body.String(), secret) {
					t.Fatal("preparation error exposed credentials")
				}
			}
		})
	}
}

func TestGoYCSBResultsRedactSecretsAndPreserveMetrics(t *testing.T) {
	safeMetrics := []runnerresult.Metric{{Name: "ops_per_second", Unit: "ops/s", Value: 27017.25}, {Name: "latency_avg", Unit: "ms", Value: 1.2}}
	for _, test := range []struct {
		name    string
		metrics bool
		failed  bool
	}{
		{"successful metrics", true, false},
		{"failed run with metrics", true, true},
		{"successful run without metrics", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			const password = "mongo+p@ss/word"
			credentials := goycsbCredentials()
			credentials.Password = password
			credentials.URI = "mongodb://mongo-user:" + url.QueryEscape(password) + "@mongo.example.com:27017/admin?ssl=false"
			var diagnostics bytes.Buffer
			original := log.Writer()
			log.SetOutput(&diagnostics)
			defer log.SetOutput(original)
			a := newGoYCSBAPI(t, credentials, testRunnerFunc(func(_ context.Context, input coordinator.RunInput) (coordinator.Result, error) {
				effectiveURI := string(input.Spec.SecretData["MONGODB_URI"])
				output := "host=mongo.example.com user=mongo-user port=27017 ops=27017.25 password=" + password +
					" encoded=" + url.QueryEscape(password) + " original=" + credentials.URI + " effective=" + effectiveURI
				result := coordinator.Result{Output: output, OutputTruncated: true}
				if test.metrics {
					result.Metrics = append([]runnerresult.Metric(nil), safeMetrics...)
					result.Metrics = append(result.Metrics, runnerresult.Metric{Name: password, Unit: "ms", Value: 1}, runnerresult.Metric{Name: "unsafe", Unit: effectiveURI, Value: 2})
				}
				if test.failed {
					return result, errors.New(output)
				}
				return result, nil
			}), coordinator.Resources{})
			run := awaitAcceptedRun(t, a, submitRun(a, goycsbRunBody))
			wantStatus, wantError := RunStatusSucceeded, ""
			if test.failed {
				wantStatus, wantError = RunStatusFailed, "benchmark execution failed"
			}
			var wantMetrics []runnerresult.Metric
			if test.metrics {
				wantMetrics = safeMetrics
			}
			const wantOutput = "host=mongo.example.com user=mongo-user port=27017 ops=27017.25 password=[REDACTED] encoded=[REDACTED] original=[REDACTED] effective=[REDACTED]"
			if run.Status != wantStatus || run.Error != wantError || run.Output != wantOutput || !run.OutputTruncated || !reflect.DeepEqual(run.Metrics, wantMetrics) {
				t.Fatal("stored results lost status, redacted output, or structured metrics")
			}
			w := getGoYCSBRun(a, run.ID, "Bearer test-token")
			var response RunStatusResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusOK || response.Status != wantStatus || response.Error != wantError || response.Output != wantOutput ||
				!response.OutputTruncated || !reflect.DeepEqual(response.Metrics, wantMetrics) {
				t.Fatal("GET did not retain stored output, status, and metrics")
			}
			if strings.Contains(w.Body.String(), `"metrics":`) != test.metrics {
				t.Fatal("unavailable metrics must be omitted")
			}
			for _, secret := range []string{password, url.QueryEscape(password), credentials.URI} {
				if strings.Contains(w.Body.String(), secret) || strings.Contains(diagnostics.String(), secret) {
					t.Fatal("API results or backend diagnostics exposed credentials")
				}
			}
			if test.failed && !strings.Contains(diagnostics.String(), wantOutput) {
				t.Fatal("failure diagnostic did not preserve safely redacted details")
			}
		})
	}
}

func TestGoYCSBRunResultsRequireTargetAccess(t *testing.T) {
	a := newGoYCSBAPI(t, goycsbCredentials(), testRunnerFunc(func(context.Context, coordinator.RunInput) (coordinator.Result, error) {
		return coordinator.Result{Output: "private-mongodb-output", Metrics: []runnerresult.Metric{{Name: "ops_per_second", Unit: "ops/s", Value: 10}}}, nil
	}), coordinator.Resources{})
	run := awaitAcceptedRun(t, a, submitRun(a, goycsbRunBody))
	for _, test := range []struct {
		name, authorization string
		accessErr           error
		status              int
	}{
		{"unauthenticated", "", nil, http.StatusUnauthorized},
		{"unauthorized", "Bearer test-token", everest.ErrUnauthorized, http.StatusUnauthorized},
		{"forbidden", "Bearer test-token", everest.ErrForbidden, http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			a.checkInstanceAccess = func(_ context.Context, token, cluster, namespace, instance string) error {
				if token != "test-token" || cluster != run.Target.K8sCluster || namespace != run.Target.Namespace || instance != run.Target.Instance {
					t.Error("GET did not authorize the stored run target")
				}
				return test.accessErr
			}
			w := getGoYCSBRun(a, run.ID, test.authorization)
			if w.Code != test.status || strings.Contains(w.Body.String(), "private-mongodb-output") ||
				strings.Contains(w.Body.String(), `"metrics":`) || strings.Contains(w.Body.String(), `"options":`) || strings.Contains(w.Body.String(), goycsbRunnerImage) {
				t.Fatalf("unauthorized GET exposed run details: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func testCACertificate(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestGoYCSBRunSuppliesProviderCACertificate(t *testing.T) {
	credentials := goycsbCredentials()
	credentials.URI = strings.Replace(credentials.URI, "ssl=false", "tls=true", 1)
	credentials.CACertificate = testCACertificate(t)
	calls := make(chan coordinator.RunInput, 1)
	a := newGoYCSBAPI(t, credentials, testRunnerFunc(func(_ context.Context, input coordinator.RunInput) (coordinator.Result, error) {
		calls <- input
		return coordinator.Result{}, nil
	}), coordinator.Resources{})
	awaitAcceptedRun(t, a, submitRun(a, goycsbRunBody))
	var input coordinator.RunInput
	select {
	case input = <-calls:
	default:
		t.Fatal("accepted go-ycsb run never reached execution")
	}
	if string(input.Spec.SecretData["MONGODB_TLS_CA"]) != credentials.CACertificate ||
		input.Spec.SecretEnv["MONGODB_TLS_CA"] != "MONGODB_TLS_CA" {
		t.Fatal("execution did not receive the provider CA through the per-run Secret")
	}
}
