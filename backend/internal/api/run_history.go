package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	"github.com/openeverest/plugin-bench/backend/internal/runnerresult"
)

const defaultRunListLimit = 20

// RunSummaryResponse is one history entry. Runner output is omitted because
// it can be up to 1 MiB per run; GET /api/runs/{id} returns it.
type RunSummaryResponse struct {
	ID              string                `json:"id"`
	Status          RunStatus             `json:"status"`
	DatabaseType    string                `json:"databaseType,omitempty"`
	Driver          string                `json:"driver"`
	RunnerImage     string                `json:"runnerImage"`
	Database        string                `json:"database,omitempty"`
	Options         json.RawMessage       `json:"options"`
	Resources       coordinator.Resources `json:"resources"`
	CreatedAt       time.Time             `json:"createdAt"`
	CompletedAt     *time.Time            `json:"completedAt,omitempty"`
	OutputTruncated bool                  `json:"outputTruncated"`
	Metrics         []runnerresult.Metric `json:"metrics,omitempty"`
	Error           string                `json:"error,omitempty"`
}

// listRuns returns one target's history after verifying the caller can read
// that instance, so history is never exposed beyond OpenEverest access.
func (a *API) listRuns(w http.ResponseWriter, r *http.Request) {
	token, err := extractBearerToken(r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	target, limit, err := parseRunListQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if a.checkInstanceAccess == nil {
		writeError(w, http.StatusServiceUnavailable, "instance access check is unavailable")
		return
	}
	if err := a.checkInstanceAccess(r.Context(), token, target.K8sCluster, target.Namespace, target.Instance); err != nil {
		status, message := instanceAccessError(err)
		if status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		writeError(w, status, message)
		return
	}
	runs, err := a.store.ListByTarget(r.Context(), target, limit)
	if err != nil {
		log.Printf("benchmark history could not be listed (cluster=%q namespace=%q instance=%q): %v",
			target.K8sCluster, target.Namespace, target.Instance, err)
		writeError(w, http.StatusInternalServerError, "failed to list benchmark runs")
		return
	}
	summaries := make([]RunSummaryResponse, 0, len(runs))
	for _, run := range runs {
		summaries = append(summaries, RunSummaryResponse{
			ID: run.ID, Status: run.Status, DatabaseType: run.Snapshot.DatabaseType, Driver: run.Snapshot.Driver, RunnerImage: run.Snapshot.RunnerImage,
			Database: run.Snapshot.Database, Options: run.Snapshot.Options, Resources: run.Resources,
			CreatedAt: run.CreatedAt, CompletedAt: run.CompletedAt, OutputTruncated: run.OutputTruncated,
			Metrics: run.Metrics, Error: run.Error,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct {
		Runs []RunSummaryResponse `json:"runs"`
	}{Runs: summaries}); err != nil {
		log.Printf("benchmark history response could not be written: %v", err)
	}
}

func parseRunListQuery(rawQuery string) (Target, int, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return Target{}, 0, errors.New("invalid query string")
	}
	single := func(name string) (string, error) {
		values := query[name]
		if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			return "", errors.New("a single nonempty " + name + " query parameter is required")
		}
		return values[0], nil
	}
	var target Target
	for _, field := range []struct {
		name   string
		target *string
	}{{"k8sCluster", &target.K8sCluster}, {"namespace", &target.Namespace}, {"instance", &target.Instance}} {
		if *field.target, err = single(field.name); err != nil {
			return Target{}, 0, err
		}
	}
	limit := defaultRunListLimit
	if values, present := query["limit"]; present {
		if len(values) != 1 {
			return Target{}, 0, errors.New("limit must be specified once")
		}
		limit, err = strconv.Atoi(values[0])
		if err != nil || ValidateListLimit(limit) != nil {
			return Target{}, 0, errors.New("limit must be an integer from 1 to " + strconv.Itoa(MaxRunListLimit))
		}
	}
	return target, limit, nil
}
