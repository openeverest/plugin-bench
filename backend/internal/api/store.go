package api

import (
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	corev1 "k8s.io/api/core/v1"
)

const completedRunRetention = time.Hour

var (
	ErrRunNotFound         = errors.New("run not found")
	ErrRunExists           = errors.New("run ID already exists")
	ErrRunAlreadyCompleted = errors.New("run is already completed")
)

type RunStore struct {
	mu   sync.RWMutex
	runs map[string]Run
	now  func() time.Time
}

// Create stores independent copies of the request and prepared affinity snapshot.
// A nil prepared affinity is retained as an empty object in status responses.
func (s *RunStore) Create(id string, request CreateRunRequest, nodeAffinity *corev1.NodeAffinity) (Run, error) {
	if strings.TrimSpace(id) == "" {
		return Run{}, errors.New("run ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	s.pruneExpiredLocked(now)
	if _, exists := s.runs[id]; exists {
		return Run{}, ErrRunExists
	}
	request.NodeAffinity = request.NodeAffinity.DeepCopy()
	run := Run{ID: id, Request: request, Status: RunStatusRunning, CreatedAt: now.UTC()}
	if nodeAffinity != nil {
		run.NodeAffinity = *nodeAffinity.DeepCopy()
	}
	if s.runs == nil {
		s.runs = make(map[string]Run)
	}
	s.runs[id] = run
	return copyRun(run), nil
}

// Get returns a copy so callers cannot change the stored record without locking.
func (s *RunStore) Get(id string) (Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, exists := s.runs[id]
	if !exists {
		return Run{}, ErrRunNotFound
	}
	if isExpired(run, s.clock()) {
		return Run{}, ErrRunNotFound
	}
	return copyRun(run), nil
}

func copyRun(run Run) Run {
	run.Request.NodeAffinity = run.Request.NodeAffinity.DeepCopy()
	run.NodeAffinity = *run.NodeAffinity.DeepCopy()
	if run.CompletedAt != nil {
		completedAt := *run.CompletedAt
		run.CompletedAt = &completedAt
	}
	return run
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
	now := s.clock()
	run, exists := s.runs[id]
	if !exists {
		return ErrRunNotFound
	}
	if run.Status != RunStatusRunning {
		return ErrRunAlreadyCompleted
	}
	completedAt := now.UTC()
	run.Status = status
	run.CompletedAt = &completedAt
	run.JobName = result.JobName
	run.Output = result.Output
	run.OutputTruncated = result.OutputTruncated
	run.Error = message
	s.runs[id] = run
	return nil
}

// isExpired reports whether a completed run is past its retention window.
// Only Complete sets CompletedAt, so running records never expire.
func isExpired(run Run, now time.Time) bool {
	return run.CompletedAt != nil && now.Sub(*run.CompletedAt) >= completedRunRetention
}

// pruneExpiredLocked removes terminal runs whose retention window has elapsed.
// The caller must hold s.mu for writing.
func (s *RunStore) pruneExpiredLocked(now time.Time) {
	for id, run := range s.runs {
		if isExpired(run, now) {
			delete(s.runs, id)
		}
	}
}

func NewRunStore() *RunStore {
	return &RunStore{
		runs: make(map[string]Run),
		now:  time.Now,
	}
}

// clock keeps the zero-value RunStore usable; tests can override now.
func (s *RunStore) clock() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}
