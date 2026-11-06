package api

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	corev1 "k8s.io/api/core/v1"
)

const completedRunRetention = time.Hour

// MaxRunListLimit bounds one history page so a single request cannot load an
// unbounded number of records.
const MaxRunListLimit = 100

var (
	ErrRunNotFound         = errors.New("run not found")
	ErrRunExists           = errors.New("run ID already exists")
	ErrRunAlreadyCompleted = errors.New("run is already completed")
)

// RunStore persists benchmark run records. Implementations must be safe for
// concurrent use and return copies that callers may freely mutate.
type RunStore interface {
	// Create stores a new running record with safe effective settings only.
	// A nil prepared affinity is retained as an empty object.
	Create(ctx context.Context, id string, target Target, snapshot RunSnapshot, resources coordinator.Resources, nodeAffinity *corev1.NodeAffinity) (Run, error)
	Get(ctx context.Context, id string) (Run, error)
	// Complete records a terminal result exactly once.
	Complete(ctx context.Context, id string, status RunStatus, result coordinator.Result, message string) error
	// ListByTarget returns up to limit runs for one target, newest first.
	// Records omit runner output, which can be large; use Get for details.
	ListByTarget(ctx context.Context, target Target, limit int) ([]Run, error)
	// FailRunning marks every running record failed with message and returns
	// their IDs. Startup uses it for runs whose executor no longer exists.
	FailRunning(ctx context.Context, message string) ([]string, error)
}

// ValidateRunID rejects IDs that cannot identify a stored run.
func ValidateRunID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("run ID is required")
	}
	return nil
}

// ValidateCompletion enforces the terminal status contract shared by stores.
func ValidateCompletion(status RunStatus, message string) error {
	if status != RunStatusSucceeded && status != RunStatusFailed {
		return errors.New("completion status must be succeeded or failed")
	}
	if status == RunStatusSucceeded && message != "" {
		return errors.New("successful run cannot have an error message")
	}
	if status == RunStatusFailed && strings.TrimSpace(message) == "" {
		return errors.New("failed run requires an error message")
	}
	return nil
}

// ValidateListLimit keeps history pages within MaxRunListLimit.
func ValidateListLimit(limit int) error {
	if limit < 1 || limit > MaxRunListLimit {
		return errors.New("run list limit is out of range")
	}
	return nil
}

// SortRunsNewestFirst orders history deterministically when timestamps tie.
func SortRunsNewestFirst(runs []Run) {
	slices.SortFunc(runs, func(a, b Run) int {
		if n := b.CreatedAt.Compare(a.CreatedAt); n != 0 {
			return n
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

// MemoryRunStore keeps records in process memory. Records are lost on restart
// and completed records expire after completedRunRetention.
type MemoryRunStore struct {
	mu   sync.RWMutex
	runs map[string]Run
	now  func() time.Time
}

func NewRunStore() *MemoryRunStore {
	return &MemoryRunStore{
		runs: make(map[string]Run),
		now:  time.Now,
	}
}

func (s *MemoryRunStore) Create(_ context.Context, id string, target Target, snapshot RunSnapshot, resources coordinator.Resources, nodeAffinity *corev1.NodeAffinity) (Run, error) {
	if err := ValidateRunID(id); err != nil {
		return Run{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	s.pruneExpiredLocked(now)
	if _, exists := s.runs[id]; exists {
		return Run{}, ErrRunExists
	}
	snapshot.Options = slices.Clone(snapshot.Options)
	run := Run{ID: id, Target: target, Snapshot: snapshot, Resources: resources, Status: RunStatusRunning, CreatedAt: now.UTC()}
	if nodeAffinity != nil {
		run.NodeAffinity = *nodeAffinity.DeepCopy()
	}
	if s.runs == nil {
		s.runs = make(map[string]Run)
	}
	s.runs[id] = run
	return copyRun(run), nil
}

func (s *MemoryRunStore) Get(_ context.Context, id string) (Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, exists := s.runs[id]
	if !exists || isExpired(run, s.clock()) {
		return Run{}, ErrRunNotFound
	}
	return copyRun(run), nil
}

func (s *MemoryRunStore) Complete(_ context.Context, id string, status RunStatus, result coordinator.Result, message string) error {
	if err := ValidateCompletion(status, message); err != nil {
		return err
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
	s.runs[id] = completeRun(run, s.clock(), status, result, message)
	return nil
}

func (s *MemoryRunStore) ListByTarget(_ context.Context, target Target, limit int) ([]Run, error) {
	if err := ValidateListLimit(limit); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.clock()
	runs := make([]Run, 0)
	for _, run := range s.runs {
		if run.Target == target && !isExpired(run, now) {
			run = copyRun(run)
			run.Output = ""
			runs = append(runs, run)
		}
	}
	SortRunsNewestFirst(runs)
	if len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}

func (s *MemoryRunStore) FailRunning(_ context.Context, message string) ([]string, error) {
	if err := ValidateCompletion(RunStatusFailed, message); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	ids := make([]string, 0)
	for id, run := range s.runs {
		if run.Status == RunStatusRunning {
			s.runs[id] = completeRun(run, now, RunStatusFailed, coordinator.Result{}, message)
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

func completeRun(run Run, now time.Time, status RunStatus, result coordinator.Result, message string) Run {
	completedAt := now.UTC()
	run.Status = status
	run.CompletedAt = &completedAt
	run.JobName = result.JobName
	run.Output = result.Output
	run.OutputTruncated = result.OutputTruncated
	run.Metrics = slices.Clone(result.Metrics)
	run.Error = message
	return run
}

func copyRun(run Run) Run {
	run.Snapshot.Options = slices.Clone(run.Snapshot.Options)
	run.Metrics = slices.Clone(run.Metrics)
	run.NodeAffinity = *run.NodeAffinity.DeepCopy()
	if run.CompletedAt != nil {
		completedAt := *run.CompletedAt
		run.CompletedAt = &completedAt
	}
	return run
}

// isExpired reports whether a completed run is past its retention window.
// Only completion sets CompletedAt, so running records never expire.
func isExpired(run Run, now time.Time) bool {
	return run.CompletedAt != nil && now.Sub(*run.CompletedAt) >= completedRunRetention
}

// pruneExpiredLocked removes terminal runs whose retention window has elapsed.
// The caller must hold s.mu for writing.
func (s *MemoryRunStore) pruneExpiredLocked(now time.Time) {
	for id, run := range s.runs {
		if isExpired(run, now) {
			delete(s.runs, id)
		}
	}
}

// clock keeps the zero-value MemoryRunStore usable; tests can override now.
func (s *MemoryRunStore) clock() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}
