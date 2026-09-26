package api

import (
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
)

var (
	ErrRunNotFound         = errors.New("run not found")
	ErrRunExists           = errors.New("run ID already exists")
	ErrRunAlreadyCompleted = errors.New("run is already completed")
)

type RunStore struct {
	mu   sync.RWMutex
	runs map[string]Run
}

// Create stores a new running record.
func (s *RunStore) Create(id string, request CreateRunRequest) (Run, error) {
	if strings.TrimSpace(id) == "" {
		return Run{}, errors.New("run ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.runs[id]; exists {
		return Run{}, ErrRunExists
	}
	run := Run{ID: id, Request: request, Status: RunStatusRunning, CreatedAt: time.Now().UTC()}
	if s.runs == nil {
		s.runs = make(map[string]Run)
	}
	s.runs[id] = run
	return run, nil
}

// Get returns a copy so callers cannot change the stored record without locking.
func (s *RunStore) Get(id string) (Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, exists := s.runs[id]
	if !exists {
		return Run{}, ErrRunNotFound
	}
	if run.CompletedAt != nil {
		completedAt := *run.CompletedAt
		run.CompletedAt = &completedAt
	}
	return run, nil
}

func (s *RunStore) Complete(id string, status RunStatus, result coordinator.Result, message string) error {
	if status != RunStatusSucceeded && status != RunStatusFailed {
		return errors.New("completion status must be succeeded or failed")
	}
	if status == RunStatusSucceeded && message != "" {
		return errors.New("successful run cannot have an error message")
	}
	if status == RunStatusFailed && strings.TrimSpace(message) == "" {
		return errors.New("failed run requires an error message")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run, exists := s.runs[id]
	if !exists {
		return ErrRunNotFound
	}
	if run.Status != RunStatusRunning {
		return ErrRunAlreadyCompleted
	}
	completedAt := time.Now().UTC()
	run.Status = status
	run.CompletedAt = &completedAt
	run.JobName = result.JobName
	run.Output = result.Output
	run.OutputTruncated = result.OutputTruncated
	run.Error = message
	s.runs[id] = run
	return nil
}

func NewRunStore() *RunStore {
	return &RunStore{
		runs: make(map[string]Run),
	}
}
