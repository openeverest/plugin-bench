package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	"github.com/openeverest/plugin-bench/backend/internal/everest"
	corev1 "k8s.io/api/core/v1"
)

const maxCreateRunRequestBytes = 16 << 10
const maxConcurrentRuns = 2

const (
	unschedulableRunMessage        = "benchmark runner could not be scheduled: no node satisfies the placement rules and resource requests"
	unschedulableRunTimeoutMessage = "benchmark execution timed out before the runner could be scheduled: no node satisfies the placement rules and resource requests"
)

const (
	RunStatusRunning   RunStatus = "running"
	RunStatusSucceeded RunStatus = "succeeded"
	RunStatusFailed    RunStatus = "failed"
)

// Target identifies an instance registered with OpenEverest.
type Target struct {
	K8sCluster string `json:"k8sCluster"`
	Namespace  string `json:"namespace"`
	Instance   string `json:"instance"`
}

// CreateRunRequest contains the target and per-run benchmark settings.
type CreateRunRequest struct {
	Target       Target                `json:"target"`
	Database     string                `json:"database"`
	Driver       string                `json:"driver"`
	Options      json.RawMessage       `json:"options"`
	Resources    coordinator.Resources `json:"resources"`
	NodeAffinity *corev1.NodeAffinity  `json:"nodeAffinity,omitempty"`
}

// RunStatus describes the execution workflow. Running does not imply that
// the Kubernetes runner Pod has already started.
type RunStatus string

// RunSnapshot records effective, safe configuration after driver preparation.
type RunSnapshot struct {
	Driver      string          `json:"driver"`
	RunnerImage string          `json:"runnerImage"`
	Database    string          `json:"database"`
	Options     json.RawMessage `json:"options"`
}

// Run is the record retained for status and result retrieval.
type Run struct {
	ID              string                `json:"id"`
	Target          Target                `json:"target"`
	Snapshot        RunSnapshot           `json:"snapshot"`
	Resources       coordinator.Resources `json:"resources"`
	NodeAffinity    corev1.NodeAffinity   `json:"nodeAffinity"`
	Status          RunStatus             `json:"status"`
	CreatedAt       time.Time             `json:"createdAt"`
	CompletedAt     *time.Time            `json:"completedAt,omitempty"`
	JobName         string                `json:"jobName,omitempty"`
	Output          string                `json:"output,omitempty"`
	OutputTruncated bool                  `json:"outputTruncated"`
	Error           string                `json:"error,omitempty"`
}

type RunStatusResponse struct {
	Driver          string                `json:"driver"`
	RunnerImage     string                `json:"runnerImage"`
	Options         json.RawMessage       `json:"options"`
	ID              string                `json:"id"`
	Status          RunStatus             `json:"status"`
	Database        string                `json:"database,omitempty"`
	Resources       coordinator.Resources `json:"resources"`
	NodeAffinity    corev1.NodeAffinity   `json:"nodeAffinity"`
	CreatedAt       time.Time             `json:"createdAt"`
	CompletedAt     *time.Time            `json:"completedAt,omitempty"`
	JobName         string                `json:"jobName,omitempty"`
	Output          string                `json:"output,omitempty"`
	OutputTruncated bool                  `json:"outputTruncated"`
	Error           string                `json:"error,omitempty"`
}

func (a *API) createRun(w http.ResponseWriter, r *http.Request) {
	token, err := extractBearerToken(r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	request, err := decodeCreateRunRequest(w, r)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errUnsupportedContentType) {
			status = http.StatusUnsupportedMediaType
		} else {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				status = http.StatusRequestEntityTooLarge
			}
		}
		writeError(w, status, err.Error())
		return
	}
	request, resolvedResources, err := a.prepareSharedRequest(request)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if a.registry == nil {
		writeError(w, http.StatusServiceUnavailable, "benchmark drivers are unavailable")
		return
	}
	if a.getCredentials == nil {
		writeError(w, http.StatusServiceUnavailable, "database credential lookup is unavailable")
		return
	}
	credentials, err := a.getCredentials(
		r.Context(), token,
		request.Target.K8sCluster,
		request.Target.Namespace,
		request.Target.Instance,
	)
	if err != nil {
		status, message := credentialLookupError(err)
		if status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		writeError(w, status, message)
		return
	}
	prepared, err := a.prepareRun(request, credentials)
	if err != nil {
		status, message := preparationError(err)
		writeError(w, status, message)
		return
	}
	if a.lifecycleCtx.Err() != nil {
		writeError(w, http.StatusServiceUnavailable, "benchmark service is shutting down")
		return
	}
	if a.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "benchmark runner is unavailable")
		return
	}
	if !a.registerRun() {
		writeError(w, http.StatusServiceUnavailable, "benchmark service is shutting down")
		return
	}
	select {
	case a.runSlots <- struct{}{}:
	default:
		a.runs.Done()
		writeError(w, http.StatusTooManyRequests, "benchmark capacity is currently full")
		return
	}

	runID, err := newRunID()
	if err != nil {
		<-a.runSlots
		a.runs.Done()
		writeError(w, http.StatusInternalServerError, "failed to create benchmark run")
		return
	}
	run, err := a.store.Create(runID, request.Target, prepared.Snapshot, resolvedResources, prepared.Input.NodeAffinity)
	if err != nil {
		<-a.runSlots
		a.runs.Done()
		writeError(w, http.StatusInternalServerError, "failed to create benchmark run")
		return
	}
	runCtx, cancel := context.WithCancel(a.lifecycleCtx)
	go a.executeRun(runCtx, cancel, run.ID, prepared)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"id": run.ID, "status": string(run.Status)})
}

func newRunID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func (a *API) executeRun(ctx context.Context, cancel context.CancelFunc, id string, prepared preparedRun) {
	defer a.runs.Done()
	defer func() { <-a.runSlots }()
	defer cancel()
	result, err := a.runner.Run(ctx, prepared.Input)
	result.Output = sanitizeRunnerOutput(result.Output, prepared.SensitiveValues...)
	status, message := RunStatusSucceeded, ""
	if err != nil {
		status, message = RunStatusFailed, "benchmark execution failed"
		// Scheduler diagnostics reveal node counts and taints, so users get a
		// generic message and the details stay in the backend log.
		var schedulingErr *coordinator.SchedulingError
		if errors.As(err, &schedulingErr) {
			message = unschedulableRunMessage
			if errors.Is(err, context.DeadlineExceeded) {
				message = unschedulableRunTimeoutMessage
			}
		}
		log.Printf(
			"benchmark run %q failed: %v (job=%q, output_bytes=%d, output_truncated=%t)",
			id, sanitizeRunnerOutput(err.Error(), prepared.SensitiveValues...), result.JobName, len(result.Output), result.OutputTruncated,
		)
	}
	if err := a.store.Complete(id, status, result, message); err != nil {
		log.Printf("benchmark run %q result could not be stored: %v", id, err)
		return
	}
}

func credentialLookupError(err error) (int, string) {
	return mapOpenEverestError(
		err,
		"timed out retrieving database connection details",
		"failed to retrieve database connection details",
	)
}

func instanceAccessError(err error) (int, string) {
	return mapOpenEverestError(
		err,
		"timed out verifying access to database target",
		"failed to verify access to database target",
	)
}

func mapOpenEverestError(err error, timeoutMessage, upstreamMessage string) (int, string) {
	switch {
	case errors.Is(err, everest.ErrUnauthorized):
		return http.StatusUnauthorized, "OpenEverest authentication failed"
	case errors.Is(err, everest.ErrForbidden):
		return http.StatusForbidden, "access to the database target is denied"
	case errors.Is(err, everest.ErrNotFound):
		return http.StatusNotFound, "database target was not found"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, timeoutMessage
	default:
		return http.StatusBadGateway, upstreamMessage
	}
}

func (a *API) getRun(w http.ResponseWriter, r *http.Request) {
	token, err := extractBearerToken(r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	run, err := a.store.Get(r.PathValue("id"))
	if errors.Is(err, ErrRunNotFound) {
		writeError(w, http.StatusNotFound, "benchmark run not found")
		return
	}
	if err != nil {
		log.Printf("benchmark run %q could not be retrieved from the run store: %v", r.PathValue("id"), err)
		writeError(w, http.StatusInternalServerError, "failed to retrieve benchmark run")
		return
	}
	if a.checkInstanceAccess == nil {
		log.Printf("benchmark run %q cannot be authorized: instance access check is unavailable", run.ID)
		writeError(w, http.StatusServiceUnavailable, "instance access check is unavailable")
		return
	}
	err = a.checkInstanceAccess(
		r.Context(), token,
		run.Target.K8sCluster,
		run.Target.Namespace,
		run.Target.Instance,
	)
	if err != nil {
		status, message := instanceAccessError(err)
		log.Printf(
			"benchmark run %q access check failed (cluster=%q namespace=%q instance=%q): %v",
			run.ID,
			run.Target.K8sCluster,
			run.Target.Namespace,
			run.Target.Instance,
			err,
		)
		if status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		writeError(w, status, message)
		return
	}

	response := RunStatusResponse{
		Driver:          run.Snapshot.Driver,
		RunnerImage:     run.Snapshot.RunnerImage,
		Options:         run.Snapshot.Options,
		ID:              run.ID,
		Status:          run.Status,
		Database:        run.Snapshot.Database,
		Resources:       run.Resources,
		NodeAffinity:    run.NodeAffinity,
		CreatedAt:       run.CreatedAt,
		CompletedAt:     run.CompletedAt,
		JobName:         run.JobName,
		Output:          run.Output,
		OutputTruncated: run.OutputTruncated,
		Error:           run.Error,
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("benchmark run %q status response could not be written: %v", run.ID, err)
	}
}
