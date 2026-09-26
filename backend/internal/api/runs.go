package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	"github.com/openeverest/plugin-bench/backend/internal/everest"
)

const maxCreateRunRequestBytes = 16 << 10
const maxConcurrentRuns = 2

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

func (a *API) createRun(w http.ResponseWriter, r *http.Request) {
	token, err := extractBearerToken(r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, err.Error(), http.StatusUnauthorized)
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
		http.Error(w, err.Error(), status)
		return
	}
	if err := validateCreateRunRequest(request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if a.getCredentials == nil {
		http.Error(w, "database credential lookup is unavailable", http.StatusServiceUnavailable)
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
		http.Error(w, message, status)
		return
	}
	connection, err := connectionFromCredentials(request.Database, credentials)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if a.lifecycleCtx.Err() != nil {
		http.Error(w, "benchmark service is shutting down", http.StatusServiceUnavailable)
		return
	}
	if a.runner == nil {
		http.Error(w, "benchmark runner is unavailable", http.StatusServiceUnavailable)
		return
	}
	select {
	case a.runSlots <- struct{}{}:
	default:
		http.Error(w, "benchmark capacity is currently full", http.StatusTooManyRequests)
		return
	}

	runID, err := newRunID()
	if err != nil {
		<-a.runSlots
		http.Error(w, "failed to create benchmark run", http.StatusInternalServerError)
		return
	}
	run, err := a.store.Create(runID, request)
	if err != nil {
		<-a.runSlots
		http.Error(w, "failed to create benchmark run", http.StatusInternalServerError)
		return
	}
	options := coordinator.Options{
		Duration:   request.DurationSeconds,
		Clients:    request.Clients,
		Threads:    request.Threads,
		Scale:      request.Scale,
		Initialize: request.Initialize,
	}
	runCtx, cancel := context.WithCancel(a.lifecycleCtx)
	go a.executeRun(runCtx, cancel, run.ID, connection, options)

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

func (a *API) executeRun(ctx context.Context, cancel context.CancelFunc, id string, connection coordinator.Connection, options coordinator.Options) {
	defer cancel()
	defer func() { <-a.runSlots }()
	result, err := a.runner.Run(ctx, connection, options)
	status, message := RunStatusSucceeded, ""
	if err != nil {
		status, message = RunStatusFailed, "benchmark execution failed"
	}
	if err := a.store.Complete(id, status, result, message); err != nil {
		// A lost run record cannot be repaired here, but the execution goroutine
		// must still release its concurrency slot.
		return
	}
}

func credentialLookupError(err error) (int, string) {
	switch {
	case errors.Is(err, everest.ErrUnauthorized):
		return http.StatusUnauthorized, "OpenEverest authentication failed"
	case errors.Is(err, everest.ErrForbidden):
		return http.StatusForbidden, "access to the database target is denied"
	case errors.Is(err, everest.ErrNotFound):
		return http.StatusNotFound, "database target was not found"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "timed out retrieving database connection details"
	default:
		return http.StatusBadGateway, "failed to retrieve database connection details"
	}
}

func connectionFromCredentials(database string, credentials *everest.Credentials) (coordinator.Connection, error) {
	if credentials == nil {
		return coordinator.Connection{}, errors.New("OpenEverest returned no connection details")
	}
	if !strings.EqualFold(strings.TrimSpace(credentials.Type), "postgresql") {
		return coordinator.Connection{}, errors.New("benchmarking is currently supported only for PostgreSQL targets")
	}
	switch credentials.Provider {
	case "provider-cloudnative-pg", "provider-percona-postgresql":
	default:
		return coordinator.Connection{}, errors.New("benchmarking is not supported for this PostgreSQL provider")
	}
	if strings.TrimSpace(credentials.Password) == "" && strings.TrimSpace(credentials.URI) != "" {
		return coordinator.Connection{}, errors.New("URI-only connection details are not supported by the pgbench runner")
	}
	port, err := strconv.Atoi(credentials.Port)
	if err != nil {
		return coordinator.Connection{}, errors.New("OpenEverest returned an invalid database port")
	}
	connection := coordinator.Connection{
		Host:     credentials.Host,
		Port:     port,
		Database: database,
		Username: credentials.Username,
		Password: credentials.Password,
	}
	if err := connection.Validate(); err != nil {
		return coordinator.Connection{}, errors.New("OpenEverest returned incomplete connection details")
	}
	return connection, nil
}

func validateCreateRunRequest(request CreateRunRequest) error {
	if strings.TrimSpace(request.Target.K8sCluster) == "" {
		return errors.New("target.k8sCluster is required")
	}
	if strings.TrimSpace(request.Target.Namespace) == "" {
		return errors.New("target.namespace is required")
	}
	if strings.TrimSpace(request.Target.Instance) == "" {
		return errors.New("target.instance is required")
	}
	if strings.TrimSpace(request.Database) == "" {
		return errors.New("database is required")
	}
	if strings.Contains(request.Database, "=") || strings.HasPrefix(strings.ToLower(request.Database), "postgres://") || strings.HasPrefix(strings.ToLower(request.Database), "postgresql://") {
		return errors.New("database must be a database name, not a connection string")
	}

	options := coordinator.Options{
		Duration:   request.DurationSeconds,
		Clients:    request.Clients,
		Threads:    request.Threads,
		Scale:      request.Scale,
		Initialize: request.Initialize,
	}
	return options.Validate()
}

var errUnsupportedContentType = errors.New("Content-Type must be application/json")

func decodeCreateRunRequest(w http.ResponseWriter, r *http.Request) (CreateRunRequest, error) {
	var request CreateRunRequest
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return request, errUnsupportedContentType
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxCreateRunRequestBytes)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var decoded *CreateRunRequest
	if err := decoder.Decode(&decoded); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return request, err
		}
		return request, errors.New("request body must contain a valid JSON object")
	}
	if decoded == nil {
		return request, errors.New("request body must contain a JSON object")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return request, err
		}
		return request, errors.New("request body must contain exactly one JSON object")
	}
	return *decoded, nil
}

// getRun is a placeholder until run status retrieval is implemented.
func (a *API) getRun(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "benchmark run status retrieval is not implemented", http.StatusNotImplemented)
}
