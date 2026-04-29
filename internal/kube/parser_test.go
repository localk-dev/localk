package kube

import (
	"strings"
	"testing"
)

// TestParseBytes_MultiDoc verifies that a stream of `---`-separated documents
// is parsed correctly.
func TestParseBytes_MultiDoc(t *testing.T) {
	data := strings.TrimSpace(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: cm-a
data:
  KEY: value
---
apiVersion: v1
kind: Service
metadata:
  name: svc-a
spec:
  selector:
    app: a
  ports:
  - port: 80
`)

	m, err := ParseBytes([]byte(data))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if len(m.ConfigMaps) != 1 || m.ConfigMaps[0].Metadata.Name != "cm-a" {
		t.Errorf("expected one ConfigMap named cm-a, got %+v", m.ConfigMaps)
	}
	if len(m.Services) != 1 || m.Services[0].Metadata.Name != "svc-a" {
		t.Errorf("expected one Service named svc-a, got %+v", m.Services)
	}
}

// TestParseBytes_ListWrapper verifies kubectl-style `kind: List` output is
// unwrapped and each item is dispatched as if it were a top-level document.
// This is the shape produced by `kubectl get deployment,service,... -o yaml`.
func TestParseBytes_ListWrapper(t *testing.T) {
	data := strings.TrimSpace(`
apiVersion: v1
kind: List
items:
- apiVersion: apps/v1
  kind: Deployment
  metadata:
    name: api
  spec:
    selector:
      matchLabels:
        app: api
    template:
      metadata:
        labels:
          app: api
      spec:
        containers:
        - name: api
          image: example/api:1.0
- apiVersion: v1
  kind: Service
  metadata:
    name: api
  spec:
    selector:
      app: api
    ports:
    - port: 80
- apiVersion: v1
  kind: ConfigMap
  metadata:
    name: api-config
  data:
    LOG_LEVEL: info
- apiVersion: v1
  kind: Secret
  metadata:
    name: api-secret
  stringData:
    DB_PASSWORD: hunter2
- apiVersion: v1
  kind: PersistentVolumeClaim
  metadata:
    name: data
  spec:
    storageClassName: standard
    accessModes: [ReadWriteOnce]
`)

	m, err := ParseBytes([]byte(data))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}

	if got, want := len(m.Deployments), 1; got != want {
		t.Errorf("Deployments: got %d, want %d", got, want)
	}
	if got, want := len(m.Services), 1; got != want {
		t.Errorf("Services: got %d, want %d", got, want)
	}
	if got, want := len(m.ConfigMaps), 1; got != want {
		t.Errorf("ConfigMaps: got %d, want %d", got, want)
	}
	if got, want := len(m.Secrets), 1; got != want {
		t.Errorf("Secrets: got %d, want %d", got, want)
	}
	if got, want := len(m.PVCs), 1; got != want {
		t.Errorf("PVCs: got %d, want %d", got, want)
	}

	// Spot-check that nested fields survived the round-trip through
	// re-marshal-then-dispatch.
	if got := m.Deployments[0].Spec.Template.Spec.Containers[0].Image; got != "example/api:1.0" {
		t.Errorf("Deployment image not preserved: got %q", got)
	}
	if got := m.Secrets[0].StringData["DB_PASSWORD"]; got != "hunter2" {
		t.Errorf("Secret stringData not preserved: got %q", got)
	}
}

// TestParseBytes_StatefulSet verifies StatefulSets land in the right slice
// and their volumeClaimTemplates survive parsing intact — that's the
// distinguishing feature of a StatefulSet vs a Deployment.
func TestParseBytes_StatefulSet(t *testing.T) {
	data := strings.TrimSpace(`
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: postgres
spec:
  serviceName: postgres
  replicas: 1
  selector:
    matchLabels:
      app: postgres
  template:
    metadata:
      labels:
        app: postgres
    spec:
      containers:
      - name: postgres
        image: postgres:16
        volumeMounts:
        - name: data
          mountPath: /var/lib/postgresql/data
  volumeClaimTemplates:
  - metadata:
      name: data
    spec:
      accessModes: [ReadWriteOnce]
      storageClassName: standard
`)
	m, err := ParseBytes([]byte(data))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if len(m.StatefulSets) != 1 {
		t.Fatalf("expected 1 StatefulSet, got %d", len(m.StatefulSets))
	}
	ss := m.StatefulSets[0]
	if ss.Metadata.Name != "postgres" {
		t.Errorf("name = %q, want postgres", ss.Metadata.Name)
	}
	if len(ss.Spec.VolumeClaimTemplates) != 1 {
		t.Fatalf("expected 1 VCT, got %d", len(ss.Spec.VolumeClaimTemplates))
	}
	if ss.Spec.VolumeClaimTemplates[0].Metadata.Name != "data" {
		t.Errorf("VCT name = %q, want data", ss.Spec.VolumeClaimTemplates[0].Metadata.Name)
	}
}

// TestParseBytes_UnknownKindIgnored verifies that resources we don't yet
// support don't cause an error — they're simply skipped. Important for
// kubectl input where the user's namespace may contain resources beyond what
// the converter handles.
func TestParseBytes_UnknownKindIgnored(t *testing.T) {
	data := strings.TrimSpace(`
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: web
spec:
  rules: []
---
apiVersion: v1
kind: Service
metadata:
  name: web
spec:
  selector:
    app: web
  ports:
  - port: 80
`)

	m, err := ParseBytes([]byte(data))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if len(m.Services) != 1 {
		t.Errorf("expected the Service to be parsed even when an unknown kind is present, got %d Services", len(m.Services))
	}
}

// TestParseBytes_EmptyDocsSkipped verifies that empty documents (e.g. trailing
// `---`) don't break parsing.
func TestParseBytes_EmptyDocsSkipped(t *testing.T) {
	data := `
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: only
---
`
	m, err := ParseBytes([]byte(data))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if len(m.ConfigMaps) != 1 {
		t.Fatalf("expected 1 ConfigMap, got %d", len(m.ConfigMaps))
	}
}
