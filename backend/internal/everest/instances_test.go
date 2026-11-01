package everest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func TestListInstancesMetadata(t *testing.T) {
	requests := make(chan string, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Path
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer user-token" {
			t.Error("discovery must forward the user's token on every GET")
		}
		switch r.URL.Path {
		case "/v1/clusters/main/namespaces":
			io.WriteString(w, `["zeta","alpha","alpha","","  "]`)
		case "/v1/clusters/main/namespaces/zeta/instances":
			io.WriteString(w, `{"items":[{"metadata":{"name":"shared"},"spec":{"providerRef":{"name":"percona-server-mysql"}}}]}`)
		case "/v1/clusters/main/namespaces/alpha/instances":
			io.WriteString(w, `{"items":[
				{"metadata":{"name":"shared","namespace":"wrong"},"spec":{"providerRef":{"name":"provider-cloudnative-pg"}},"password":"private-value","uri":"private-uri"},
				{"metadata":{"name":"shared"},"spec":{"providerRef":{"name":"provider-cloudnative-pg"}}},
				{"metadata":{"name":"mongo"},"spec":{"providerRef":{"name":"percona-server-mongodb"}}},
				{"metadata":{"name":""},"spec":{"providerRef":{"name":"provider-cloudnative-pg"}}},
				{"metadata":{"name":"  "},"spec":{"providerRef":{"name":"provider-cloudnative-pg"}}},
				{"metadata":{"name":"no-provider"}},
				{"metadata":{"name":"blank-provider"},"spec":{"providerRef":{"name":" "}}}
			]}`)
		default:
			t.Errorf("unexpected discovery request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("EVEREST_API_URL", server.URL)

	got, err := ListInstances(context.Background(), "user-token", "main")
	if err != nil {
		t.Fatal(err)
	}
	want := []InstanceSummary{
		{K8sCluster: "main", Namespace: "alpha", Name: "mongo", Provider: "percona-server-mongodb"},
		{K8sCluster: "main", Namespace: "alpha", Name: "shared", Provider: "provider-cloudnative-pg"},
		{K8sCluster: "main", Namespace: "zeta", Name: "shared", Provider: "percona-server-mysql"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("instances = %#v, want %#v", got, want)
	}
	if len(requests) != 3 {
		t.Fatalf("made %d requests, want one namespace request and two instance requests", len(requests))
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-value", "private-uri", "password", "metadata", "spec"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("metadata projection leaked %q", forbidden)
		}
	}
}

func TestListInstancesEmpty(t *testing.T) {
	for _, namespaces := range []string{`[]`, `["default"]`} {
		t.Run(namespaces, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/namespaces") {
					io.WriteString(w, namespaces)
				} else {
					io.WriteString(w, `{"items":[]}`)
				}
			}))
			defer server.Close()
			t.Setenv("EVEREST_API_URL", server.URL)
			got, err := ListInstances(context.Background(), "token", "main")
			if err != nil || got == nil || len(got) != 0 {
				t.Fatalf("instances = %#v, error = %v; want non-nil empty list", got, err)
			}
		})
	}
}

func TestListInstancesIsolatesUsers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/namespaces") {
			io.WriteString(w, `["default"]`)
			return
		}
		name := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{
			"metadata": map[string]string{"name": name},
			"spec":     map[string]any{"providerRef": map[string]string{"name": "provider-cloudnative-pg"}},
		}}})
	}))
	defer server.Close()
	t.Setenv("EVEREST_API_URL", server.URL)
	for _, token := range []string{"alice", "bob"} {
		got, err := ListInstances(context.Background(), token, "main")
		if err != nil || len(got) != 1 || got[0].Name != token {
			t.Fatalf("user %s: instances = %#v, error = %v", token, got, err)
		}
	}
}

func TestListInstancesEscapesPaths(t *testing.T) {
	paths := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.EscapedPath()
		if strings.HasSuffix(r.URL.Path, "/namespaces") {
			io.WriteString(w, `["ns/name?#"]`)
		} else {
			io.WriteString(w, `{"items":[]}`)
		}
	}))
	defer server.Close()
	t.Setenv("EVEREST_API_URL", server.URL)
	if _, err := ListInstances(context.Background(), "token", "cluster/name?#"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"/v1/clusters/cluster%2Fname%3F%23/namespaces",
		"/v1/clusters/cluster%2Fname%3F%23/namespaces/ns%2Fname%3F%23/instances",
	} {
		if got := <-paths; got != want {
			t.Fatalf("path = %q, want %q", got, want)
		}
	}
}

func TestListInstancesUpstreamErrors(t *testing.T) {
	for _, stage := range []string{"namespaces", "instances"} {
		for _, status := range []int{401, 403, 404, 500, 502} {
			t.Run(stage+"/"+http.StatusText(status), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if stage == "instances" && strings.HasSuffix(r.URL.Path, "/namespaces") {
						io.WriteString(w, `["first","second"]`)
					} else if strings.Contains(r.URL.Path, "/first/instances") {
						io.WriteString(w, `{"items":[{"metadata":{"name":"db"},"spec":{"providerRef":{"name":"pg"}}}]}`)
					} else {
						http.Error(w, "password=private-upstream-body", status)
					}
				}))
				defer server.Close()
				t.Setenv("EVEREST_API_URL", server.URL)
				got, err := ListInstances(context.Background(), "token", "main")
				var responseErr *ResponseError
				if got != nil || !errors.As(err, &responseErr) || responseErr.StatusCode != status || !errors.Is(err, errorKindForStatus(status)) {
					t.Fatalf("instances = %#v, error = %v; want typed status %d and no partial results", got, err, status)
				}
				if strings.Contains(err.Error(), "private-upstream-body") {
					t.Fatal("upstream error body was exposed")
				}
			})
		}
	}
}

func TestListInstancesInvalidResponses(t *testing.T) {
	for _, stage := range []string{"namespaces", "instances"} {
		bodies := []string{`null`, `{}`, `"private-body"`, `{"items":`, `<html>private-body</html>`}
		if stage == "namespaces" {
			bodies = append(bodies, `[42]`)
		} else {
			bodies = append(bodies, `[]`, `{"items":null}`, `{"items":{}}`, `{"items":[{"metadata":{"name":42}}]}`)
		}
		for _, body := range bodies {
			t.Run(stage+"/"+body, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if stage == "instances" && strings.HasSuffix(r.URL.Path, "/namespaces") {
						io.WriteString(w, `["default"]`)
					} else {
						io.WriteString(w, body)
					}
				}))
				defer server.Close()
				t.Setenv("EVEREST_API_URL", server.URL)
				got, err := ListInstances(context.Background(), "token", "main")
				var invalid *InvalidResponseError
				if got != nil || !errors.As(err, &invalid) {
					t.Fatalf("instances = %#v, error = %v; want invalid response", got, err)
				}
				if strings.Contains(err.Error(), "private-body") {
					t.Fatal("invalid upstream contents were exposed")
				}
			})
		}
	}
}

type discoveryTransport func(*http.Request) (*http.Response, error)

func (f discoveryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestListInstancesSharedDeadline(t *testing.T) {
	original := everestHTTPClient
	t.Cleanup(func() { everestHTTPClient = original })
	t.Setenv("EVEREST_API_URL", "http://everest.test")
	var deadline time.Time
	calls := 0
	started := time.Now()
	// Disable the per-request client deadline here to inspect the shared one.
	everestHTTPClient = &http.Client{Transport: discoveryTransport(func(r *http.Request) (*http.Response, error) {
		got, ok := r.Context().Deadline()
		if !ok || got.Before(started.Add(20*time.Second)) || got.After(time.Now().Add(20*time.Second)) {
			return nil, errors.New("discovery must set a twenty-second deadline")
		}
		if !deadline.IsZero() && !deadline.Equal(got) {
			return nil, errors.New("namespace traversal reset the discovery deadline")
		}
		deadline = got
		calls++
		body := `["default"]`
		if calls == 2 {
			body = `{"items":[]}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	if _, err := ListInstances(context.Background(), "token", "main"); err != nil || calls != 2 {
		t.Fatalf("calls = %d, error = %v", calls, err)
	}
}

func TestListInstancesStopsOnCancellationAndTimeout(t *testing.T) {
	for _, mode := range []string{"cancellation", "context timeout", "client timeout"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			original := everestHTTPClient
			everestHTTPClient = &http.Client{Timeout: 2 * time.Second}
			t.Cleanup(func() { everestHTTPClient = original })
			want := error(context.DeadlineExceeded)
			if mode == "context timeout" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 100*time.Millisecond)
				defer stop()
			} else if mode == "client timeout" {
				everestHTTPClient.Timeout = 100 * time.Millisecond
			} else {
				want = context.Canceled
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/namespaces") {
					io.WriteString(w, `["first","must-not-be-requested"]`)
					return
				}
				if strings.Contains(r.URL.Path, "must-not-be-requested") {
					t.Error("continued discovery after cancellation")
				}
				if mode == "cancellation" {
					cancel()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			t.Setenv("EVEREST_API_URL", server.URL)
			got, err := ListInstances(ctx, "token", "main")
			if got != nil || !errors.Is(err, want) {
				t.Fatalf("instances = %#v, error = %v; want %v", got, err, want)
			}
		})
	}
}

func TestListInstancesReadFailure(t *testing.T) {
	original := everestHTTPClient
	t.Cleanup(func() { everestHTTPClient = original })
	t.Setenv("EVEREST_API_URL", "http://everest.test")
	readErr := errors.New("read interrupted")
	everestHTTPClient = &http.Client{Transport: discoveryTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(iotest.ErrReader(readErr)), Header: make(http.Header)}, nil
	})}
	got, err := ListInstances(context.Background(), "token", "main")
	var readFailure *ResponseReadError
	if got != nil || !errors.As(err, &readFailure) || !errors.Is(err, readErr) {
		t.Fatalf("instances = %#v, error = %v; want response read failure", got, err)
	}
}
