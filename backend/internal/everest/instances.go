package everest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	instanceDiscoveryTimeout     = 20 * time.Second
	instanceDiscoveryConcurrency = 4
)

// InstanceSummary is the metadata needed to navigate to an instance. It never
// contains credentials or the full upstream Instance object.
type InstanceSummary struct {
	K8sCluster string `json:"k8sCluster"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	Provider   string `json:"provider"`
}

type discoveryInstance struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		ProviderRef struct {
			Name string `json:"name"`
		} `json:"providerRef"`
	} `json:"spec"`
}

// ListInstances discovers metadata in one Kubernetes cluster using the caller's
// token. OpenEverest filters both lists by that caller's access. Driver/provider
// compatibility is deliberately left to the consumer; discovery reads no secrets.
func ListInstances(ctx context.Context, token, k8sCluster string) ([]InstanceSummary, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrUnauthorized
	}
	if strings.TrimSpace(k8sCluster) == "" {
		return nil, errors.New("Kubernetes cluster is required")
	}
	ctx, cancel := context.WithTimeout(ctx, instanceDiscoveryTimeout)
	defer cancel()

	basePath := "/v1/clusters/" + url.PathEscape(k8sCluster) + "/namespaces"
	var namespaces []string
	if err := getDiscoveryJSON(ctx, token, basePath, &namespaces); err != nil {
		return nil, err
	}
	if namespaces == nil {
		return nil, &InvalidResponseError{Err: errors.New("namespace list must be a JSON array")}
	}

	namespaces = uniqueNamespaces(namespaces)
	listed, err := listNamespaceInstances(ctx, token, basePath, namespaces)
	if err != nil {
		return nil, err
	}

	instances := make([]InstanceSummary, 0)
	type targetKey struct{ cluster, namespace, name string }
	seenTargets := make(map[targetKey]struct{})
	for i, namespace := range namespaces {
		for _, instance := range listed[i] {
			name, provider := instance.Metadata.Name, instance.Spec.ProviderRef.Name
			if strings.TrimSpace(name) == "" || strings.TrimSpace(provider) == "" {
				continue
			}
			key := targetKey{k8sCluster, namespace, name}
			if _, seen := seenTargets[key]; seen {
				continue
			}
			seenTargets[key] = struct{}{}
			instances = append(instances, InstanceSummary{
				K8sCluster: k8sCluster, Namespace: namespace, Name: name, Provider: provider,
			})
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(instances, func(a, b InstanceSummary) int {
		if n := strings.Compare(a.K8sCluster, b.K8sCluster); n != 0 {
			return n
		}
		if n := strings.Compare(a.Namespace, b.Namespace); n != 0 {
			return n
		}
		return strings.Compare(a.Name, b.Name)
	})
	return instances, nil
}

func uniqueNamespaces(namespaces []string) []string {
	unique := make([]string, 0, len(namespaces))
	seen := make(map[string]struct{}, len(namespaces))
	for _, namespace := range namespaces {
		if strings.TrimSpace(namespace) == "" {
			continue
		}
		if _, exists := seen[namespace]; exists {
			continue
		}
		seen[namespace] = struct{}{}
		unique = append(unique, namespace)
	}
	return unique
}

// listNamespaceInstances fetches namespaces with bounded concurrency so large
// clusters do not serialize discovery behind one shared deadline. The first
// failure cancels outstanding requests and is the error reported.
func listNamespaceInstances(ctx context.Context, token, basePath string, namespaces []string) ([][]discoveryInstance, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	results := make([][]discoveryInstance, len(namespaces))
	slots := make(chan struct{}, instanceDiscoveryConcurrency)
	var (
		wg       sync.WaitGroup
		failOnce sync.Once
		firstErr error
	)
	for i, namespace := range namespaces {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer func() {
				<-slots
				wg.Done()
			}()
			var listed struct {
				Items *[]discoveryInstance `json:"items"`
			}
			path := basePath + "/" + url.PathEscape(namespace) + "/instances"
			err := getDiscoveryJSON(ctx, token, path, &listed)
			if err == nil && listed.Items == nil {
				err = &InvalidResponseError{Err: errors.New("instance list must contain an items array")}
			}
			if err != nil {
				// Cancel before releasing the slot so no queued namespace starts.
				failOnce.Do(func() {
					firstErr = err
					cancel(err)
				})
				return
			}
			results[i] = *listed.Items
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func getDiscoveryJSON(ctx context.Context, token, path string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, everestAPIURL()+path, nil)
	if err != nil {
		return &RequestError{Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := everestHTTPClient.Do(req)
	if err != nil {
		return &RequestError{Err: normalizeRequestError(err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &ResponseError{StatusCode: resp.StatusCode, Kind: errorKindForStatus(resp.StatusCode)}
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ResponseReadError{Err: normalizeRequestError(err)}
	}
	if err := json.Unmarshal(body, target); err != nil {
		// Decoder errors can quote upstream contents. Keep the body and its
		// values out of errors that may later be returned or logged.
		return &InvalidResponseError{Err: errors.New("could not decode discovery response")}
	}
	return nil
}
