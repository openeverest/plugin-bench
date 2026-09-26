package api

import (
	"context"
	"encoding/json"
	"io"
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
	}, testRunner{}, NewRunStore(), context.Background())
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
		{"missing database", `{"target":{"k8sCluster":"local","namespace":"dbs","instance":"postgres-1"},"durationSeconds":30,"clients":1,"threads":1,"scale":1}`, "application/json", http.StatusBadRequest},
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
	}, testRunner{}, NewRunStore(), context.Background())
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
		return coordinator.Result{JobName: "bench-run-job", Output: "done"}, nil
	})
	api := NewAPI(func(context.Context, string, string, string, string) (*everest.Credentials, error) {
		return testCredentials(), nil
	}, runner, store, context.Background())
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
	if err != nil || run.Status != RunStatusSucceeded || run.JobName != "bench-run-job" || run.Output != "done" {
		t.Fatalf("run completion was not stored: run=%+v err=%v", run, err)
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
			}, testRunner{}, NewRunStore(), context.Background())
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
