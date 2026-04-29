// Package compose models the subset of the Docker Compose schema that
// localk emits. We marshal these types as YAML.
package compose

// File is a complete docker-compose.yml document.
type File struct {
	Version  string             `yaml:"version,omitempty"`
	Services map[string]Service `yaml:"services"`
	Volumes  map[string]Volume  `yaml:"volumes,omitempty"`
	Networks map[string]Network `yaml:"networks,omitempty"`
}

// Service is a single compose service entry.
type Service struct {
	Image       string            `yaml:"image,omitempty"`
	Build       *Build            `yaml:"build,omitempty"`
	Command     []string          `yaml:"command,omitempty"`
	Entrypoint  []string          `yaml:"entrypoint,omitempty"`
	Environment map[string]string `yaml:"environment,omitempty"`
	EnvFile     []string          `yaml:"env_file,omitempty"`
	Ports       []string          `yaml:"ports,omitempty"`
	Volumes     []string          `yaml:"volumes,omitempty"`
	// DependsOn uses the long form so we can express conditions —
	// notably `service_completed_successfully` for init-container
	// dependencies. The map keys are dependency service names.
	DependsOn map[string]DependsOnSpec `yaml:"depends_on,omitempty"`
	Restart   string                   `yaml:"restart,omitempty"`
	Deploy    *Deploy                  `yaml:"deploy,omitempty"`
	Networks  []string                 `yaml:"networks,omitempty"`
	// NetworkMode lets a sidecar service share another service's network
	// namespace via "service:<name>", matching the k8s "all containers in
	// a pod share an IP" model. Mutually exclusive with Ports.
	NetworkMode string `yaml:"network_mode,omitempty"`
}

// DependsOnSpec is the long-form depends_on entry. Common conditions:
// service_started (default in compose v2), service_healthy, and
// service_completed_successfully (used for init-container chains).
type DependsOnSpec struct {
	Condition string `yaml:"condition,omitempty"`
}

// Build configures local image build instead of pulling.
type Build struct {
	Context    string `yaml:"context"`
	Dockerfile string `yaml:"dockerfile,omitempty"`
}

// Deploy carries resource limits when present.
type Deploy struct {
	Resources DeployResources `yaml:"resources,omitempty"`
}

type DeployResources struct {
	Limits       *ResourceSpec `yaml:"limits,omitempty"`
	Reservations *ResourceSpec `yaml:"reservations,omitempty"`
}

type ResourceSpec struct {
	CPUs   string `yaml:"cpus,omitempty"`
	Memory string `yaml:"memory,omitempty"`
}

// Volume is a top-level named volume declaration.
type Volume struct {
	Driver string            `yaml:"driver,omitempty"`
	Labels map[string]string `yaml:"labels,omitempty"`
}

// Network is a top-level network declaration.
type Network struct {
	Driver string `yaml:"driver,omitempty"`
}
