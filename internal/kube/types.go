// Package kube defines the minimal subset of Kubernetes resource types
// that localk parses. We deliberately avoid depending on k8s.io/api to keep
// the binary small and the dependency graph tight; we only need a handful
// of fields from each resource type.
package kube

// Resource is the common envelope for any Kubernetes object.
// We use it during the initial pass to dispatch on Kind.
type Resource struct {
	APIVersion string     `yaml:"apiVersion"`
	Kind       string     `yaml:"kind"`
	Metadata   ObjectMeta `yaml:"metadata"`
}

// ObjectMeta mirrors metav1.ObjectMeta with only the fields we use.
type ObjectMeta struct {
	Name        string            `yaml:"name"`
	Namespace   string            `yaml:"namespace,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty"`
	Annotations map[string]string `yaml:"annotations,omitempty"`
}

// Deployment is a subset of appsv1.Deployment.
type Deployment struct {
	APIVersion string         `yaml:"apiVersion"`
	Kind       string         `yaml:"kind"`
	Metadata   ObjectMeta     `yaml:"metadata"`
	Spec       DeploymentSpec `yaml:"spec"`
}

type DeploymentSpec struct {
	Replicas int32       `yaml:"replicas,omitempty"`
	Selector LabelSelect `yaml:"selector"`
	Template PodTemplate `yaml:"template"`
}

// StatefulSet is a subset of appsv1.StatefulSet. The pod template is
// identical to a Deployment's; the differentiator is volumeClaimTemplates,
// which Kubernetes uses to provision a per-replica PVC. Locally we collapse
// each template into a single named compose volume — replicas don't make
// sense in compose anyway, so one stateful service gets one persistent
// volume per template.
type StatefulSet struct {
	APIVersion string          `yaml:"apiVersion"`
	Kind       string          `yaml:"kind"`
	Metadata   ObjectMeta      `yaml:"metadata"`
	Spec       StatefulSetSpec `yaml:"spec"`
}

type StatefulSetSpec struct {
	Replicas             int32                           `yaml:"replicas,omitempty"`
	Selector             LabelSelect                     `yaml:"selector"`
	Template             PodTemplate                     `yaml:"template"`
	ServiceName          string                          `yaml:"serviceName,omitempty"`
	VolumeClaimTemplates []PersistentVolumeClaimTemplate `yaml:"volumeClaimTemplates,omitempty"`
}

// PersistentVolumeClaimTemplate is the inline-PVC shape that StatefulSets
// use to provision per-replica storage. Only the fields we actually need
// to map into compose volumes are modeled.
type PersistentVolumeClaimTemplate struct {
	Metadata ObjectMeta `yaml:"metadata"`
	Spec     PVCSpec    `yaml:"spec,omitempty"`
}

type LabelSelect struct {
	MatchLabels map[string]string `yaml:"matchLabels,omitempty"`
}

type PodTemplate struct {
	Metadata ObjectMeta `yaml:"metadata"`
	Spec     PodSpec    `yaml:"spec"`
}

type PodSpec struct {
	Containers     []Container `yaml:"containers"`
	InitContainers []Container `yaml:"initContainers,omitempty"`
	Volumes        []Volume    `yaml:"volumes,omitempty"`
	RestartPolicy  string      `yaml:"restartPolicy,omitempty"`
}

type Container struct {
	Name         string          `yaml:"name"`
	Image        string          `yaml:"image"`
	Command      []string        `yaml:"command,omitempty"`
	Args         []string        `yaml:"args,omitempty"`
	Env          []EnvVar        `yaml:"env,omitempty"`
	EnvFrom      []EnvFromSource `yaml:"envFrom,omitempty"`
	Ports        []ContainerPort `yaml:"ports,omitempty"`
	VolumeMounts []VolumeMount   `yaml:"volumeMounts,omitempty"`
	Resources    ResourceReqs    `yaml:"resources,omitempty"`
}

type EnvVar struct {
	Name      string        `yaml:"name"`
	Value     string        `yaml:"value,omitempty"`
	ValueFrom *EnvVarSource `yaml:"valueFrom,omitempty"`
}

type EnvVarSource struct {
	ConfigMapKeyRef *KeyRef         `yaml:"configMapKeyRef,omitempty"`
	SecretKeyRef    *KeyRef         `yaml:"secretKeyRef,omitempty"`
	FieldRef        *ObjectFieldRef `yaml:"fieldRef,omitempty"`
}

// ObjectFieldRef is the downward-API selector — k8s pods can read their own
// metadata into env vars via this ref. Locally we resolve well-known paths
// (metadata.name, metadata.namespace, status.podIP, ...) to sensible
// defaults so manifests that depend on the downward API don't break.
type ObjectFieldRef struct {
	FieldPath string `yaml:"fieldPath"`
}

type KeyRef struct {
	Name string `yaml:"name"`
	Key  string `yaml:"key"`
}

type EnvFromSource struct {
	ConfigMapRef *RefName `yaml:"configMapRef,omitempty"`
	SecretRef    *RefName `yaml:"secretRef,omitempty"`
	Prefix       string   `yaml:"prefix,omitempty"`
}

type RefName struct {
	Name string `yaml:"name"`
}

type ContainerPort struct {
	Name          string `yaml:"name,omitempty"`
	ContainerPort int32  `yaml:"containerPort"`
	Protocol      string `yaml:"protocol,omitempty"`
}

type VolumeMount struct {
	Name      string `yaml:"name"`
	MountPath string `yaml:"mountPath"`
	SubPath   string `yaml:"subPath,omitempty"`
	ReadOnly  bool   `yaml:"readOnly,omitempty"`
}

type Volume struct {
	Name      string        `yaml:"name"`
	ConfigMap *ConfigMapVol `yaml:"configMap,omitempty"`
	Secret    *SecretVol    `yaml:"secret,omitempty"`
	PVC       *PVCVolSource `yaml:"persistentVolumeClaim,omitempty"`
	EmptyDir  *EmptyDirVol  `yaml:"emptyDir,omitempty"`
	HostPath  *HostPathVol  `yaml:"hostPath,omitempty"`
}

type ConfigMapVol struct {
	Name string `yaml:"name"`
}

type SecretVol struct {
	SecretName string `yaml:"secretName"`
}

type PVCVolSource struct {
	ClaimName string `yaml:"claimName"`
}

type EmptyDirVol struct{}

type HostPathVol struct {
	Path string `yaml:"path"`
}

type ResourceReqs struct {
	Limits   ResourceList `yaml:"limits,omitempty"`
	Requests ResourceList `yaml:"requests,omitempty"`
}

type ResourceList struct {
	CPU    string `yaml:"cpu,omitempty"`
	Memory string `yaml:"memory,omitempty"`
}

// Service is a subset of corev1.Service.
type Service struct {
	APIVersion string      `yaml:"apiVersion"`
	Kind       string      `yaml:"kind"`
	Metadata   ObjectMeta  `yaml:"metadata"`
	Spec       ServiceSpec `yaml:"spec"`
}

type ServiceSpec struct {
	Type     string            `yaml:"type,omitempty"`
	Selector map[string]string `yaml:"selector,omitempty"`
	Ports    []ServicePort     `yaml:"ports"`
}

type ServicePort struct {
	Name       string `yaml:"name,omitempty"`
	Port       int32  `yaml:"port"`
	TargetPort any    `yaml:"targetPort,omitempty"` // int or string
	Protocol   string `yaml:"protocol,omitempty"`
}

// ConfigMap is a subset of corev1.ConfigMap.
type ConfigMap struct {
	APIVersion string            `yaml:"apiVersion"`
	Kind       string            `yaml:"kind"`
	Metadata   ObjectMeta        `yaml:"metadata"`
	Data       map[string]string `yaml:"data,omitempty"`
}

// Secret is a subset of corev1.Secret. Values may be base64-encoded under
// `data` or plain under `stringData` — we support both for parsing but warn
// and emit them into a `.env` file regardless.
type Secret struct {
	APIVersion string            `yaml:"apiVersion"`
	Kind       string            `yaml:"kind"`
	Metadata   ObjectMeta        `yaml:"metadata"`
	Type       string            `yaml:"type,omitempty"`
	Data       map[string]string `yaml:"data,omitempty"`
	StringData map[string]string `yaml:"stringData,omitempty"`
}

// PersistentVolumeClaim is a subset of corev1.PersistentVolumeClaim.
type PersistentVolumeClaim struct {
	APIVersion string     `yaml:"apiVersion"`
	Kind       string     `yaml:"kind"`
	Metadata   ObjectMeta `yaml:"metadata"`
	Spec       PVCSpec    `yaml:"spec"`
}

type PVCSpec struct {
	StorageClassName string   `yaml:"storageClassName,omitempty"`
	AccessModes      []string `yaml:"accessModes,omitempty"`
}

// Ingress is a subset of networkingv1.Ingress, modeling host- and
// path-based HTTP routing. Locally we materialize this as a Caddy reverse
// proxy that forwards <host>/<path> to the named compose service on the
// shared compose network.
type Ingress struct {
	APIVersion string      `yaml:"apiVersion"`
	Kind       string      `yaml:"kind"`
	Metadata   ObjectMeta  `yaml:"metadata"`
	Spec       IngressSpec `yaml:"spec"`
}

type IngressSpec struct {
	Rules []IngressRule `yaml:"rules,omitempty"`
}

type IngressRule struct {
	Host string           `yaml:"host,omitempty"`
	HTTP *IngressRuleHTTP `yaml:"http,omitempty"`
}

type IngressRuleHTTP struct {
	Paths []IngressPath `yaml:"paths"`
}

type IngressPath struct {
	Path     string         `yaml:"path,omitempty"`
	PathType string         `yaml:"pathType,omitempty"`
	Backend  IngressBackend `yaml:"backend"`
}

type IngressBackend struct {
	Service IngressServiceBackend `yaml:"service"`
}

type IngressServiceBackend struct {
	Name string             `yaml:"name"`
	Port IngressServicePort `yaml:"port"`
}

type IngressServicePort struct {
	Number int32  `yaml:"number,omitempty"`
	Name   string `yaml:"name,omitempty"`
}

// Manifests is the bundle of resources parsed out of an input directory.
type Manifests struct {
	Deployments  []Deployment
	StatefulSets []StatefulSet
	Services     []Service
	ConfigMaps   []ConfigMap
	Secrets      []Secret
	PVCs         []PersistentVolumeClaim
	Ingresses    []Ingress
}
