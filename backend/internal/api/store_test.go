package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
)

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

			if _, err := s.Create(context.Background(), "completed", Target{}, RunSnapshot{}, coordinator.Resources{}, nil); err != nil {
				t.Fatal(err)
			}
			if err := s.Complete(context.Background(), "completed", test.status, coordinator.Result{}, test.message); err != nil {
				t.Fatal(err)
			}

			now = now.Add(completedRunRetention + time.Second)
			if _, err := s.Get(context.Background(), "completed"); !errors.Is(err, ErrRunNotFound) {
				t.Fatalf("Get expired run error = %v, want %v", err, ErrRunNotFound)
			}
			if _, err := s.Create(context.Background(), "completed", Target{}, RunSnapshot{}, coordinator.Resources{}, nil); err != nil {
				t.Fatalf("Create should prune the expired run before reusing its ID: %v", err)
			}
		})
	}
}

func TestRunStoreRetainsRunningRunsPastRetention(t *testing.T) {
	now := time.Date(2026, time.September, 29, 12, 0, 0, 0, time.UTC)
	s := NewRunStore()
	s.now = func() time.Time { return now }

	if _, err := s.Create(context.Background(), "running", Target{}, RunSnapshot{}, coordinator.Resources{}, nil); err != nil {
		t.Fatal(err)
	}
	now = now.Add(completedRunRetention + time.Second)
	if _, err := s.Create(context.Background(), "another", Target{}, RunSnapshot{}, coordinator.Resources{}, nil); err != nil {
		t.Fatal(err)
	}

	run, err := s.Get(context.Background(), "running")
	if err != nil {
		t.Fatalf("Get running run after retention window: %v", err)
	}
	if run.Status != RunStatusRunning {
		t.Fatalf("running run status = %q, want %q", run.Status, RunStatusRunning)
	}
}

func TestMemoryRunStoreListOmitsExpiredRuns(t *testing.T) {
	now := time.Date(2026, time.September, 29, 12, 0, 0, 0, time.UTC)
	s := NewRunStore()
	s.now = func() time.Time { return now }
	target := Target{K8sCluster: "main", Namespace: "db", Instance: "pg"}
	for _, id := range []string{"expired", "running"} {
		if _, err := s.Create(context.Background(), id, target, RunSnapshot{}, coordinator.Resources{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Complete(context.Background(), "expired", RunStatusSucceeded, coordinator.Result{}, ""); err != nil {
		t.Fatal(err)
	}
	now = now.Add(completedRunRetention)
	runs, err := s.ListByTarget(context.Background(), target, 10)
	if err != nil || len(runs) != 1 || runs[0].ID != "running" {
		t.Fatalf("ListByTarget = %+v, %v; want only the unexpired running record", runs, err)
	}
}
