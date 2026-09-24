package api

import (
	"net/http"
	"time"
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
	Target          Target `json:"target"`
	Database        string `json:"database"`
	DurationSeconds int    `json:"durationSeconds"`
	Clients         int    `json:"clients"`
	Threads         int    `json:"threads"`
	Scale           int    `json:"scale"`
	Initialize      bool   `json:"initialize"`
}

// RunStatus describes the execution workflow. Running does not imply that
// the Kubernetes runner Pod has already started.
type RunStatus string

// Run is the record retained for status and result retrieval.
type Run struct {
	ID              string           `json:"id"`
	Request         CreateRunRequest `json:"request"`
	Status          RunStatus        `json:"status"`
	CreatedAt       time.Time        `json:"createdAt"`
	CompletedAt     *time.Time       `json:"completedAt,omitempty"`
	JobName         string           `json:"jobName,omitempty"`
	Output          string           `json:"output,omitempty"`
	OutputTruncated bool             `json:"outputTruncated"`
	Error           string           `json:"error,omitempty"`
}

// createRun is a placeholder until asynchronous run creation is implemented.
func (a *API) createRun(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "benchmark run creation is not implemented", http.StatusNotImplemented)
}

// getRun is a placeholder until run status retrieval is implemented.
func (a *API) getRun(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "benchmark run status retrieval is not implemented", http.StatusNotImplemented)
}
