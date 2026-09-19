package coordinator

import (
	"errors"
	"strings"
	"time"
)

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
// Kubernetes client support is added in the next implementation step.
type Coordinator struct {
	config Config
}

// New validates the shared configuration before constructing a coordinator.
func New(config Config) (*Coordinator, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Coordinator{config: config}, nil
}
