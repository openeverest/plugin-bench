// Package driver defines benchmark preparation contracts independent of HTTP
// and Kubernetes. Preparation describes a workload without executing it.
package driver

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
)

// Driver implementations must support concurrent calls and must not mutate
// caller-owned input. Prepare performs no network, Kubernetes, or subprocess work.
type Driver interface {
	ID() string
	Supports(target TargetInfo) bool
	Prepare(input PrepareInput) (RunnerSpec, error)
}

// TargetInfo describes the authorized target. Drivers match on DatabaseType;
// Provider is available for provider-specific connection details.
type TargetInfo struct {
	DatabaseType string
	Provider     string
}

// ConnectionDetails contains upstream credentials. Do not log or persist it.
type ConnectionDetails struct {
	Host     string
	Port     string
	Username string
	Password string
	URI      string
	Database string
}

// PrepareInput carries options in their public JSON form. The selected driver
// strictly decodes them into its typed options after applying its defaults.
type PrepareInput struct {
	Target            TargetInfo
	Connection        ConnectionDetails
	RequestedDatabase string
	Options           json.RawMessage
}

// RunnerConfig comes from trusted deployment configuration, never the request.
type RunnerConfig struct {
	Image           string
	ImagePullPolicy string
}

func (c RunnerConfig) Validate() error {
	if strings.TrimSpace(c.Image) == "" {
		return errors.New("runner image is required")
	}
	switch c.ImagePullPolicy {
	case "Always", "IfNotPresent", "Never":
		return nil
	default:
		return errors.New("image pull policy must be Always, IfNotPresent, or Never")
	}
}

// OptionSnapshot contains only safe, effective settings after driver defaults
// and validation. Drivers must keep credentials out of this snapshot.
type OptionSnapshot struct {
	Database string
	Options  json.RawMessage
}

// RunnerSpec is an internal execution description, not an API response or a run
// record. SecretData and SensitiveValues contain credentials: never log this value.
// Empty Command/Args preserve the runner image's entrypoint and arguments.
type RunnerSpec struct {
	Command         []string
	Args            []string
	Env             map[string]string // Literal values; the coordinator escapes Kubernetes environment expansion.
	SecretData      map[string][]byte // True secrets only; every value is redacted from output. Use Env for ordinary settings.
	SecretEnv       map[string]string // Environment name -> key in the per-run Secret.
	SensitiveValues []string          // Additional sensitive representations to redact.
	Snapshot        OptionSnapshot
}

// Clone owns every mutable collection, including Secret byte values and options
// JSON, so subsequent caller mutations cannot alter the prepared execution.
func (s RunnerSpec) Clone() RunnerSpec {
	clone := s
	clone.Command = slices.Clone(s.Command)
	clone.Args = slices.Clone(s.Args)
	clone.Env = maps.Clone(s.Env)
	clone.SecretData = maps.Clone(s.SecretData)
	for key, value := range clone.SecretData {
		clone.SecretData[key] = slices.Clone(value)
	}
	clone.SecretEnv = maps.Clone(s.SecretEnv)
	clone.SensitiveValues = slices.Clone(s.SensitiveValues)
	clone.Snapshot.Options = slices.Clone(s.Snapshot.Options)
	return clone
}

// Validate checks shared workload invariants. Tool-specific values and safe
// snapshot construction remain the driver's responsibility. Errors deliberately
// omit map keys and values, because malformed input may itself contain secrets.
func (s RunnerSpec) Validate() error {
	for name := range s.Env {
		if strings.TrimSpace(name) == "" {
			return errors.New("runner environment names must not be blank")
		}
	}
	for key := range s.SecretData {
		if strings.TrimSpace(key) == "" {
			return errors.New("runner Secret keys must not be blank")
		}
	}
	for name, key := range s.SecretEnv {
		if strings.TrimSpace(name) == "" {
			return errors.New("runner secret environment names must not be blank")
		}
		if _, exists := s.Env[name]; exists {
			return errors.New("runner environment name cannot appear in both Env and SecretEnv")
		}
		if _, exists := s.SecretData[key]; !exists {
			return errors.New("runner secret environment reference must identify an existing Secret key")
		}
	}
	return nil
}
