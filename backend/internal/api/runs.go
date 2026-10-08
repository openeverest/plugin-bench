package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
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

// RunResources contains Kubernetes quantity strings for the runner container.
// Empty request fields inherit the deployment defaults.
type RunResources struct {
	CPURequest    string `json:"cpuRequest,omitempty"`
	CPULimit      string `json:"cpuLimit,omitempty"`
	MemoryRequest string `json:"memoryRequest,omitempty"`
	MemoryLimit   string `json:"memoryLimit,omitempty"`
}

func (r RunResources) coordinatorResources() coordinator.Resources {
	return coordinator.Resources{
		CPURequest: r.CPURequest, CPULimit: r.CPULimit,
		MemoryRequest: r.MemoryRequest, MemoryLimit: r.MemoryLimit,
	}
}

func runResources(r coordinator.Resources) RunResources {
	return RunResources{
		CPURequest: r.CPURequest, CPULimit: r.CPULimit,
		MemoryRequest: r.MemoryRequest, MemoryLimit: r.MemoryLimit,
	}
}

// CreateRunRequest contains the target and per-run benchmark settings.
type CreateRunRequest struct {
	Target          Target       `json:"target"`
	Database        string       `json:"database"`
	DurationSeconds int          `json:"durationSeconds"`
	Clients         int          `json:"clients"`
	Threads         int          `json:"threads"`
	Scale           int          `json:"scale"`
	Initialize      bool         `json:"initialize"`
	Resources       RunResources `json:"resources"`
}

// RunStatus describes the execution workflow. Running does not imply that
// the Kubernetes runner Pod has already started.
type RunStatus string

// Run is the record retained for status and result retrieval.
type Run struct {
	ID              string           `json:"id"`
	Request         CreateRunRequest `json:"request"`
	Resources       RunResources     `json:"resources"`
	Status          RunStatus        `json:"status"`
	CreatedAt       time.Time        `json:"createdAt"`
	CompletedAt     *time.Time       `json:"completedAt,omitempty"`
	JobName         string           `json:"jobName,omitempty"`
	Output          string           `json:"output,omitempty"`
	OutputTruncated bool             `json:"outputTruncated"`
	Error           string           `json:"error,omitempty"`
}

type RunStatusResponse struct {
	ID              string       `json:"id"`
	Status          RunStatus    `json:"status"`
	Database        string       `json:"database,omitempty"`
	Resources       RunResources `json:"resources"`
	CreatedAt       time.Time    `json:"createdAt"`
	CompletedAt     *time.Time   `json:"completedAt,omitempty"`
	JobName         string       `json:"jobName,omitempty"`
	Output          string       `json:"output,omitempty"`
	OutputTruncated bool         `json:"outputTruncated"`
	Error           string       `json:"error,omitempty"`
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
	if err := validateCreateRunRequest(request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	options := optionsFromRequest(request)
	resolved, err := coordinator.ResolveResources(a.resourceDefaults, options.Resources)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
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
	connection, err := connectionFromCredentials(request.Database, credentials)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	request.Database = connection.Database
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
	run, err := a.store.Create(runID, request, runResources(resolved))
	if err != nil {
		<-a.runSlots
		a.runs.Done()
		writeError(w, http.StatusInternalServerError, "failed to create benchmark run")
		return
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
	defer a.runs.Done()
	result, err := a.runner.Run(ctx, connection, options)
	result.Output = sanitizeRunnerOutput(result.Output, connection.Password)
	status, message := RunStatusSucceeded, ""
	if err != nil {
		status, message = RunStatusFailed, "benchmark execution failed"
		log.Printf(
			"benchmark run %q failed: %v (job=%q, output_bytes=%d, output_truncated=%t)",
			id, err, result.JobName, len(result.Output), result.OutputTruncated,
		)
	}
	if err := a.store.Complete(id, status, result, message); err != nil {

		log.Printf("benchmark run %q result could not be stored: %v", id, err)
		return
	}
}

func sanitizeRunnerOutput(output, password string) string {
	if password == "" {
		return output
	}
	return strings.ReplaceAll(output, password, "[REDACTED]")
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
	database = strings.TrimSpace(database)
	if database == "" {
		database = defaultDatabase(credentials)
	}
	if database == "" {
		return coordinator.Connection{}, errors.New("could not determine the instance's default database; enter a database name")
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

// defaultDatabase prefers the explicit "database" key and falls back to the
// database path of the connection URI.
func defaultDatabase(credentials *everest.Credentials) string {
	if database := strings.TrimSpace(credentials.Database); database != "" {
		return database
	}
	uri, err := url.Parse(strings.TrimSpace(credentials.URI))
	if err != nil {
		return ""
	}
	database := strings.TrimPrefix(uri.Path, "/")
	if strings.Contains(database, "/") {
		return ""
	}
	return strings.TrimSpace(database)
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
	if strings.Contains(request.Database, "=") || strings.HasPrefix(strings.ToLower(request.Database), "postgres://") || strings.HasPrefix(strings.ToLower(request.Database), "postgresql://") {
		return errors.New("database must be a database name, not a connection string")
	}

	return optionsFromRequest(request).Validate()
}

func optionsFromRequest(request CreateRunRequest) coordinator.Options {
	return coordinator.Options{
		Duration:   request.DurationSeconds,
		Clients:    request.Clients,
		Threads:    request.Threads,
		Scale:      request.Scale,
		Initialize: request.Initialize,
		Resources:  request.Resources.coordinatorResources(),
	}
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
		run.Request.Target.K8sCluster,
		run.Request.Target.Namespace,
		run.Request.Target.Instance,
	)
	if err != nil {
		status, message := instanceAccessError(err)
		log.Printf(
			"benchmark run %q access check failed (cluster=%q namespace=%q instance=%q): %v",
			run.ID,
			run.Request.Target.K8sCluster,
			run.Request.Target.Namespace,
			run.Request.Target.Instance,
			err,
		)
		if status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		writeError(w, status, message)
		return
	}

	response := RunStatusResponse{
		ID:              run.ID,
		Status:          run.Status,
		Database:        run.Request.Database,
		Resources:       run.Resources,
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
