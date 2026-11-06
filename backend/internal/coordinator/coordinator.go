package coordinator

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var ErrKubernetesClientRequired = errors.New("kubernetes client is required")

// Config contains deployment-level settings shared by benchmark runs.
type Config struct {
	// InstallationID identifies this plugin installation (the Helm release).
	// It scopes runner resource labels so restart cleanup never touches
	// another installation's Jobs or Secrets in a shared namespace.
	InstallationID       string
	WorkloadNamespace    string
	RunnerServiceAccount string
	Resources            Resources
	ExecutionTimeout     time.Duration
	// SchedulingGracePeriod limits continuously observed scheduler rejection; zero disables it.
	SchedulingGracePeriod time.Duration
	// NodeSelector and Tolerations are admin-defined placement applied to every
	// runner Job, e.g. to target a tainted, dedicated benchmark node pool.
	NodeSelector map[string]string
	Tolerations  []corev1.Toleration
}

func (c Config) Validate() error {
	if c.InstallationID == "" || len(validation.IsValidLabelValue(c.InstallationID)) != 0 {
		return errors.New("installation ID must be a nonempty Kubernetes label value")
	}
	if strings.TrimSpace(c.WorkloadNamespace) == "" {
		return errors.New("workload namespace is required")
	}
	if strings.TrimSpace(c.RunnerServiceAccount) == "" {
		return errors.New("runner service account is required")
	}
	if c.ExecutionTimeout < time.Second || c.ExecutionTimeout%time.Second != 0 {
		return errors.New("execution timeout must be a positive whole number of seconds")
	}
	if c.SchedulingGracePeriod < 0 || c.SchedulingGracePeriod%time.Second != 0 {
		return errors.New("scheduling grace period must be a nonnegative whole number of seconds")
	}
	if _, err := resourceRequirements(c.Resources); err != nil {
		return fmt.Errorf("invalid runner resources: %w", err)
	}
	if err := validateRunnerPlacement(c.NodeSelector, c.Tolerations); err != nil {
		return fmt.Errorf("invalid runner placement: %w", err)
	}
	return nil
}

// Coordinator holds shared configuration, not per-run credentials or options.
// The Kubernetes client is injected so the coordinator remains testable.
type Coordinator struct {
	config     Config
	kubeClient kubernetes.Interface
	// processID labels the runner resources this backend process creates, so
	// orphan cleanup can tell them apart from an earlier process's leftovers.
	processID string
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
	var processID [8]byte
	if _, err := rand.Read(processID[:]); err != nil {
		return nil, fmt.Errorf("generate backend process ID: %w", err)
	}
	return &Coordinator{config: config, kubeClient: kubeClient, processID: hex.EncodeToString(processID[:])}, nil
}

// NewInCluster constructs a coordinator using the Pod's ServiceAccount.
// Use New with an injected client in unit tests.
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
