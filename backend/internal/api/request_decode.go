package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	corev1 "k8s.io/api/core/v1"
)

var errUnsupportedContentType = errors.New("Content-Type must be application/json")

// legacyRunOptions exists only at the HTTP migration boundary. Both request
// shapes enter the same driver preparation and execution path.
type legacyRunOptions struct {
	DurationSeconds int  `json:"durationSeconds"`
	Clients         int  `json:"clients"`
	Threads         int  `json:"threads"`
	Scale           int  `json:"scale"`
	Initialize      bool `json:"initialize"`
}

func decodeCreateRunRequest(w http.ResponseWriter, r *http.Request) (CreateRunRequest, error) {
	var request CreateRunRequest
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return request, errUnsupportedContentType
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxCreateRunRequestBytes)
	defer r.Body.Close()
	var raw json.RawMessage
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&raw); err != nil {
		return request, requestDecodeError(err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return request, err
		}
		return request, errors.New("request body must contain exactly one JSON object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return request, errors.New("request body must contain a JSON object")
	}
	_, hasDriver := fields["driver"]
	_, hasOptions := fields["options"]
	if hasDriver || hasOptions {
		strict := json.NewDecoder(bytes.NewReader(raw))
		strict.DisallowUnknownFields()
		if err := strict.Decode(&request); err != nil {
			return request, errors.New("invalid benchmark request fields")
		}
		if !hasDriver || request.Driver == "" {
			return request, errors.New("driver is required")
		}
		options := bytes.TrimSpace(request.Options)
		if !hasOptions || len(options) == 0 || options[0] != '{' {
			return request, errors.New("options must be a JSON object")
		}
		return request, nil
	}
	var legacy struct {
		Target       Target                `json:"target"`
		Database     string                `json:"database"`
		Resources    coordinator.Resources `json:"resources"`
		NodeAffinity *corev1.NodeAffinity  `json:"nodeAffinity,omitempty"`
		legacyRunOptions
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&legacy); err != nil {
		return request, errors.New("invalid legacy benchmark request fields")
	}
	// Explicitly marshal zero values: legacy numeric fields remain required,
	// whereas omitted modern options receive the driver's defaults.
	options, err := json.Marshal(legacy.legacyRunOptions)
	if err != nil {
		return request, errors.New("invalid legacy benchmark options")
	}
	return CreateRunRequest{Target: legacy.Target, Database: legacy.Database, Resources: legacy.Resources,
		NodeAffinity: legacy.NodeAffinity, Driver: "pgbench", Options: options}, nil
}

func requestDecodeError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return err
	}
	return errors.New("request body must contain a valid JSON object")
}
