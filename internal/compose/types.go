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
	// Volumes accepts either compose's short-form string ("name:/path"
	// / ":/path" / "./host:/container:ro") or a long-form MountLong
	// struct. We need both because compose's short form has no
	// subpath syntax — for emptyDir/PVC mounts that need to share
	// one volume across several paths via different subPaths
	// (Bitnami helm chart pattern), only the long form works.
	Volumes []any `yaml:"volumes,omitempty"`
	// DependsOn uses the long form so we can express conditions —
	// notably `service_completed_successfully` for init-container
	// dependencies. The map keys are dependency service names.
	DependsOn map[string]DependsOnSpec `yaml:"depends_on,omitempty"`
	Restart   string                   `yaml:"restart,omitempty"`
	Deploy    *Deploy                  `yaml:"deploy,omitempty"`
	// Networks attaches the service to one or more compose networks.
	// We use the long form (map[name]ServiceNetwork) so we can express
	// per-network aliases — the mechanism that makes k8s-style FQDNs
	// like `nats-headless.default.svc.cluster.local` resolve inside
	// the compose network without changing the service name itself.
	Networks map[string]ServiceNetwork `yaml:"networks,omitempty"`
	// NetworkMode lets a sidecar service share another service's network
	// namespace via "service:<name>", matching the k8s "all containers in
	// a pod share an IP" model. Mutually exclusive with Ports.
	NetworkMode string `yaml:"network_mode,omitempty"`
	// Platform pins the image variant compose pulls — typically
	// "linux/amd64" when the host is Apple Silicon and the registry
	// only ships amd64 builds. Empty means "no preference" (Docker
	// picks based on the host arch).
	Platform string `yaml:"platform,omitempty"`
	// Hostname is the unix hostname inside the container. Set this
	// to a k8s FQDN form for workloads whose own env vars / config
	// reference the FQDN — Erlang's `USE_LONGNAME=true`, Akka,
	// distributed RPC, etc. all do `gethostbyname(self_fqdn)` to
	// bind their listener; without a matching hostname Docker's
	// embedded DNS resolves the FQDN for *other* containers but
	// not for the container itself, and the bind fails.
	Hostname string `yaml:"hostname,omitempty"`
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

// ServiceNetwork is the per-service network entry used to declare
// network aliases (additional DNS names that resolve to the service
// inside the compose network).
type ServiceNetwork struct {
	Aliases []string `yaml:"aliases,omitempty"`
}

// MountLong is the long-form volume mount entry. We only emit it
// when the short string syntax can't represent what we need — at
// the moment that's only `volume.subpath`, which compose supports
// only in the long form.
type MountLong struct {
	Type     string           `yaml:"type"`
	Source   string           `yaml:"source"`
	Target   string           `yaml:"target"`
	ReadOnly bool             `yaml:"read_only,omitempty"`
	Volume   *MountVolumeOpts `yaml:"volume,omitempty"`
}

// MountVolumeOpts carries volume-type-specific options. Right now
// only subpath, but bind/tmpfs would slot in next to it.
type MountVolumeOpts struct {
	Subpath string `yaml:"subpath,omitempty"`
}
