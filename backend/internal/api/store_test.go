package api

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
)

func TestRunStoreLifecycle(t *testing.T) {
	for _, status := range []RunStatus{RunStatusSucceeded, RunStatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			s := NewRunStore()
			request := CreateRunRequest{Database: "benchmark"}
			created, err := s.Create("run-1", request)
			if err != nil || created.Status != RunStatusRunning || created.CreatedAt.IsZero() {
				t.Fatalf("Create = %+v, %v", created, err)
			}
			created.Request.Database = "changed"
			if _, err := s.Create("run-1", CreateRunRequest{}); !errors.Is(err, ErrRunExists) {
				t.Fatalf("duplicate Create: %v", err)
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
			if err != nil || got.Request != request || got.Status != status || got.Output != result.Output || got.JobName != result.JobName || !got.OutputTruncated || got.Error != message || got.CompletedAt == nil {
				t.Fatalf("Get = %+v, %v", got, err)
			}
			*got.CompletedAt = time.Time{}
			again, _ := s.Get("run-1")
			if again.CompletedAt.IsZero() {
				t.Fatal("Get exposed stored timestamp pointer")
			}
			if err := s.Complete("run-1", RunStatusSucceeded, coordinator.Result{}, ""); !errors.Is(err, ErrRunAlreadyCompleted) {
				t.Fatalf("repeated Complete: %v", err)
			}
		})
	}
}

func TestRunStoreRejectsInvalidOperations(t *testing.T) {
	s := NewRunStore()
	if _, err := s.Create(" ", CreateRunRequest{}); err == nil {
		t.Fatal("accepted empty ID")
	}
	if _, err := s.Get("missing"); !errors.Is(err, ErrRunNotFound) {
		t.Fatal(err)
	}
	if err := s.Complete("missing", RunStatusSucceeded, coordinator.Result{}, ""); !errors.Is(err, ErrRunNotFound) {
		t.Fatal(err)
	}
	if _, err := s.Create("run", CreateRunRequest{}); err != nil {
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

func TestRunStoreConcurrentCompletion(t *testing.T) {
	s := NewRunStore()
	if _, err := s.Create("run", CreateRunRequest{}); err != nil {
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
