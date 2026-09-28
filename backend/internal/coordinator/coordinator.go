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
	if _, err := resourceRequirements(c.Resources); err != nil {
		return fmt.Errorf("invalid runner resources: %w", err)
	}
	return nil
}

type Resources struct {
	CPURequest    string
	MemoryRequest string
	CPULimit      string
	MemoryLimit   string
}

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
