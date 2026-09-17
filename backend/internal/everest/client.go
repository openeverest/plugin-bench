package everest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultRequestTimeout = 10 * time.Second

var (
	ErrUnauthorized     = errors.New("OpenEverest authentication failed")
	ErrForbidden        = errors.New("OpenEverest access denied")
	ErrNotFound         = errors.New("OpenEverest target not found")
	ErrUpstream         = errors.New("OpenEverest API failure")
	ErrUnexpectedStatus = errors.New("unexpected OpenEverest API response")

	everestHTTPClient = &http.Client{
		Timeout: defaultRequestTimeout,
	}
)

// RequestError identifies a failure to send or complete a request to the
// OpenEverest API. The underlying error is retained so callers can still use
// errors.Is for context cancellation and deadline errors.
type RequestError struct {
	Err error
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("OpenEverest credentials request failed: %v", e.Err)
}

func (e *RequestError) Unwrap() error {
	return e.Err
}

// ResponseError identifies an unsuccessful HTTP status from the OpenEverest
// API. The response body is intentionally not included because it may contain
// credentials or other sensitive information.
type ResponseError struct {
	StatusCode int
	Kind       error
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("OpenEverest credentials endpoint returned status %d: %v", e.StatusCode, e.Kind)
}

func (e *ResponseError) Unwrap() error {
	return e.Kind
}

// ResponseReadError identifies a failure while reading the API response body.
// This is distinct from a JSON decoding error because the body may be
// incomplete or unavailable.
type ResponseReadError struct {
	Err error
}

func (e *ResponseReadError) Error() string {
	return fmt.Sprintf("failed to read OpenEverest credentials response: %v", e.Err)
}

func (e *ResponseReadError) Unwrap() error {
	return e.Err
}

// InvalidResponseError identifies a successful HTTP response that does not
// contain usable credentials.
type InvalidResponseError struct {
	Err error
}

func (e *InvalidResponseError) Error() string {
	return fmt.Sprintf("invalid OpenEverest credentials response: %v", e.Err)
}

func (e *InvalidResponseError) Unwrap() error {
	return e.Err
}

type Credentials struct {
	URI      string `json:"uri"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	Provider string `json:"provider"`
	Type     string `json:"type"`
}

func validateCredentials(creds Credentials) error {
	if strings.TrimSpace(creds.Host) == "" {
		return fmt.Errorf("missing host")
	}

	port := strings.TrimSpace(creds.Port)
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("invalid port")
	}

	if strings.TrimSpace(creds.Username) == "" {
		return fmt.Errorf("missing username")
	}

	if strings.TrimSpace(creds.Password) == "" && strings.TrimSpace(creds.URI) == "" {
		return fmt.Errorf("missing password or URI")
	}

	return nil
}

func everestAPIURL() string {
	// Explicit override wins (useful for local dev or air-gapped setups).
	if v := os.Getenv("EVEREST_API_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	// Kubernetes automatically injects <SVCNAME>_SERVICE_HOST / _SERVICE_PORT
	// for every Service in the same namespace, so we get this for free when
	// the plugin pod runs alongside the "everest" Service in everest-system.
	if host := os.Getenv("EVEREST_SERVICE_HOST"); host != "" {
		port := os.Getenv("EVEREST_SERVICE_PORT")
		if port == "" {
			port = "8080"
		}
		return "http://" + host + ":" + port
	}
	// Fallback: stable DNS name of the everest Service.
	return "http://everest.everest-system.svc.cluster.local:8080"
}

func errorKindForStatus(statusCode int) error {
	switch statusCode {
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusForbidden:
		return ErrForbidden
	case http.StatusNotFound:
		return ErrNotFound
	}
	if statusCode >= http.StatusInternalServerError {
		return ErrUpstream
	}
	return ErrUnexpectedStatus
}

// GetCredentials retrieves connection details for an OpenEverest-managed
// PostgreSQL instance using the caller's authorization token.
func GetCredentials(ctx context.Context, jwt, k8sCluster, namespace, instance string) (*Credentials, error) {
	apiURL := fmt.Sprintf("%s/v1/clusters/%s/namespaces/%s/instances/%s/connection",
		everestAPIURL(),
		url.PathEscape(k8sCluster),
		url.PathEscape(namespace),
		url.PathEscape(instance),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, &RequestError{Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+jwt)

	resp, err := everestHTTPClient.Do(req)
	if err != nil {
		return nil, &RequestError{Err: err}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &ResponseReadError{Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &ResponseError{
			StatusCode: resp.StatusCode,
			Kind:       errorKindForStatus(resp.StatusCode),
		}
	}

	var creds Credentials
	if err := json.Unmarshal(body, &creds); err != nil {
		return nil, &InvalidResponseError{Err: fmt.Errorf("failed to decode credentials: %w", err)}
	}
	if err := validateCredentials(creds); err != nil {
		return nil, &InvalidResponseError{Err: err}
	}
	return &creds, nil
}
