package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	"github.com/openeverest/plugin-bench/backend/internal/everest"
)

func instanceDiscoveryAPI(t *testing.T, lookup InstanceLookup) *API {
	t.Helper()
	return NewAPI(
		func(context.Context, string, string, string, string) (*everest.Credentials, error) {
			t.Error("discovery requested credentials")
			return nil, errors.New("unexpected credential lookup")
		},
		func(context.Context, string, string, string, string) error {
			t.Error("discovery invoked the individual instance access check")
			return errors.New("unexpected access check")
		},
		testRunnerFunc(func(context.Context, coordinator.RunInput) (coordinator.Result, error) {
			t.Error("discovery invoked the benchmark runner")
			return coordinator.Result{}, errors.New("unexpected benchmark")
		}),
		NewRunStore(), context.Background(), coordinator.Resources{}, nil, lookup,
	)
}

func serveInstanceDiscovery(a *API, r *http.Request) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	a.RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestListInstancesEndpoint(t *testing.T) {
	want := []everest.InstanceSummary{{K8sCluster: "cluster-a", Namespace: "default", Name: "postgres-1", Provider: "provider-cloudnative-pg"}}
	r := httptest.NewRequest(http.MethodGet, "/api/instances?k8sCluster=cluster-a", nil)
	r.Header.Set("Authorization", "Bearer user-token")
	calls := 0
	a := instanceDiscoveryAPI(t, func(ctx context.Context, token, cluster string) ([]everest.InstanceSummary, error) {
		calls++
		if ctx != r.Context() || token != "user-token" || cluster != "cluster-a" {
			t.Error("discovery did not receive the request context, caller token, and cluster")
		}
		return want, nil
	})
	// Discovery is independent of benchmark admission and must work at capacity.
	for range cap(a.runSlots) {
		a.runSlots <- struct{}{}
	}
	w := serveInstanceDiscovery(a, r)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("GET = %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Instances []everest.InstanceSummary `json:"instances"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || !reflect.DeepEqual(response.Instances, want) {
		t.Fatalf("response = %s, decode error = %v", w.Body.String(), err)
	}
	if calls != 1 || storedRunCount(a) != 0 || len(a.runSlots) != cap(a.runSlots) {
		t.Fatal("discovery changed benchmark run state or called lookup more than once")
	}
}

func TestListInstancesEndpointEmpty(t *testing.T) {
	for _, results := range [][]everest.InstanceSummary{nil, {}} {
		a := instanceDiscoveryAPI(t, func(context.Context, string, string) ([]everest.InstanceSummary, error) {
			return results, nil
		})
		r := httptest.NewRequest(http.MethodGet, "/api/instances?k8sCluster=main", nil)
		r.Header.Set("Authorization", "Bearer token")
		w := serveInstanceDiscovery(a, r)
		if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"instances":[]}` {
			t.Fatalf("empty response = %d: %s", w.Code, w.Body.String())
		}
	}
}

func TestListInstancesEndpointRejectsInvalidRequests(t *testing.T) {
	for _, test := range []struct {
		name, query string
		auth        []string
		status      int
	}{
		{"missing authorization", "?k8sCluster=main", nil, 401},
		{"wrong authorization scheme", "?k8sCluster=main", []string{"Basic private-token"}, 401},
		{"empty bearer", "?k8sCluster=main", []string{"Bearer"}, 401},
		{"multiple authorization headers", "?k8sCluster=main", []string{"Bearer a", "Bearer b"}, 401},
		{"missing cluster", "", []string{"Bearer token"}, 400},
		{"empty cluster", "?k8sCluster=", []string{"Bearer token"}, 400},
		{"blank cluster", "?k8sCluster=%20%20", []string{"Bearer token"}, 400},
		{"duplicate cluster", "?k8sCluster=main&k8sCluster=other", []string{"Bearer token"}, 400},
		{"invalid query encoding", "?k8sCluster=%zz", []string{"Bearer token"}, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := instanceDiscoveryAPI(t, func(context.Context, string, string) ([]everest.InstanceSummary, error) {
				t.Error("invalid request reached discovery")
				return nil, nil
			})
			r := httptest.NewRequest(http.MethodGet, "/api/instances"+test.query, nil)
			for _, auth := range test.auth {
				r.Header.Add("Authorization", auth)
			}
			w := serveInstanceDiscovery(a, r)
			if w.Code != test.status || w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("GET = %d: %s", w.Code, w.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body) != 1 || body["error"] == "" {
				t.Fatalf("invalid error envelope: %s", w.Body.String())
			}
			if test.status == 401 && w.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatal("missing Bearer authentication challenge")
			}
			if strings.Contains(w.Body.String(), "private-token") {
				t.Fatal("error echoed authorization contents")
			}
		})
	}
}

func TestListInstancesEndpointErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
	}{
		{"expired token", fmt.Errorf("private-token: %w", everest.ErrUnauthorized), 401},
		{"forbidden", fmt.Errorf("private-token: %w", everest.ErrForbidden), 403},
		{"missing target", everest.ErrNotFound, 404},
		{"timeout", fmt.Errorf("private-token: %w", context.DeadlineExceeded), 504},
		{"upstream failure", &everest.ResponseError{StatusCode: 500, Kind: everest.ErrUpstream}, 502},
		{"invalid response", &everest.InvalidResponseError{Err: errors.New("private-token")}, 502},
		{"read failure", &everest.ResponseReadError{Err: errors.New("private-token")}, 502},
		{"unexpected failure", errors.New("password=private-token"), 502},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := instanceDiscoveryAPI(t, func(context.Context, string, string) ([]everest.InstanceSummary, error) {
				return []everest.InstanceSummary{{Name: "partial-private-instance"}}, test.err
			})
			r := httptest.NewRequest(http.MethodGet, "/api/instances?k8sCluster=main", nil)
			r.Header.Set("Authorization", "Bearer private-token")
			w := serveInstanceDiscovery(a, r)
			if w.Code != test.status {
				t.Fatalf("GET = %d: %s, want %d", w.Code, w.Body.String(), test.status)
			}
			if strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "password") {
				t.Fatalf("error leaked internal details: %s", w.Body.String())
			}
			if test.status == 401 && w.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatal("missing challenge for expired authentication")
			}
		})
	}
}

func TestListInstancesEndpointPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest(http.MethodGet, "/api/instances?k8sCluster=main", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer token")
	a := instanceDiscoveryAPI(t, func(lookupCtx context.Context, _, _ string) ([]everest.InstanceSummary, error) {
		cancel()
		if lookupCtx != ctx || !errors.Is(lookupCtx.Err(), context.Canceled) {
			t.Error("request cancellation did not reach lookup")
		}
		return nil, lookupCtx.Err()
	})
	w := serveInstanceDiscovery(a, r)
	if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), `"instances"`) {
		t.Fatalf("cancelled discovery returned success or results: %d %s", w.Code, w.Body.String())
	}
}

func TestListInstancesEndpointUnavailable(t *testing.T) {
	a := instanceDiscoveryAPI(t, nil)
	r := httptest.NewRequest(http.MethodGet, "/api/instances?k8sCluster=main", nil)
	r.Header.Set("Authorization", "Bearer token")
	w := serveInstanceDiscovery(a, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET = %d: %s", w.Code, w.Body.String())
	}
}
