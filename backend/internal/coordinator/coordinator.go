package coordinator

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var ErrKubernetesClientRequired = errors.New("kubernetes client is required")

// Config contains deployment-level settings shared by benchmark runs.
type Config struct {
	WorkloadNamespace    string
	RunnerImage          string
	ImagePullPolicy      string
	RunnerServiceAccount string
	Resources            Resources
	ExecutionTimeout     time.Duration
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.WorkloadNamespace) == "" {
		return errors.New("workload namespace is required")
	}
	if strings.TrimSpace(c.RunnerImage) == "" {
		return errors.New("runner image is required")
	}
	switch c.ImagePullPolicy {
	case "Always", "IfNotPresent", "Never":
	default:
		return errors.New("image pull policy must be Always, IfNotPresent, or Never")
	}
	if strings.TrimSpace(c.RunnerServiceAccount) == "" {
		return errors.New("runner service account is required")
	}
	if c.ExecutionTimeout < time.Second || c.ExecutionTimeout%time.Second != 0 {
		return errors.New("execution timeout must be a positive whole number of seconds")
	}
	return nil
}

// Resources uses Kubernetes quantity strings, such as "100m" and "128Mi".
// Empty fields leave that request or limit unspecified. Quantity parsing and
// request/limit validation belong to Job construction when Kubernetes types
// are introduced.
type Resources struct {
	CPURequest    string
	MemoryRequest string
	CPULimit      string
	MemoryLimit   string
}

// Coordinator holds shared configuration, not per-run credentials or options.
// The Kubernetes client is injected so the coordinator remains testable.
type Coordinator struct {
	config     Config
	kubeClient kubernetes.Interface
}

// New validates the shared configuration and injects the Kubernetes client.
// The interface keeps the coordinator testable with a fake client.
func New(config Config, kubeClient kubernetes.Interface) (*Coordinator, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if kubeClient == nil {
		return nil, ErrKubernetesClientRequired
	}
	return &Coordinator{config: config, kubeClient: kubeClient}, nil
}

// NewInCluster constructs a coordinator using the Pod's ServiceAccount
// credentials. Use New with an injected client in unit tests.
func NewInCluster(config Config) (*Coordinator, error) {
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("load in-cluster Kubernetes config: %w", err)
	}

	kubeClient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	return New(config, kubeClient)
}
