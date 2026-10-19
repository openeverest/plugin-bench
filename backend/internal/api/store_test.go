package api

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	"github.com/openeverest/plugin-bench/backend/internal/runnerresult"
)

func TestRunStoreOwnsCompletedMetrics(t *testing.T) {
	for _, status := range []RunStatus{RunStatusSucceeded, RunStatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			store := NewRunStore()
			if _, err := store.Create("run", Target{}, RunSnapshot{}, coordinator.Resources{}, nil); err != nil {
				t.Fatal(err)
			}
			metrics := []runnerresult.Metric{{Name: "tps", Unit: "tx/s", Value: 5432.10}}
			message := ""
			if status == RunStatusFailed {
				message = "benchmark failed"
			}
			if err := store.Complete("run", status, coordinator.Result{Metrics: metrics}, message); err != nil {
				t.Fatal(err)
			}
			metrics[0] = runnerresult.Metric{Name: "changed", Unit: "changed", Value: -1}
			got, err := store.Get("run")
			want := []runnerresult.Metric{{Name: "tps", Unit: "tx/s", Value: 5432.10}}
			if err != nil || !reflect.DeepEqual(got.Metrics, want) {
				t.Fatal("completion retained the caller's metrics slice")
			}
			got.Metrics[0] = metrics[0]
			again, err := store.Get("run")
			if err != nil || !reflect.DeepEqual(again.Metrics, want) {
				t.Fatal("retrieval exposed the stored metrics slice")
			}
		})
	}
}

func TestRunStoreLifecycle(t *testing.T) {
	for _, status := range []RunStatus{RunStatusSucceeded, RunStatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			s := NewRunStore()
			target := Target{K8sCluster: "main", Namespace: "db", Instance: "instance"}
			snapshot := RunSnapshot{Driver: "pgbench", RunnerImage: testRunnerImage, Database: "benchmark", Options: []byte(`{"clients":4}`)}
			expected := snapshot
			expected.Options = append([]byte(nil), snapshot.Options...)
			resolved := coordinator.Resources{CPURequest: "500m", CPULimit: "1", MemoryRequest: "128Mi", MemoryLimit: "512Mi"}
			created, err := s.Create("run-1", target, snapshot, resolved, nil)
			if err != nil || created.Status != RunStatusRunning || created.CreatedAt.IsZero() {
				t.Fatalf("Create = %+v, %v", created, err)
			}
			snapshot.Options[0] = 'X'
			created.Snapshot.Options[0] = 'Y'
			created.Snapshot.Database = "changed"
			created.Target.Instance = "changed"
			created.Resources.CPURequest = "changed"
			if _, err := s.Create("run-1", Target{}, RunSnapshot{}, coordinator.Resources{}, nil); !errors.Is(err, ErrRunExists) {
				t.Fatal(err)
			}
			message := ""
			if status == RunStatusFailed {
				message = "benchmark failed"
			}
			result := coordinator.Result{JobName: "job-1", Output: "output", OutputTruncated: true}
			if err := s.Complete("run-1", status, result, message); err != nil {
				t.Fatal(err)
			}
			got, err := s.Get("run-1")
			if err != nil || got.Target != target || !reflect.DeepEqual(got.Snapshot, expected) || got.Resources != resolved ||
				got.Status != status || got.Output != result.Output || got.JobName != result.JobName || !got.OutputTruncated || got.Error != message || got.CompletedAt == nil {
				t.Fatalf("Get = %+v, %v", got, err)
			}
			got.Resources.MemoryLimit = "changed"
			got.Snapshot.Options[0] = 'Z'
			*got.CompletedAt = time.Time{}
			again, _ := s.Get("run-1")
			if again.CompletedAt.IsZero() || again.Resources != resolved || !reflect.DeepEqual(again.Snapshot, expected) {
				t.Fatal("Get exposed stored values")
			}
			if err := s.Complete("run-1", RunStatusSucceeded, coordinator.Result{}, ""); !errors.Is(err, ErrRunAlreadyCompleted) {
				t.Fatal(err)
			}
		})
	}
}

func TestRunStoreRejectsInvalidOperations(t *testing.T) {
	s := NewRunStore()
	if _, err := s.Create(" ", Target{}, RunSnapshot{}, coordinator.Resources{}, nil); err == nil {
		t.Fatal("accepted empty ID")
	}
	if _, err := s.Get("missing"); !errors.Is(err, ErrRunNotFound) {
		t.Fatal(err)
	}
	if err := s.Complete("missing", RunStatusSucceeded, coordinator.Result{}, ""); !errors.Is(err, ErrRunNotFound) {
		t.Fatal(err)
	}
	if _, err := s.Create("run", Target{}, RunSnapshot{}, coordinator.Resources{}, nil); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		status  RunStatus
		message string
	}{
		{RunStatusRunning, ""}, {RunStatus("unknown"), ""}, {RunStatusSucceeded, "error"}, {RunStatusFailed, " "},
	} {
		if err := s.Complete("run", test.status, coordinator.Result{}, test.message); err == nil {
			t.Fatal("accepted invalid completion")
		}
	}
	got, _ := s.Get("run")
	if got.Status != RunStatusRunning || got.CompletedAt != nil {
		t.Fatal("invalid completion changed run")
	}
}

func TestRunStoreOwnsPreparedNodeAffinitySnapshot(t *testing.T) {
	for _, status := range []RunStatus{RunStatusSucceeded, RunStatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			store := NewRunStore()
			submitted := testNodeAffinity()
			prepared := submitted.DeepCopy()
			prepared.PreferredDuringSchedulingIgnoredDuringExecution[0].Weight = 50
			wantSnapshot := prepared.DeepCopy()
			snapshot := RunSnapshot{Database: "benchmark"}
			created, err := store.Create("run-1", Target{}, snapshot, coordinator.Resources{}, prepared)
			if err != nil {
				t.Fatal(err)
			}
			if created.Snapshot.Database != snapshot.Database {
				t.Fatal("database snapshot was not retained")
			}
			mutateTestNodeAffinity(submitted)
			mutateTestNodeAffinity(prepared)
			mutateTestNodeAffinity(&created.NodeAffinity)
			got, err := store.Get("run-1")
			if err != nil || !reflect.DeepEqual(&got.NodeAffinity, wantSnapshot) {
				t.Fatalf("Create exposed its stored pointers: run=%+v err=%v", got, err)
			}
			mutateTestNodeAffinity(&got.NodeAffinity)
			message := ""
			if status == RunStatusFailed {
				message = "benchmark failed"
			}
			if err := store.Complete("run-1", status, coordinator.Result{}, message); err != nil {
				t.Fatal(err)
			}
			completed, err := store.Get("run-1")
			if err != nil || completed.Status != status || !reflect.DeepEqual(&completed.NodeAffinity, wantSnapshot) {
				t.Fatalf("Get mutations or completion changed history: run=%+v err=%v", completed, err)
			}
		})
	}
}

func TestRunStoreConcurrentCompletion(t *testing.T) {
	s := NewRunStore()
	if _, err := s.Create("run", Target{}, RunSnapshot{}, coordinator.Resources{}, nil); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	outcomes := make(chan error, 10)
	for i := 0; i < cap(outcomes); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.Get("run")
			outcomes <- s.Complete("run", RunStatusSucceeded, coordinator.Result{}, "")
		}()
	}
	wg.Wait()
	close(outcomes)
	successes := 0
	for err := range outcomes {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrRunAlreadyCompleted) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful completions = %d, want 1", successes)
	}
}

func TestRunStoreExpiresCompletedRuns(t *testing.T) {
	for _, test := range []struct {
		status  RunStatus
		message string
	}{
		{status: RunStatusSucceeded},
		{status: RunStatusFailed, message: "benchmark failed"},
	} {
		t.Run(string(test.status), func(t *testing.T) {
			now := time.Date(2026, time.September, 29, 12, 0, 0, 0, time.UTC)
			s := NewRunStore()
			s.now = func() time.Time { return now }

			if _, err := s.Create("completed", Target{}, RunSnapshot{}, coordinator.Resources{}, nil); err != nil {
				t.Fatal(err)
			}
			if err := s.Complete("completed", test.status, coordinator.Result{}, test.message); err != nil {
				t.Fatal(err)
			}

			now = now.Add(completedRunRetention + time.Second)
			if _, err := s.Get("completed"); !errors.Is(err, ErrRunNotFound) {
				t.Fatalf("Get expired run error = %v, want %v", err, ErrRunNotFound)
			}
			if _, err := s.Create("completed", Target{}, RunSnapshot{}, coordinator.Resources{}, nil); err != nil {
				t.Fatalf("Create should prune the expired run before reusing its ID: %v", err)
			}
		})
	}
}

func TestRunStoreRetainsRunningRunsPastRetention(t *testing.T) {
	now := time.Date(2026, time.September, 29, 12, 0, 0, 0, time.UTC)
	s := NewRunStore()
	s.now = func() time.Time { return now }

	if _, err := s.Create("running", Target{}, RunSnapshot{}, coordinator.Resources{}, nil); err != nil {
		t.Fatal(err)
	}
	now = now.Add(completedRunRetention + time.Second)
	if _, err := s.Create("another", Target{}, RunSnapshot{}, coordinator.Resources{}, nil); err != nil {
		t.Fatal(err)
	}

	run, err := s.Get("running")
	if err != nil {
		t.Fatalf("Get running run after retention window: %v", err)
	}
	if run.Status != RunStatusRunning {
		t.Fatalf("running run status = %q, want %q", run.Status, RunStatusRunning)
	}
}
