package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/openeverest/plugin-bench/backend/internal/everest"
)

func (a *API) listInstances(w http.ResponseWriter, r *http.Request) {
	token, err := extractBearerToken(r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	clusters := query["k8sCluster"]
	if err != nil || len(clusters) != 1 || strings.TrimSpace(clusters[0]) == "" {
		writeError(w, http.StatusBadRequest, "a single nonempty k8sCluster query parameter is required")
		return
	}
	if a.lookupInstances == nil {
		writeError(w, http.StatusServiceUnavailable, "instance discovery is unavailable")
		return
	}
	instances, err := a.lookupInstances(r.Context(), token, clusters[0])
	if err != nil {
		status, message := mapOpenEverestError(err, "instance discovery timed out", "failed to discover database instances")
		if status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		writeError(w, status, message)
		return
	}
	if instances == nil {
		instances = make([]everest.InstanceSummary, 0)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Instances []everest.InstanceSummary `json:"instances"`
	}{Instances: instances})
}
