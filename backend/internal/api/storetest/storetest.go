// Package storetest verifies that api.RunStore implementations behave
// identically, so the API can switch storage without changing semantics.
package storetest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/openeverest/plugin-bench/backend/internal/api"
	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	"github.com/openeverest/plugin-bench/backend/internal/runnerresult"
	corev1 "k8s.io/api/core/v1"
)

// NewStore returns an empty store owned by one subtest.
type NewStore func(t *testing.T) api.RunStore

var ctx = context.Background()

func Run(t *testing.T, newStore NewStore) {
	t.Run("lifecycle round-trips every field", func(t *testing.T) { testLifecycle(t, newStore(t)) })
	t.Run("returned records are copies", func(t *testing.T) { testCopies(t, newStore(t)) })
	t.Run("rejects invalid operations", func(t *testing.T) { testInvalidOperations(t, newStore(t)) })
	t.Run("completes exactly once under concurrency", func(t *testing.T) { testConcurrentCompletion(t, newStore(t)) })
	t.Run("stores output that is not valid UTF-8", func(t *testing.T) { testUnsafeOutput(t, newStore(t)) })
	t.Run("lists one target newest first", func(t *testing.T) { testListByTarget(t, newStore(t)) })
	t.Run("fails only running records", func(t *testing.T) { testFailRunning(t, newStore(t)) })
}

func target(instance string) api.Target {
	return api.Target{K8sCluster: "main", Namespace: "databases", Instance: instance}
}

func snapshot() api.RunSnapshot {
	// Key order and spacing differ from Go's encoding to catch normalization.
	return api.RunSnapshot{
		DatabaseType: "postgresql",
		Driver:       "pgbench",
		RunnerImage:  "ghcr.io/openeverest/plugin-bench-pgbench:test",
		Database:     "bench",
		Options:      []byte(`{"durationSeconds":30, "clients":4,"threads":2,"scale":1,"initialize":true}`),
	}
}

func nodeAffinity() *corev1.NodeAffinity {
	return &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
			{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "workload", Operator: corev1.NodeSelectorOpIn, Values: []string{"benchmark"}}}},
		}},
		PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{{Weight: 75, Preference: corev1.NodeSelectorTerm{
			MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "disk-type", Operator: corev1.NodeSelectorOpIn, Values: []string{"ssd"}}},
		}}},
	}
}

var resources = coordinator.Resources{CPURequest: "500m", MemoryRequest: "128Mi", CPULimit: "1", MemoryLimit: "512Mi"}

func mustCreate(t *testing.T, store api.RunStore, id string, target api.Target) api.Run {
	t.Helper()
	run, err := store.Create(ctx, id, target, snapshot(), resources, nil)
	if err != nil {
		t.Fatalf("Create(%q): %v", id, err)
	}
	return run
}

func testLifecycle(t *testing.T, store api.RunStore) {
	before := time.Now().Add(-time.Second)
	created, err := store.Create(ctx, "run-1", target("pg"), snapshot(), resources, nodeAffinity())
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "run-1" || created.Status != api.RunStatusRunning || created.CompletedAt != nil || created.CreatedAt.Before(before) {
		t.Fatalf("Create = %+v", created)
	}
	if _, err := store.Create(ctx, "run-1", api.Target{}, api.RunSnapshot{}, coordinator.Resources{}, nil); !errors.Is(err, api.ErrRunExists) {
		t.Fatalf("duplicate Create error = %v, want %v", err, api.ErrRunExists)
	}

	running, err := store.Get(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if running.Target != target("pg") || !reflect.DeepEqual(running.Snapshot, snapshot()) || running.Resources != resources ||
		!reflect.DeepEqual(&running.NodeAffinity, nodeAffinity()) || !running.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("running record = %+v", running)
	}

	result := coordinator.Result{
		JobName: "plugin-bench-abc", Output: "tps = 1234.5\n", OutputTruncated: true,
		Metrics: []runnerresult.Metric{{Name: "tps", Unit: "tx/s", Value: 1234.5}, {Name: "latency_avg", Unit: "ms", Value: 0}},
	}
	if err := store.Complete(ctx, "run-1", api.RunStatusFailed, result, "benchmark execution failed"); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, "run-1", api.RunStatusSucceeded, coordinator.Result{}, ""); !errors.Is(err, api.ErrRunAlreadyCompleted) {
		t.Fatalf("second Complete error = %v, want %v", err, api.ErrRunAlreadyCompleted)
	}
	got, err := store.Get(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != api.RunStatusFailed || got.Error != "benchmark execution failed" || got.JobName != result.JobName ||
		got.Output != result.Output || !got.OutputTruncated || !reflect.DeepEqual(got.Metrics, result.Metrics) ||
		got.CompletedAt == nil || got.CompletedAt.Before(got.CreatedAt) || !reflect.DeepEqual(got.Snapshot, snapshot()) {
		t.Fatalf("completed record = %+v", got)
	}

	if err := store.Complete(ctx, mustCreate(t, store, "run-2", target("pg")).ID, api.RunStatusSucceeded, coordinator.Result{}, ""); err != nil {
		t.Fatal(err)
	}
	empty, err := store.Get(ctx, "run-2")
	if err != nil || empty.Metrics != nil || !reflect.DeepEqual(empty.NodeAffinity, corev1.NodeAffinity{}) {
		t.Fatalf("run without metrics or affinity = %+v, %v", empty, err)
	}
}

func testCopies(t *testing.T, store api.RunStore) {
	affinity := nodeAffinity()
	created, err := store.Create(ctx, "run", target("pg"), snapshot(), resources, affinity)
	if err != nil {
		t.Fatal(err)
	}
	affinity.PreferredDuringSchedulingIgnoredDuringExecution[0].Weight = 1
	created.Snapshot.Options[0] = 'X'
	metrics := []runnerresult.Metric{{Name: "tps", Unit: "tx/s", Value: 1}}
	if err := store.Complete(ctx, "run", api.RunStatusSucceeded, coordinator.Result{Metrics: metrics}, ""); err != nil {
		t.Fatal(err)
	}
	metrics[0].Value = 2
	got, _ := store.Get(ctx, "run")
	got.Snapshot.Options[0] = 'Y'
	got.Metrics[0].Value = 3
	*got.CompletedAt = time.Time{}
	got.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].Weight = 2

	again, err := store.Get(ctx, "run")
	if err != nil || !reflect.DeepEqual(again.Snapshot, snapshot()) || again.Metrics[0].Value != 1 ||
		again.CompletedAt.IsZero() || !reflect.DeepEqual(&again.NodeAffinity, nodeAffinity()) {
		t.Fatalf("stored record changed through a caller's copy: %+v, %v", again, err)
	}
}

func testInvalidOperations(t *testing.T, store api.RunStore) {
	if _, err := store.Create(ctx, " ", api.Target{}, api.RunSnapshot{}, coordinator.Resources{}, nil); err == nil {
		t.Fatal("accepted a blank run ID")
	}
	if _, err := store.Get(ctx, "missing"); !errors.Is(err, api.ErrRunNotFound) {
		t.Fatalf("Get missing error = %v", err)
	}
	if err := store.Complete(ctx, "missing", api.RunStatusSucceeded, coordinator.Result{}, ""); !errors.Is(err, api.ErrRunNotFound) {
		t.Fatalf("Complete missing error = %v", err)
	}
	mustCreate(t, store, "run", target("pg"))
	for _, test := range []struct {
		status  api.RunStatus
		message string
	}{{api.RunStatusRunning, ""}, {"unknown", ""}, {api.RunStatusSucceeded, "error"}, {api.RunStatusFailed, " "}} {
		if err := store.Complete(ctx, "run", test.status, coordinator.Result{}, test.message); err == nil {
			t.Fatalf("accepted completion %+v", test)
		}
	}
	got, err := store.Get(ctx, "run")
	if err != nil || got.Status != api.RunStatusRunning || got.CompletedAt != nil {
		t.Fatalf("invalid completion changed the record: %+v, %v", got, err)
	}
	for _, limit := range []int{0, -1, api.MaxRunListLimit + 1} {
		if _, err := store.ListByTarget(ctx, target("pg"), limit); err == nil {
			t.Fatalf("accepted list limit %d", limit)
		}
	}
	if _, err := store.FailRunning(ctx, " "); err == nil {
		t.Fatal("accepted a blank failure message")
	}
}

func testConcurrentCompletion(t *testing.T, store api.RunStore) {
	mustCreate(t, store, "run", target("pg"))
	var wg sync.WaitGroup
	outcomes := make(chan error, 8)
	for range cap(outcomes) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outcomes <- store.Complete(ctx, "run", api.RunStatusSucceeded, coordinator.Result{}, "")
		}()
	}
	wg.Wait()
	close(outcomes)
	successes := 0
	for err := range outcomes {
		if err == nil {
			successes++
		} else if !errors.Is(err, api.ErrRunAlreadyCompleted) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful completions = %d, want 1", successes)
	}
}

// Logs are truncated at a byte limit and can split a multibyte character or
// contain NUL bytes; storage must keep the rest of the output readable.
func testUnsafeOutput(t *testing.T, store api.RunStore) {
	mustCreate(t, store, "run", target("pg"))
	output := "tps = 1\x00\nlatency \xe2\x82"
	if err := store.Complete(ctx, "run", api.RunStatusFailed, coordinator.Result{Output: output}, "failed \xff"); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, "run")
	if err != nil || !strings.HasPrefix(got.Output, "tps = 1") || !strings.Contains(got.Output, "latency") || !strings.HasPrefix(got.Error, "failed") {
		t.Fatalf("output = %q, error = %q, err = %v", got.Output, got.Error, err)
	}
	// Memory keeps bytes verbatim; persistent stores may sanitize them, but
	// sanitized text must then be valid UTF-8 without NUL bytes.
	if got.Output != output && (!utf8.ValidString(got.Output) || strings.ContainsRune(got.Output, 0)) {
		t.Fatalf("sanitized output is still unsafe: %q", got.Output)
	}
}

func testListByTarget(t *testing.T, store api.RunStore) {
	empty, err := store.ListByTarget(ctx, target("pg"), 10)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty list = %#v, %v; want non-nil empty", empty, err)
	}
	for i := range 4 {
		mustCreate(t, store, fmt.Sprintf("pg-%d", i), target("pg"))
		time.Sleep(2 * time.Millisecond)
	}
	mustCreate(t, store, "other-instance", target("other"))
	mustCreate(t, store, "other-namespace", api.Target{K8sCluster: "main", Namespace: "elsewhere", Instance: "pg"})
	pg1Metrics := []runnerresult.Metric{{Name: "tps", Unit: "tx/s", Value: 100}, {Name: "latency_avg", Unit: "ms", Value: 2.5}}
	if err := store.Complete(ctx, "pg-1", api.RunStatusSucceeded, coordinator.Result{Output: "large output", Metrics: pg1Metrics}, ""); err != nil {
		t.Fatal(err)
	}
	pg2Metrics := []runnerresult.Metric{{Name: "ops", Unit: "op/s", Value: 7}}
	if err := store.Complete(ctx, "pg-2", api.RunStatusFailed, coordinator.Result{Metrics: pg2Metrics}, "failed"); err != nil {
		t.Fatal(err)
	}

	got, err := store.ListByTarget(ctx, target("pg"), 3)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(got))
	for i, run := range got {
		ids[i] = run.ID
	}
	if !reflect.DeepEqual(ids, []string{"pg-3", "pg-2", "pg-1"}) {
		t.Fatalf("listed IDs = %v, want newest three for the target", ids)
	}
	completed := got[2]
	if completed.Status != api.RunStatusSucceeded || completed.Output != "" || completed.CompletedAt == nil ||
		!reflect.DeepEqual(completed.Snapshot, snapshot()) || completed.Resources != resources {
		t.Fatalf("listed record = %+v; want metadata without output", completed)
	}
	if got[0].Metrics != nil || !reflect.DeepEqual(got[1].Metrics, pg2Metrics) || !reflect.DeepEqual(got[2].Metrics, pg1Metrics) {
		t.Fatalf("listed metrics = %v, %v, %v; want each run's own metrics in reported order", got[0].Metrics, got[1].Metrics, got[2].Metrics)
	}
}

func testFailRunning(t *testing.T, store api.RunStore) {
	mustCreate(t, store, "b-running", target("pg"))
	mustCreate(t, store, "a-running", target("other"))
	mustCreate(t, store, "done", target("pg"))
	if err := store.Complete(ctx, "done", api.RunStatusSucceeded, coordinator.Result{Output: "kept"}, ""); err != nil {
		t.Fatal(err)
	}
	ids, err := store.FailRunning(ctx, "interrupted by restart")
	if err != nil || !reflect.DeepEqual(ids, []string{"a-running", "b-running"}) {
		t.Fatalf("FailRunning = %v, %v", ids, err)
	}
	for _, id := range ids {
		run, err := store.Get(ctx, id)
		if err != nil || run.Status != api.RunStatusFailed || run.Error != "interrupted by restart" || run.CompletedAt == nil {
			t.Fatalf("interrupted run %q = %+v, %v", id, run, err)
		}
	}
	done, err := store.Get(ctx, "done")
	if err != nil || done.Status != api.RunStatusSucceeded || done.Output != "kept" || done.Error != "" {
		t.Fatalf("completed run changed: %+v, %v", done, err)
	}
	again, err := store.FailRunning(ctx, "interrupted by restart")
	if err != nil || again == nil || len(again) != 0 {
		t.Fatalf("second FailRunning = %#v, %v; want non-nil empty", again, err)
	}
}
