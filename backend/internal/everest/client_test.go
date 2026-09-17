package everest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type roundTripper struct {
	handler func(*http.Request) (*http.Response, error)
}

func (r roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return r.handler(req)
}

type failingReadCloser struct {
	err error
}

func (r failingReadCloser) Read([]byte) (int, error) {
	return 0, r.err
}

func (r failingReadCloser) Close() error {
	return nil
}

func TestValidateCredentials(t *testing.T) {
	valid := Credentials{
		Host:     "postgres.example.com",
		Port:     "5432",
		Username: "postgres",
		Password: "secret",
	}

	tests := []struct {
		name        string
		credentials Credentials
		wantError   bool
	}{
		{name: "valid", credentials: valid},
		{name: "uri can replace password", credentials: Credentials{
			Host:     valid.Host,
			Port:     valid.Port,
			Username: valid.Username,
			URI:      "postgres://postgres@example.com/bench",
		}},
		{name: "missing host", credentials: Credentials{
			Port:     valid.Port,
			Username: valid.Username,
			Password: valid.Password,
		}, wantError: true},
		{name: "invalid port", credentials: Credentials{
			Host:     valid.Host,
			Port:     "70000",
			Username: valid.Username,
			Password: valid.Password,
		}, wantError: true},
		{name: "missing username", credentials: Credentials{
			Host:     valid.Host,
			Port:     valid.Port,
			Password: valid.Password,
		}, wantError: true},
		{name: "missing password and URI", credentials: Credentials{
			Host:     valid.Host,
			Port:     valid.Port,
			Username: valid.Username,
		}, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateCredentials(test.credentials)
			if (err != nil) != test.wantError {
				t.Fatalf("validateCredentials() error = %v, wantError %v", err, test.wantError)
			}
		})
	}
}

func TestGetCredentialsSuccessfulRetrieval(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/v1/clusters/cluster-a/namespaces/database/instances/postgres/connection" {
			t.Errorf("path = %s, want connection endpoint", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer test-token")
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"uri":"postgres://postgres:secret@postgres.example.com:5432/app","host":"postgres.example.com","port":"5432","username":"postgres","password":"secret","provider":"provider-cloudnative-pg","type":"postgresql"}`))
	}))
	defer server.Close()
	t.Setenv("EVEREST_API_URL", server.URL)

	credentials, err := GetCredentials(context.Background(), "test-token", "cluster-a", "database", "postgres")
	if err != nil {
		t.Fatalf("GetCredentials() error = %v", err)
	}
	want := Credentials{
		URI:  "postgres://postgres:secret@postgres.example.com:5432/app",
		Host: "postgres.example.com", Port: "5432", Username: "postgres",
		Password: "secret", Provider: "provider-cloudnative-pg", Type: "postgresql",
	}
	if credentials == nil || *credentials != want {
		t.Fatal("GetCredentials() did not return all expected connection fields")
	}
}

func TestGetCredentialsAuthorizationFailures(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "upstream body password=secret", status)
			}))
			defer server.Close()
			t.Setenv("EVEREST_API_URL", server.URL)

			credentials, err := GetCredentials(context.Background(), "test-token", "cluster-a", "database", "postgres")
			if credentials != nil {
				t.Fatal("expected nil credentials on authorization failure")
			}
			if err == nil {
				t.Fatalf("GetCredentials() error = nil, want status %d", status)
			}
			var responseErr *ResponseError
			if !errors.As(err, &responseErr) || responseErr.StatusCode != status {
				t.Fatalf("GetCredentials() error = %v, want ResponseError with status %d", err, status)
			}
			wantKind := ErrUnauthorized
			if status == http.StatusForbidden {
				wantKind = ErrForbidden
			}
			if !errors.Is(err, wantKind) {
				t.Fatalf("GetCredentials() error = %v, want %v", err, wantKind)
			}
			if strings.Contains(err.Error(), "password=secret") {
				t.Fatalf("GetCredentials() exposed upstream response body: %v", err)
			}
		})
	}
}

func TestGetCredentialsMissingTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "instance not found", http.StatusNotFound)
	}))
	defer server.Close()
	t.Setenv("EVEREST_API_URL", server.URL)

	credentials, err := GetCredentials(context.Background(), "test-token", "cluster-a", "database", "missing")
	if credentials != nil {
		t.Fatal("expected nil credentials for missing target")
	}
	if err == nil {
		t.Fatal("GetCredentials() error = nil, want not found error")
	}
	var responseErr *ResponseError
	if !errors.As(err, &responseErr) || responseErr.StatusCode != http.StatusNotFound {
		t.Fatalf("GetCredentials() error = %v, want ResponseError with status 404", err)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetCredentials() error = %v, want ErrNotFound", err)
	}
	if strings.Contains(err.Error(), "instance not found") {
		t.Fatalf("GetCredentials() exposed upstream response body: %v", err)
	}
}

func TestGetCredentialsUpstreamFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "password=secret", http.StatusBadGateway)
	}))
	defer server.Close()
	t.Setenv("EVEREST_API_URL", server.URL)

	credentials, err := GetCredentials(context.Background(), "test-token", "cluster-a", "database", "postgres")
	if credentials != nil {
		t.Fatal("expected nil credentials for upstream failure")
	}
	var responseErr *ResponseError
	if !errors.As(err, &responseErr) || responseErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("GetCredentials() error = %v, want ResponseError with status 502", err)
	}
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("GetCredentials() error = %v, want ErrUpstream", err)
	}
	if strings.Contains(err.Error(), "password=secret") {
		t.Fatalf("GetCredentials() exposed upstream response body: %v", err)
	}
}

func TestGetCredentialsInvalidResponses(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantError string
	}{
		{name: "invalid JSON", body: `{"host":`, wantError: "failed to decode credentials:"},
		{name: "non-JSON", body: `<html>upstream error</html>`, wantError: "failed to decode credentials:"},
		{name: "wrong field type", body: `{"port":5432}`, wantError: "failed to decode credentials:"},
		{name: "missing required fields", body: `{"port":"5432","username":"postgres","password":"secret"}`, wantError: "invalid OpenEverest credentials response: missing host"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			t.Setenv("EVEREST_API_URL", server.URL)

			credentials, err := GetCredentials(context.Background(), "test-token", "cluster-a", "database", "postgres")
			if credentials != nil {
				t.Fatal("expected nil credentials for invalid response")
			}
			if err == nil {
				t.Fatal("GetCredentials() error = nil, want invalid response error")
			}
			var invalidErr *InvalidResponseError
			if !errors.As(err, &invalidErr) {
				t.Fatalf("GetCredentials() error = %v, want InvalidResponseError", err)
			}
			if !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("GetCredentials() error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestGetCredentialsResponseReadError(t *testing.T) {
	original := everestHTTPClient
	readErr := errors.New("body read failed")
	everestHTTPClient = &http.Client{
		Transport: roundTripper{
			handler: func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       failingReadCloser{err: readErr},
					Header:     make(http.Header),
					Request:    req,
				}, nil
			},
		},
	}
	t.Cleanup(func() { everestHTTPClient = original })
	t.Setenv("EVEREST_API_URL", "http://everest.test")

	credentials, err := GetCredentials(context.Background(), "test-token", "cluster-a", "database", "postgres")
	if credentials != nil {
		t.Fatal("expected nil credentials when response body cannot be read")
	}
	var responseReadErr *ResponseReadError
	if !errors.As(err, &responseReadErr) {
		t.Fatalf("GetCredentials() error = %v, want ResponseReadError", err)
	}
	if !errors.Is(err, readErr) {
		t.Fatalf("GetCredentials() error = %v, want underlying body read error", err)
	}
}

func TestGetCredentialsTimeoutAndCancellation(t *testing.T) {
	// These tests replace the shared client and must remain sequential.
	if everestHTTPClient.Timeout != 10*time.Second {
		t.Fatal("production client must have a 10-second timeout")
	}
	for _, mode := range []string{"client timeout", "context deadline", "explicit cancellation"} {
		t.Run(mode, func(t *testing.T) {
			original := everestHTTPClient
			everestHTTPClient = &http.Client{Timeout: 5 * time.Second}
			t.Cleanup(func() { everestHTTPClient = original })
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			wantError := error(context.DeadlineExceeded)
			switch mode {
			case "client timeout":
				everestHTTPClient.Timeout = 100 * time.Millisecond
			case "context deadline":
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer deadlineCancel()
			case "explicit cancellation":
				wantError = context.Canceled
			}
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				if mode == "explicit cancellation" {
					cancel() // Cancel after the request reaches the server.
				}
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer server.Close()
			defer close(release) // Ensure server cleanup even if an assertion fails.
			t.Setenv("EVEREST_API_URL", server.URL)
			started := time.Now()
			credentials, err := GetCredentials(ctx, "test-token", "cluster-a", "database", "postgres")
			if time.Since(started) >= 3*time.Second {
				t.Fatal("request reached the fallback timeout instead of stopping promptly")
			}
			if credentials != nil {
				t.Fatal("expected nil credentials on timeout or cancellation")
			}
			if !errors.Is(err, wantError) {
				t.Fatalf("error = %v, want %v", err, wantError)
			}
			if mode == "client timeout" && ctx.Err() != nil {
				t.Fatal("caller deadline fired; HTTP client timeout did not stop the request first")
			}
		})
	}
}

func TestValidateCredentialsEdgeCases(t *testing.T) {
	for _, test := range []struct {
		name, field, value, wantError string
	}{
		{"empty port", "port", "", "invalid port"},
		{"non-numeric port", "port", "abc", "invalid port"},
		{"zero port", "port", "0", "invalid port"},
		{"negative port", "port", "-1", "invalid port"},
		{"lower boundary", "port", "1", ""},
		{"upper boundary", "port", "65535", ""},
		{"above upper boundary", "port", "65536", "invalid port"},
		{"blank host", "host", " \t", "missing host"},
		{"blank username", "username", " \t", "missing username"},
		{"blank password and URI", "password", " \t", "missing password or URI"},
	} {
		t.Run(test.name, func(t *testing.T) {
			creds := Credentials{Host: "postgres.example.com", Port: "5432", Username: "postgres", Password: "secret", URI: " "}
			switch test.field {
			case "port":
				creds.Port = test.value
			case "host":
				creds.Host = test.value
			case "username":
				creds.Username = test.value
			case "password":
				creds.Password = test.value
			}
			err := validateCredentials(creds)
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else if err == nil || err.Error() != test.wantError {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
		})
	}
}
