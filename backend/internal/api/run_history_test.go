package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	"github.com/openeverest/plugin-bench/backend/internal/everest"
	"github.com/openeverest/plugin-bench/backend/internal/runnerresult"
)

var historyTarget = Target{K8sCluster: "main", Namespace: "dbs", Instance: "postgres-1"}

const historyPath = "/api/runs?k8sCluster=main&namespace=dbs&instance=postgres-1"

type accessCall struct{ token, cluster, namespace, instance string }

func historyAPI(store RunStore, accessErr error, calls *[]accessCall) http.Handler {
	access := func(_ context.Context, token, cluster, namespace, instance string) error {
		*calls = append(*calls, accessCall{token, cluster, namespace, instance})
		return accessErr
	}
	mux := http.NewServeMux()
	newTestAPI(nil, access, testRunner{}, store, context.Background(), coordinator.Resources{}).RegisterRoutes(mux)
	return mux
}

func getHistory(handler http.Handler, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("Authorization", "Bearer user-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func seedHistory(t *testing.T, store RunStore, target Target, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, err := store.Create(context.Background(), id, target, RunSnapshot{Driver: "pgbench", Options: []byte(`{"clients":1}`)}, coordinator.Resources{}, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestListRunsReturnsAuthorizedTargetHistory(t *testing.T) {
	store := NewRunStore()
	seedHistory(t, store, historyTarget, "first")
	seedHistory(t, store, Target{K8sCluster: "main", Namespace: "dbs", Instance: "other"}, "other-instance")
	result := coordinator.Result{Output: "full output", Metrics: []runnerresult.Metric{{Name: "tps", Unit: "tx/s", Value: 12}}}
	if err := store.Complete(context.Background(), "first", RunStatusSucceeded, result, ""); err != nil {
		t.Fatal(err)
	}
	var calls []accessCall
	w := getHistory(historyAPI(store, nil, &calls), historyPath)
	if w.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", w.Code, w.Body.String())
	}
	if len(calls) != 1 || calls[0] != (accessCall{"user-token", "main", "dbs", "postgres-1"}) {
		t.Fatalf("access checks = %+v; want one check of the requested target with the caller's token", calls)
	}
	if strings.Contains(w.Body.String(), "full output") || strings.Contains(w.Body.String(), `"output"`) {
		t.Fatalf("history exposed runner output: %s", w.Body.String())
	}
	var body struct {
		Runs []RunSummaryResponse `json:"runs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Runs) != 1 {
		t.Fatalf("runs = %+v; want only the requested target", body.Runs)
	}
	run := body.Runs[0]
	if run.ID != "first" || run.Status != RunStatusSucceeded || run.Driver != "pgbench" || string(run.Options) != `{"clients":1}` ||
		len(run.Metrics) != 1 || run.CompletedAt == nil {
		t.Fatalf("summary = %+v", run)
	}
}

func TestListRunsAppliesLimit(t *testing.T) {
	store := NewRunStore()
	seedHistory(t, store, historyTarget, "a", "b", "c")
	var calls []accessCall
	handler := historyAPI(store, nil, &calls)
	for _, test := range []struct {
		query string
		want  int
	}{{"", 3}, {"&limit=2", 2}, {"&limit=100", 3}} {
		w := getHistory(handler, historyPath+test.query)
		var body struct {
			Runs []RunSummaryResponse `json:"runs"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusOK || len(body.Runs) != test.want {
			t.Fatalf("%q: status=%d runs=%d err=%v", test.query, w.Code, len(body.Runs), err)
		}
	}
}

func TestListRunsReturnsEmptyArray(t *testing.T) {
	var calls []accessCall
	w := getHistory(historyAPI(NewRunStore(), nil, &calls), historyPath)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"runs":[]}` {
		t.Fatalf("GET = %d %s; want an empty array", w.Code, w.Body.String())
	}
}

func TestListRunsRejectsInvalidQueries(t *testing.T) {
	for _, query := range []string{
		"", "?k8sCluster=main&namespace=dbs", "?k8sCluster=main&namespace=dbs&instance=%20",
		"?k8sCluster=main&k8sCluster=other&namespace=dbs&instance=pg",
		"?k8sCluster=main&namespace=dbs&instance=pg&limit=0", "?k8sCluster=main&namespace=dbs&instance=pg&limit=101",
		"?k8sCluster=main&namespace=dbs&instance=pg&limit=ten", "?k8sCluster=main&namespace=dbs&instance=pg&limit=1&limit=2",
		"?k8sCluster=%zz&namespace=dbs&instance=pg",
	} {
		var calls []accessCall
		w := getHistory(historyAPI(NewRunStore(), nil, &calls), "/api/runs"+query)
		if w.Code != http.StatusBadRequest || len(calls) != 0 {
			t.Fatalf("%q: status=%d accessChecks=%d; want 400 before any access check", query, w.Code, len(calls))
		}
	}
}

func TestListRunsEnforcesAccess(t *testing.T) {
	store := NewRunStore()
	seedHistory(t, store, historyTarget, "secret-run")
	for _, test := range []struct {
		err    error
		status int
	}{
		{everest.ErrForbidden, http.StatusForbidden},
		{everest.ErrNotFound, http.StatusNotFound},
		{everest.ErrUnauthorized, http.StatusUnauthorized},
		{errors.New("upstream down"), http.StatusBadGateway},
	} {
		var calls []accessCall
		w := getHistory(historyAPI(store, test.err, &calls), historyPath)
		if w.Code != test.status || strings.Contains(w.Body.String(), "secret-run") {
			t.Fatalf("access error %v: status=%d body=%s", test.err, w.Code, w.Body.String())
		}
	}

	var calls []accessCall
	r := httptest.NewRequest(http.MethodGet, historyPath, nil)
	w := httptest.NewRecorder()
	historyAPI(store, nil, &calls).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || len(calls) != 0 {
		t.Fatalf("unauthenticated GET = %d with %d access checks", w.Code, len(calls))
	}
}

type failingListStore struct{ *MemoryRunStore }

func (failingListStore) ListByTarget(context.Context, Target, int) ([]Run, error) {
	return nil, errors.New("storage-detail-sentinel")
}

func TestListRunsHidesStoreErrors(t *testing.T) {
	var calls []accessCall
	w := getHistory(historyAPI(failingListStore{NewRunStore()}, nil, &calls), historyPath)
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "storage-detail-sentinel") {
		t.Fatalf("GET = %d %s; want a generic 500", w.Code, w.Body.String())
	}
}
