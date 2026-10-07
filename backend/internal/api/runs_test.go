package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	"github.com/openeverest/plugin-bench/backend/internal/everest"
)

const validCreateRunBody = `{"target":{"k8sCluster":"local","namespace":"dbs","instance":"postgres-1"},"database":"bench","durationSeconds":30,"clients":1,"threads":1,"scale":1}`

func testCredentials() *everest.Credentials {
	return &everest.Credentials{
		Host: "postgres.example.com", Port: "5432", Username: "bench-user",
		Password: "bench-password", Provider: "provider-cloudnative-pg", Type: "postgresql",
	}
}

type testRunner struct{}

func (testRunner) Run(context.Context, coordinator.Connection, coordinator.Options) (coordinator.Result, error) {
	return coordinator.Result{Output: "benchmark complete"}, nil
}

type testRunnerFunc func(context.Context, coordinator.Connection, coordinator.Options) (coordinator.Result, error)

func (f testRunnerFunc) Run(ctx context.Context, connection coordinator.Connection, options coordinator.Options) (coordinator.Result, error) {
	return f(ctx, connection, options)
}

func testAPI() *API {
	return NewAPI(func(context.Context, string, string, string, string) (*everest.Credentials, error) {
		return testCredentials(), nil
	}, nil, testRunner{}, NewRunStore(), context.Background())
}

func TestWriteErrorReturnsJSON(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusBadRequest, "invalid request")

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want %d", w.Code, http.StatusBadRequest)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type=%q, want application/json", got)
	}
	var response map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if response["error"] != "invalid request" {
		t.Fatalf("error=%q, want invalid request", response["error"])
	}
}

func TestExtractBearerToken(t *testing.T) {
	for _, test := range []struct {
		headers []string
		want    string
	}{
		{nil, ""},
		{[]string{"Bearer"}, ""},
		{[]string{"Basic token"}, ""},
		{[]string{"Bearer one two"}, ""},
		{[]string{"Bearer one", "Bearer two"}, ""},
		{[]string{"Bearer token"}, "token"},
		{[]string{"bearer token"}, "token"},
	} {
		r := httptest.NewRequest(http.MethodPost, "/api/runs", nil)
		for _, header := range test.headers {
			r.Header.Add("Authorization", header)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Body = http.NoBody
		if test.want != "" {
			r.Body = io.NopCloser(strings.NewReader(validCreateRunBody))
		}
		got, err := extractBearerToken(r)
		if got != test.want || (err != nil) != (test.want == "") {
			t.Fatalf("headers %v: token=%q, error=%v", test.headers, got, err)
		}
		w := httptest.NewRecorder()
		testAPI().createRun(w, r)
		wantStatus := http.StatusAccepted
		if test.want == "" {
			wantStatus = http.StatusUnauthorized
		}
		if w.Code != wantStatus {
			t.Fatalf("status=%d, want %d", w.Code, wantStatus)
		}
	}
}

func TestCreateRunDecodesRequest(t *testing.T) {
	for _, test := range []struct {
		name, body, contentType string
		wantStatus              int
	}{
		{"valid", validCreateRunBody, "application/json", http.StatusAccepted},
		{"charset", validCreateRunBody, "application/json; charset=utf-8", http.StatusAccepted},
		{"missing instance", `{"target":{"k8sCluster":"local","namespace":"dbs"},"database":"bench","durationSeconds":30,"clients":1,"threads":1,"scale":1}`, "application/json", http.StatusBadRequest},
		{"missing database without instance default", `{"target":{"k8sCluster":"local","namespace":"dbs","instance":"postgres-1"},"durationSeconds":30,"clients":1,"threads":1,"scale":1}`, "application/json", http.StatusUnprocessableEntity},
		{"invalid options", `{"target":{"k8sCluster":"local","namespace":"dbs","instance":"postgres-1"},"database":"bench","durationSeconds":30,"clients":1,"threads":2,"scale":1}`, "application/json", http.StatusBadRequest},
		{"connection string as database", `{"target":{"k8sCluster":"local","namespace":"dbs","instance":"postgres-1"},"database":"postgres://host/db","durationSeconds":30,"clients":1,"threads":1,"scale":1}`, "application/json", http.StatusBadRequest},
		{"malformed", `{`, "application/json", http.StatusBadRequest},
		{"null", `null`, "application/json", http.StatusBadRequest},
		{"unknown field", `{"unexpected":true}`, "application/json", http.StatusBadRequest},
		{"multiple values", `{} {}`, "application/json", http.StatusBadRequest},
		{"wrong type", `[]`, "application/json", http.StatusBadRequest},
		{"wrong content type", `{}`, "text/plain", http.StatusUnsupportedMediaType},
		{"oversized", strings.Repeat(" ", maxCreateRunRequestBytes+1), "application/json", http.StatusRequestEntityTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(test.body))
			r.Header.Set("Authorization", "Bearer test-token")
			r.Header.Set("Content-Type", test.contentType)
			w := httptest.NewRecorder()
			testAPI().createRun(w, r)
			if w.Code != test.wantStatus {
				t.Fatalf("status=%d, want %d; body=%s", w.Code, test.wantStatus, w.Body.String())
			}
		})
	}
}

func TestCreateRunResolvesTargetCredentials(t *testing.T) {
	called := false
	api := NewAPI(func(ctx context.Context, token, cluster, namespace, instance string) (*everest.Credentials, error) {
		called = true
		if ctx == nil || token != "test-token" || cluster != "local" || namespace != "dbs" || instance != "postgres-1" {
			t.Fatalf("unexpected credential lookup arguments: token=%q target=%q/%q/%q", token, cluster, namespace, instance)
		}
		return testCredentials(), nil
	}, nil, testRunner{}, NewRunStore(), context.Background())
	r := httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(validCreateRunBody))
	r.Header.Set("Authorization", "Bearer test-token")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.createRun(w, r)
	if !called {
		t.Fatal("expected credential lookup to be called")
	}
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want %d; body=%s", w.Code, http.StatusAccepted, w.Body.String())
	}
}

func TestCreateRunStoresAndExecutesAsynchronously(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	store := NewRunStore()
	runner := testRunnerFunc(func(_ context.Context, connection coordinator.Connection, options coordinator.Options) (coordinator.Result, error) {
		if connection.Database != "bench" || options.Duration != 30 {
			t.Errorf("unexpected runner inputs: database=%q duration=%d", connection.Database, options.Duration)
		}
		close(started)
		<-release
		return coordinator.Result{JobName: "bench-run-job", Output: "pgbench password=bench-password done"}, nil
	})
	api := NewAPI(func(context.Context, string, string, string, string) (*everest.Credentials, error) {
		return testCredentials(), nil
	}, nil, runner, store, context.Background())
	r := httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(validCreateRunBody))
	r.Header.Set("Authorization", "Bearer test-token")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.createRun(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want %d; body=%s", w.Code, http.StatusAccepted, w.Body.String())
	}
	var accepted struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.ID == "" || accepted.Status != string(RunStatusRunning) {
		t.Fatalf("unexpected acceptance response: %+v", accepted)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("runner was not started")
	}
	run, err := store.Get(accepted.ID)
	if err != nil || run.Status != RunStatusRunning {
		t.Fatalf("run should be retained as running while runner is blocked: run=%+v err=%v", run, err)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		run, err = store.Get(accepted.ID)
		if err == nil && run.Status == RunStatusSucceeded {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil || run.Status != RunStatusSucceeded || run.JobName != "bench-run-job" || run.Output != "pgbench password=[REDACTED] done" {
		t.Fatalf("run completion was not stored: run=%+v err=%v", run, err)
	}
}

func TestCreateRunDefaultsToInstanceDatabase(t *testing.T) {
	store := NewRunStore()
	databases := make(chan string, 1)
	api := NewAPI(func(context.Context, string, string, string, string) (*everest.Credentials, error) {
		credentials := testCredentials()
		credentials.Database = "app"
		return credentials, nil
	}, nil, testRunnerFunc(func(_ context.Context, connection coordinator.Connection, _ coordinator.Options) (coordinator.Result, error) {
		databases <- connection.Database
		return coordinator.Result{}, nil
	}), store, context.Background())
	body := `{"target":{"k8sCluster":"local","namespace":"dbs","instance":"postgres-1"},"database":" ","durationSeconds":30,"clients":1,"threads":1,"scale":1}`
	r := httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer test-token")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.createRun(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want %d; body=%s", w.Code, http.StatusAccepted, w.Body.String())
	}
	select {
	case database := <-databases:
		if database != "app" {
			t.Fatalf("runner database=%q, want app", database)
		}
	case <-time.After(time.Second):
		t.Fatal("runner was not started")
	}
	var accepted struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	run, err := store.Get(accepted.ID)
	if err != nil || run.Request.Database != "app" {
		t.Fatalf("resolved database was not stored: run=%+v err=%v", run, err)
	}
}

func TestShutdownWaitsForActiveRuns(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	api := NewAPI(func(context.Context, string, string, string, string) (*everest.Credentials, error) {
		return testCredentials(), nil
	}, nil, testRunnerFunc(func(context.Context, coordinator.Connection, coordinator.Options) (coordinator.Result, error) {
		close(started)
		<-release
		return coordinator.Result{}, nil
	}), NewRunStore(), context.Background())

	r := httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(validCreateRunBody))
	r.Header.Set("Authorization", "Bearer test-token")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.createRun(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want %d", w.Code, http.StatusAccepted)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("runner was not started")
	}

	shutdown := make(chan error, 1)
	go func() {
		shutdown <- api.Shutdown(context.Background())
	}()
	select {
	case err := <-shutdown:
		t.Fatalf("Shutdown returned before the run finished: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-shutdown:
		if err != nil {
			t.Fatalf("Shutdown() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not wait for the active run")
	}
}

func TestShutdownRejectsNewRuns(t *testing.T) {
	api := testAPI()
	if err := api.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	r := httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(validCreateRunBody))
	r.Header.Set("Authorization", "Bearer test-token")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.createRun(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestCreateRunMapsCredentialLookupErrors(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"unauthorized", everest.ErrUnauthorized, http.StatusUnauthorized},
		{"forbidden", everest.ErrForbidden, http.StatusForbidden},
		{"not found", everest.ErrNotFound, http.StatusNotFound},
		{"upstream error", context.DeadlineExceeded, http.StatusGatewayTimeout},
		{"upstream failure", everest.ErrUpstream, http.StatusBadGateway},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := NewAPI(func(context.Context, string, string, string, string) (*everest.Credentials, error) {
				return nil, test.err
			}, nil, testRunner{}, NewRunStore(), context.Background())
			r := httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(validCreateRunBody))
			r.Header.Set("Authorization", "Bearer test-token")
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			api.createRun(w, r)
			if w.Code != test.wantStatus {
				t.Fatalf("status=%d, want %d", w.Code, test.wantStatus)
			}
			if strings.Contains(w.Body.String(), "test-token") {
				t.Fatal("response exposed the authorization token")
			}
		})
	}
}

func TestConnectionFromCredentials(t *testing.T) {
	connection, err := connectionFromCredentials("bench", testCredentials())
	if err != nil {
		t.Fatal(err)
	}
	if connection.Database != "bench" || connection.Port != 5432 || connection.Password != "bench-password" {
		t.Fatalf("unexpected connection mapping: %+v", connection)
	}

	for _, credentials := range []*everest.Credentials{
		{Host: "postgres.example.com", Port: "5432", Username: "user", Password: "secret", Provider: "provider-mongodb", Type: "mongodb"},
		{Host: "postgres.example.com", Port: "5432", Username: "user", URI: "postgres://user:secret@host/db", Provider: "provider-cloudnative-pg", Type: "postgresql"},
	} {
		if _, err := connectionFromCredentials("bench", credentials); err == nil {
			t.Fatal("expected unsupported credentials to be rejected")
		}
	}
}

func TestConnectionFromCredentialsResolvesDefaultDatabase(t *testing.T) {
	for _, test := range []struct {
		name, requested, database, uri, want string
	}{
		{name: "explicit database wins", requested: " custom ", database: "app", uri: "postgresql://u:p@host:5432/other", want: "custom"},
		{name: "database key", database: "app", uri: "postgresql://u:p@host:5432/other", want: "app"},
		{name: "uri path", uri: "postgresql://u:p@host:5432/pg-cluster?sslmode=require", want: "pg-cluster"},
		{name: "escaped uri path", uri: "postgresql://u:p@host:5432/my%20db", want: "my db"},
		{name: "uri without path", uri: "postgresql://u:p@host:5432"},
		{name: "nested uri path", uri: "postgresql://u:p@host:5432/a/b"},
		{name: "no hints"},
	} {
		t.Run(test.name, func(t *testing.T) {
			credentials := testCredentials()
			credentials.Database = test.database
			credentials.URI = test.uri
			connection, err := connectionFromCredentials(test.requested, credentials)
			if test.want == "" {
				if err == nil || !strings.Contains(err.Error(), "default database") {
					t.Fatalf("expected default database error, got connection=%+v err=%v", connection, err)
				}
				return
			}
			if err != nil || connection.Database != test.want {
				t.Fatalf("database=%q err=%v, want %q", connection.Database, err, test.want)
			}
		})
	}
}

func TestGetRun(t *testing.T) {
	tests := []struct {
		name            string
		exists          bool
		authorization   string
		accessErr       error
		omitAccessCheck bool
		status          RunStatus
		result          coordinator.Result
		runErr          string
		wantHTTP        int
		wantAccessCheck bool
	}{
		{name: "missing authorization", exists: true, wantHTTP: http.StatusUnauthorized},
		{name: "unknown ID", authorization: "Bearer test-token", wantHTTP: http.StatusNotFound},
		{name: "access check unavailable", exists: true, authorization: "Bearer test-token", omitAccessCheck: true, wantHTTP: http.StatusServiceUnavailable},
		{name: "target access denied", exists: true, authorization: "Bearer test-token", accessErr: everest.ErrForbidden, wantHTTP: http.StatusForbidden, wantAccessCheck: true},
		{name: "running status", exists: true, authorization: "Bearer test-token", status: RunStatusRunning, wantHTTP: http.StatusOK, wantAccessCheck: true},
		{name: "successful result", exists: true, authorization: "Bearer test-token", status: RunStatusSucceeded, result: coordinator.Result{JobName: "bench-job", Output: "sanitized log", OutputTruncated: true}, wantHTTP: http.StatusOK, wantAccessCheck: true},
		{name: "failed result", exists: true, authorization: "Bearer test-token", status: RunStatusFailed, result: coordinator.Result{JobName: "failed-job", Output: "partial sanitized log"}, runErr: "benchmark execution failed", wantHTTP: http.StatusOK, wantAccessCheck: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewRunStore()
			if test.exists {
				run, err := store.Create("run-123", CreateRunRequest{
					Target:   Target{K8sCluster: "local", Namespace: "databases", Instance: "postgres-1"},
					Database: "app",
				})
				if err != nil {
					t.Fatal(err)
				}
				if test.status == RunStatusSucceeded || test.status == RunStatusFailed {
					if err := store.Complete(run.ID, test.status, test.result, test.runErr); err != nil {
						t.Fatal(err)
					}
				}
			}

			credentialLookupCalled := false
			accessCheckCalled := false
			api := NewAPI(func(_ context.Context, token, cluster, namespace, instance string) (*everest.Credentials, error) {
				credentialLookupCalled = true
				return testCredentials(), nil
			}, func(_ context.Context, token, cluster, namespace, instance string) error {
				accessCheckCalled = true
				if token != "test-token" || cluster != "local" || namespace != "databases" || instance != "postgres-1" {
					t.Fatalf("unexpected instance access check arguments: token=%q target=%q/%q/%q", token, cluster, namespace, instance)
				}
				return test.accessErr
			}, testRunner{}, store, context.Background())
			if test.omitAccessCheck {
				api.checkInstanceAccess = nil
			}

			r := httptest.NewRequest(http.MethodGet, "/api/runs/run-123", nil)
			r.SetPathValue("id", "run-123")
			if test.authorization != "" {
				r.Header.Set("Authorization", test.authorization)
			}
			w := httptest.NewRecorder()
			var logOutput bytes.Buffer
			previousLogOutput := log.Writer()
			log.SetOutput(&logOutput)
			defer log.SetOutput(previousLogOutput)
			api.getRun(w, r)
			if w.Code != test.wantHTTP {
				t.Fatalf("status=%d, want %d; body=%s", w.Code, test.wantHTTP, w.Body.String())
			}
			if credentialLookupCalled {
				t.Fatal("status retrieval must not fetch database credentials")
			}
			if accessCheckCalled != test.wantAccessCheck {
				t.Fatalf("instance access check called=%v, want %v", accessCheckCalled, test.wantAccessCheck)
			}
			if test.accessErr != nil {
				if !strings.Contains(logOutput.String(), `benchmark run "run-123" access check failed`) {
					t.Fatalf("access-check failure was not logged: %q", logOutput.String())
				}
				if strings.Contains(logOutput.String(), "test-token") || strings.Contains(logOutput.String(), "bench-password") {
					t.Fatal("access-check log exposed a token or database password")
				}
			}
			if test.omitAccessCheck && !strings.Contains(logOutput.String(), "instance access check is unavailable") {
				t.Fatalf("missing access-check dependency was not logged: %q", logOutput.String())
			}
			if test.wantHTTP != http.StatusOK {
				if strings.Contains(w.Body.String(), "test-token") || strings.Contains(w.Body.String(), "bench-password") {
					t.Fatal("error response exposed sensitive data")
				}
				return
			}

			var response RunStatusResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.ID != "run-123" || response.Status != test.status || response.Database != "app" || response.CreatedAt.IsZero() {
				t.Fatalf("unexpected run status response: %+v", response)
			}
			if test.status == RunStatusRunning {
				if response.CompletedAt != nil || response.Output != "" {
					t.Fatalf("running response contains completion data: %+v", response)
				}
				return
			}
			if response.CompletedAt == nil || response.JobName != test.result.JobName || response.Output != test.result.Output || response.OutputTruncated != test.result.OutputTruncated || response.Error != test.runErr {
				t.Fatalf("result fields do not match stored run: %+v", response)
			}
			if strings.Contains(w.Body.String(), "bench-password") || strings.Contains(w.Body.String(), "test-token") {
				t.Fatal("run response exposed credentials or authorization token")
			}
		})
	}
}
