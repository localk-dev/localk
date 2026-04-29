package convert_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/localk-dev/localk/internal/compose"
	"github.com/localk-dev/localk/internal/config"
	"github.com/localk-dev/localk/internal/convert"
	"github.com/localk-dev/localk/internal/kube"
)

// update is a flag that, when set, rewrites the golden files instead of
// asserting against them. Run `go test ./internal/convert -update` after an
// intentional change to the conversion logic.
var update = flag.Bool("update", false, "update golden files instead of asserting")

func TestConvert_SimpleExample(t *testing.T) {
	manifests, err := kube.ParseDir("../../examples/simple/k8s")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}

	result, err := convert.Convert(manifests, nil)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}

	if got, want := len(result.Compose.Services), 3; got != want {
		t.Errorf("expected %d services, got %d", want, got)
	}

	for _, name := range []string{"api", "worker", "postgres"} {
		if _, ok := result.Compose.Services[name]; !ok {
			t.Errorf("expected service %q in compose output", name)
		}
	}

	got, err := yaml.Marshal(result.Compose)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	goldenPath := filepath.Join("testdata", "simple.golden.yml")
	if *update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("updated %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("reading golden file %s: %v\nrun `go test ./internal/convert -update` to create it", goldenPath, err)
	}
	if string(got) != string(want) {
		t.Errorf("compose output drifted from golden file %s\n--- got ---\n%s\n--- want ---\n%s",
			goldenPath, got, want)
	}
}

func TestConvert_HostnamePreserved(t *testing.T) {
	// Regression: a Service-fronted Deployment should be named after the
	// Service so other services can reach it at the same hostname they use
	// in production.
	manifests, err := kube.ParseDir("../../examples/simple/k8s")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	result, err := convert.Convert(manifests, nil)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}

	worker, ok := result.Compose.Services["worker"]
	if !ok {
		t.Fatal("worker service missing")
	}
	if got := worker.Environment["API_URL"]; got != "http://api:3000" {
		t.Errorf("expected worker.API_URL to remain http://api:3000, got %q", got)
	}
}

func TestConvert_OverrideSkip(t *testing.T) {
	manifests, err := kube.ParseDir("../../examples/simple/k8s")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	cfg := &config.Config{Services: map[string]config.ServiceOverride{
		"worker": {Skip: true},
	}}
	result, err := convert.Convert(manifests, cfg)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if _, present := result.Compose.Services["worker"]; present {
		t.Error("expected worker to be skipped, but it appeared in the compose output")
	}
	if _, present := result.Compose.Services["api"]; !present {
		t.Error("api should still be present when only worker is skipped")
	}
}

func TestConvert_OverrideImage(t *testing.T) {
	manifests, err := kube.ParseDir("../../examples/simple/k8s")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	cfg := &config.Config{Services: map[string]config.ServiceOverride{
		"postgres": {Image: "postgres:15-alpine"},
	}}
	result, err := convert.Convert(manifests, cfg)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	pg := result.Compose.Services["postgres"]
	if pg.Image != "postgres:15-alpine" {
		t.Errorf("expected image override, got %q", pg.Image)
	}
	if pg.Build != nil {
		t.Errorf("Image override should leave Build nil, got %+v", pg.Build)
	}
}

func TestConvert_OverrideBuild(t *testing.T) {
	manifests, err := kube.ParseDir("../../examples/simple/k8s")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	cfg := &config.Config{Services: map[string]config.ServiceOverride{
		"api": {Build: &config.BuildOverride{Context: "./services/api", Dockerfile: "Dockerfile.dev"}},
	}}
	result, err := convert.Convert(manifests, cfg)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	api := result.Compose.Services["api"]
	if api.Image != "" {
		t.Errorf("Build override should clear Image, got %q", api.Image)
	}
	if api.Build == nil {
		t.Fatal("expected Build to be set")
	}
	if api.Build.Context != "./services/api" || api.Build.Dockerfile != "Dockerfile.dev" {
		t.Errorf("Build = %+v, want {./services/api, Dockerfile.dev}", api.Build)
	}
}

func TestConvert_OverrideUnknownServiceWarns(t *testing.T) {
	manifests, err := kube.ParseDir("../../examples/simple/k8s")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	cfg := &config.Config{Services: map[string]config.ServiceOverride{
		"nonexistent": {Skip: true},
	}}
	result, err := convert.Convert(manifests, cfg)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "nonexistent") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning about unmatched override, got warnings: %v", result.Warnings)
	}
}

// TestConvert_StatefulSet verifies a StatefulSet is converted like a
// Deployment AND that its volumeClaimTemplates materialize as top-level
// named compose volumes mounted into the service. This is the whole point
// of supporting StatefulSets — their persistent storage shape is what
// makes them different from Deployments.
func TestConvert_StatefulSet(t *testing.T) {
	manifests := &kube.Manifests{
		StatefulSets: []kube.StatefulSet{{
			Metadata: kube.ObjectMeta{Name: "postgres"},
			Spec: kube.StatefulSetSpec{
				ServiceName: "postgres",
				Selector:    kube.LabelSelect{MatchLabels: map[string]string{"app": "postgres"}},
				Template: kube.PodTemplate{
					Metadata: kube.ObjectMeta{Labels: map[string]string{"app": "postgres"}},
					Spec: kube.PodSpec{
						Containers: []kube.Container{{
							Name:  "postgres",
							Image: "postgres:16",
							Ports: []kube.ContainerPort{{ContainerPort: 5432}},
							VolumeMounts: []kube.VolumeMount{{
								Name:      "data",
								MountPath: "/var/lib/postgresql/data",
							}},
						}},
					},
				},
				VolumeClaimTemplates: []kube.PersistentVolumeClaimTemplate{{
					Metadata: kube.ObjectMeta{Name: "data"},
				}},
			},
		}},
		Services: []kube.Service{{
			Metadata: kube.ObjectMeta{Name: "postgres"},
			Spec: kube.ServiceSpec{
				Selector: map[string]string{"app": "postgres"},
				Ports:    []kube.ServicePort{{Port: 5432}},
			},
		}},
	}

	result, err := convert.Convert(manifests, nil)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}

	pg, ok := result.Compose.Services["postgres"]
	if !ok {
		t.Fatal("expected postgres service in compose output")
	}
	if pg.Image != "postgres:16" {
		t.Errorf("image = %q, want postgres:16", pg.Image)
	}
	wantMount := "postgres-data:/var/lib/postgresql/data"
	if len(pg.Volumes) != 1 || pg.Volumes[0] != wantMount {
		t.Errorf("volumes = %v, want [%s]", pg.Volumes, wantMount)
	}
	if _, ok := result.Compose.Volumes["postgres-data"]; !ok {
		t.Errorf("expected top-level volume %q to be declared, got %v", "postgres-data", result.Compose.Volumes)
	}
}

// TestConvert_Ingress_PathRouting exercises the realistic mp-production
// shape: one host, multiple Prefix paths, each routing to a different
// backend service. We assert (a) a proxy service is emitted, (b) the
// Caddyfile has handle_path entries for each prefix, (c) all backends
// have their host-port publishing stripped so they don't collide with
// the proxy on host:80.
func TestConvert_Ingress_PathRouting(t *testing.T) {
	manifests := &kube.Manifests{
		Deployments: []kube.Deployment{
			deploymentWithPort("ui-admin", 80, map[string]string{"app": "ui-admin"}),
			deploymentWithPort("ui-consent", 80, map[string]string{"app": "ui-consent"}),
			deploymentWithPort("api", 80, map[string]string{"app": "api"}),
			deploymentWithPort("postgres", 5432, map[string]string{"app": "postgres"}), // not behind ingress
		},
		Services: []kube.Service{
			serviceFor("ui-admin", 80, map[string]string{"app": "ui-admin"}),
			serviceFor("ui-consent", 80, map[string]string{"app": "ui-consent"}),
			serviceFor("api", 80, map[string]string{"app": "api"}),
			serviceFor("postgres", 5432, map[string]string{"app": "postgres"}),
		},
		Ingresses: []kube.Ingress{{
			Metadata: kube.ObjectMeta{Name: "web"},
			Spec: kube.IngressSpec{Rules: []kube.IngressRule{{
				Host: "example.com",
				HTTP: &kube.IngressRuleHTTP{Paths: []kube.IngressPath{
					{Path: "/admin", PathType: "Prefix", Backend: backendOn("ui-admin", 80)},
					{Path: "/consent", PathType: "Prefix", Backend: backendOn("ui-consent", 80)},
					{Path: "/api", PathType: "Prefix", Backend: backendOn("api", 80)},
				}},
			}}},
		}},
	}

	result, err := convert.Convert(manifests, nil)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}

	// (a) Proxy service emitted.
	proxy, ok := result.Compose.Services["proxy"]
	if !ok {
		t.Fatal("expected proxy service in compose output")
	}
	if proxy.Image != "caddy:2-alpine" {
		t.Errorf("proxy image = %q, want caddy:2-alpine", proxy.Image)
	}
	if len(proxy.Ports) != 1 || proxy.Ports[0] != "80:80" {
		t.Errorf("proxy ports = %v, want [80:80]", proxy.Ports)
	}

	// (b) Caddyfile has the right shape.
	cf := result.CaddyFile
	if !strings.Contains(cf, "example.localhost {") {
		t.Errorf("Caddyfile missing host block:\n%s", cf)
	}
	for _, want := range []string{
		"handle_path /admin/* {",
		"reverse_proxy ui-admin:80",
		"handle_path /consent/* {",
		"reverse_proxy ui-consent:80",
		"handle_path /api/* {",
		"reverse_proxy api:80",
	} {
		if !strings.Contains(cf, want) {
			t.Errorf("Caddyfile missing %q:\n%s", want, cf)
		}
	}

	// (c) Backend ports stripped so they don't collide with the proxy.
	for _, name := range []string{"ui-admin", "ui-consent", "api"} {
		s := result.Compose.Services[name]
		if len(s.Ports) != 0 {
			t.Errorf("backend %q should have no host ports after ingress strip, got %v", name, s.Ports)
		}
	}

	// (d) Non-routed services keep their ports.
	pg := result.Compose.Services["postgres"]
	if len(pg.Ports) == 0 {
		t.Error("postgres is not behind ingress; its host port should NOT be stripped")
	}
}

// TestConvert_Ingress_MissingBackendWarns verifies that an Ingress rule
// pointing at a service that doesn't exist (typo, missing manifest) emits
// a warning and is skipped, but other rules still produce output.
func TestConvert_Ingress_MissingBackendWarns(t *testing.T) {
	manifests := &kube.Manifests{
		Deployments: []kube.Deployment{
			deploymentWithPort("api", 80, map[string]string{"app": "api"}),
		},
		Services: []kube.Service{
			serviceFor("api", 80, map[string]string{"app": "api"}),
		},
		Ingresses: []kube.Ingress{{
			Metadata: kube.ObjectMeta{Name: "web"},
			Spec: kube.IngressSpec{Rules: []kube.IngressRule{{
				Host: "example.com",
				HTTP: &kube.IngressRuleHTTP{Paths: []kube.IngressPath{
					{Path: "/api", PathType: "Prefix", Backend: backendOn("api", 80)},
					{Path: "/ghost", PathType: "Prefix", Backend: backendOn("nonexistent", 80)},
				}},
			}}},
		}},
	}

	result, err := convert.Convert(manifests, nil)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}

	// The good rule still emits a Caddy entry.
	if !strings.Contains(result.CaddyFile, "reverse_proxy api:80") {
		t.Errorf("expected api route to survive missing-backend warning:\n%s", result.CaddyFile)
	}
	// The bad rule is skipped — no nonexistent backend in the Caddyfile.
	if strings.Contains(result.CaddyFile, "nonexistent") {
		t.Errorf("missing-backend rule should not appear in Caddyfile:\n%s", result.CaddyFile)
	}
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "nonexistent") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a warning about the missing backend, got %v", result.Warnings)
	}
}

// TestConvert_NoIngress_NoProxy is the regression guard: when there are no
// Ingresses, no proxy service is emitted and CaddyFile is empty. We don't
// want to surprise users with a proxy container that doesn't do anything.
func TestConvert_NoIngress_NoProxy(t *testing.T) {
	manifests, err := kube.ParseDir("../../examples/simple/k8s")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	result, err := convert.Convert(manifests, nil)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if _, present := result.Compose.Services["proxy"]; present {
		t.Error("proxy service should not be emitted when there are no Ingresses")
	}
	if result.CaddyFile != "" {
		t.Errorf("CaddyFile should be empty when there are no Ingresses, got %q", result.CaddyFile)
	}
}

// TestConvert_Ingress_LocalHostMapping verifies our hostname-rewrite rule:
// replace the last domain segment with `localhost`. This preserves the
// subdomain hierarchy that distinguishes services in prod (api.foo.eu vs
// seq.foo.eu) while ensuring everything resolves to 127.0.0.1 locally
// without /etc/hosts edits.
func TestConvert_Ingress_LocalHostMapping(t *testing.T) {
	cases := []struct {
		prod, want string
	}{
		{"example.com", "example.localhost"},
		{"api.example.com", "api.example.localhost"},
		{"seq.example.com", "seq.example.localhost"},
		{"single", "single.localhost"},
		{"already.localhost", "already.localhost"},
	}
	for _, tc := range cases {
		t.Run(tc.prod, func(t *testing.T) {
			manifests := &kube.Manifests{
				Deployments: []kube.Deployment{
					deploymentWithPort("api", 80, map[string]string{"app": "api"}),
				},
				Services: []kube.Service{
					serviceFor("api", 80, map[string]string{"app": "api"}),
				},
				Ingresses: []kube.Ingress{{
					Metadata: kube.ObjectMeta{Name: "x"},
					Spec: kube.IngressSpec{Rules: []kube.IngressRule{{
						Host: tc.prod,
						HTTP: &kube.IngressRuleHTTP{Paths: []kube.IngressPath{
							{Path: "/", Backend: backendOn("api", 80)},
						}},
					}}},
				}},
			}
			result, err := convert.Convert(manifests, nil)
			if err != nil {
				t.Fatalf("Convert: %v", err)
			}
			if !strings.Contains(result.CaddyFile, tc.want+" {") {
				t.Errorf("expected host block for %q in Caddyfile, got:\n%s", tc.want, result.CaddyFile)
			}
		})
	}
}

// TestConvert_DownwardAPIExpansion is the end-to-end version of the
// downward.go unit tests. It mirrors the real-world Bitnami MongoDB
// shape: declare POD_NAME via fieldRef, then reference $(POD_NAME) in
// MONGODB_ADVERTISED_HOSTNAME. Before this fix, the fieldRef env var
// was silently dropped AND the $(POD_NAME) reference was left literal,
// breaking replica advertisement locally.
func TestConvert_DownwardAPIExpansion(t *testing.T) {
	manifests := &kube.Manifests{
		Deployments: []kube.Deployment{{
			Metadata: kube.ObjectMeta{Name: "mongodb"},
			Spec: kube.DeploymentSpec{
				Selector: kube.LabelSelect{MatchLabels: map[string]string{"app": "mongodb"}},
				Template: kube.PodTemplate{
					Metadata: kube.ObjectMeta{Labels: map[string]string{"app": "mongodb"}},
					Spec: kube.PodSpec{
						Containers: []kube.Container{{
							Name:  "mongodb",
							Image: "bitnami/mongodb:latest",
							Env: []kube.EnvVar{
								{Name: "MY_POD_NAME", ValueFrom: &kube.EnvVarSource{FieldRef: &kube.ObjectFieldRef{FieldPath: "metadata.name"}}},
								{Name: "MY_POD_NAMESPACE", ValueFrom: &kube.EnvVarSource{FieldRef: &kube.ObjectFieldRef{FieldPath: "metadata.namespace"}}},
								{Name: "MONGODB_ADVERTISED_HOSTNAME", Value: "$(MY_POD_NAME).mongodb-headless.$(MY_POD_NAMESPACE).svc.cluster.local"},
							},
						}},
					},
				},
			},
		}},
	}

	result, err := convert.Convert(manifests, nil)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	mongo := result.Compose.Services["mongodb"]

	if got := mongo.Environment["MY_POD_NAME"]; got != "mongodb" {
		t.Errorf("MY_POD_NAME = %q, want mongodb (was likely dropped before this fix)", got)
	}
	if got := mongo.Environment["MY_POD_NAMESPACE"]; got != "default" {
		t.Errorf("MY_POD_NAMESPACE = %q, want default", got)
	}
	want := "mongodb.mongodb-headless.default.svc.cluster.local"
	if got := mongo.Environment["MONGODB_ADVERTISED_HOSTNAME"]; got != want {
		t.Errorf("MONGODB_ADVERTISED_HOSTNAME not expanded:\n got  %q\n want %q", got, want)
	}
}

// TestConvert_DownwardAPIUnknownPathWarns verifies that fieldPaths we
// don't know how to map produce a clear warning rather than silently
// dropping the env var.
func TestConvert_DownwardAPIUnknownPathWarns(t *testing.T) {
	manifests := &kube.Manifests{
		Deployments: []kube.Deployment{{
			Metadata: kube.ObjectMeta{Name: "app"},
			Spec: kube.DeploymentSpec{
				Selector: kube.LabelSelect{MatchLabels: map[string]string{"app": "app"}},
				Template: kube.PodTemplate{
					Metadata: kube.ObjectMeta{Labels: map[string]string{"app": "app"}},
					Spec: kube.PodSpec{
						Containers: []kube.Container{{
							Name:  "app",
							Image: "app:latest",
							Env: []kube.EnvVar{
								{Name: "WEIRD", ValueFrom: &kube.EnvVarSource{FieldRef: &kube.ObjectFieldRef{FieldPath: "metadata.labels['team']"}}},
							},
						}},
					},
				},
			},
		}},
	}
	result, err := convert.Convert(manifests, nil)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if _, present := result.Compose.Services["app"].Environment["WEIRD"]; present {
		t.Error("unsupported fieldPath should leave the env var unset, not silently fabricate")
	}
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "metadata.labels['team']") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning about unsupported fieldPath, got %v", result.Warnings)
	}
}

// TestConvert_Sidecar_NetworkSharing covers the most common pattern:
// a main app container plus a metrics-exporter sidecar that scrapes the
// main on localhost. Asserts (a) both compose services exist with the
// right names, (b) the sidecar uses network_mode: service:<main> so
// localhost still works between them, (c) the sidecar has no Ports of
// its own (compose forbids it when sharing another service's network),
// (d) the main keeps its Service-derived ports.
func TestConvert_Sidecar_NetworkSharing(t *testing.T) {
	manifests := &kube.Manifests{
		Deployments: []kube.Deployment{{
			Metadata: kube.ObjectMeta{Name: "app"},
			Spec: kube.DeploymentSpec{
				Selector: kube.LabelSelect{MatchLabels: map[string]string{"app": "app"}},
				Template: kube.PodTemplate{
					Metadata: kube.ObjectMeta{Labels: map[string]string{"app": "app"}},
					Spec: kube.PodSpec{
						Containers: []kube.Container{
							{
								Name:  "app",
								Image: "example/app:1.0",
								Ports: []kube.ContainerPort{{ContainerPort: 8080}},
							},
							{
								Name:  "metrics",
								Image: "example/exporter:1.0",
								Ports: []kube.ContainerPort{{ContainerPort: 9090}},
							},
						},
					},
				},
			},
		}},
		Services: []kube.Service{{
			Metadata: kube.ObjectMeta{Name: "app"},
			Spec: kube.ServiceSpec{
				Selector: map[string]string{"app": "app"},
				Ports:    []kube.ServicePort{{Port: 8080}},
			},
		}},
	}
	result, err := convert.Convert(manifests, nil)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}

	main, ok := result.Compose.Services["app"]
	if !ok {
		t.Fatal("expected main service 'app'")
	}
	if main.NetworkMode != "" {
		t.Errorf("main.NetworkMode = %q, want empty", main.NetworkMode)
	}
	if len(main.Ports) != 1 || main.Ports[0] != "8080:8080" {
		t.Errorf("main.Ports = %v, want [8080:8080]", main.Ports)
	}

	sidecar, ok := result.Compose.Services["app-metrics"]
	if !ok {
		t.Fatalf("expected sidecar 'app-metrics' in compose services, got %v", keys(result.Compose.Services))
	}
	if sidecar.NetworkMode != "service:app" {
		t.Errorf("sidecar.NetworkMode = %q, want service:app", sidecar.NetworkMode)
	}
	if len(sidecar.Ports) != 0 {
		t.Errorf("sidecar must not have Ports when sharing another service's network; got %v", sidecar.Ports)
	}
	if sidecar.Image != "example/exporter:1.0" {
		t.Errorf("sidecar.Image = %q, want example/exporter:1.0", sidecar.Image)
	}
}

// TestConvert_Sidecar_SharedEmptyDirPromoted exercises the classic
// pattern: main writes logs into an emptyDir, sidecar tails them. The
// emptyDir name is referenced by both containers' volumeMounts, so we
// must promote it to a named compose volume — otherwise each compose
// service gets its own anonymous mount and the sharing silently breaks.
func TestConvert_Sidecar_SharedEmptyDirPromoted(t *testing.T) {
	manifests := &kube.Manifests{
		Deployments: []kube.Deployment{{
			Metadata: kube.ObjectMeta{Name: "app"},
			Spec: kube.DeploymentSpec{
				Selector: kube.LabelSelect{MatchLabels: map[string]string{"app": "app"}},
				Template: kube.PodTemplate{
					Metadata: kube.ObjectMeta{Labels: map[string]string{"app": "app"}},
					Spec: kube.PodSpec{
						Volumes: []kube.Volume{
							{Name: "logs", EmptyDir: &kube.EmptyDirVol{}},
							{Name: "scratch", EmptyDir: &kube.EmptyDirVol{}}, // not shared
						},
						Containers: []kube.Container{
							{
								Name:  "app",
								Image: "example/app:1.0",
								VolumeMounts: []kube.VolumeMount{
									{Name: "logs", MountPath: "/var/log/app"},
									{Name: "scratch", MountPath: "/tmp"},
								},
							},
							{
								Name:  "logger",
								Image: "example/log-shipper:1.0",
								VolumeMounts: []kube.VolumeMount{
									{Name: "logs", MountPath: "/logs"},
								},
							},
						},
					},
				},
			},
		}},
	}
	result, err := convert.Convert(manifests, nil)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}

	// (a) Shared emptyDir promoted to a named volume on both services.
	wantNamed := "app-logs:" // name is <workload>-<volume.name>
	mainVols := result.Compose.Services["app"].Volumes
	sidecarVols := result.Compose.Services["app-logger"].Volumes
	mainHasShared := false
	for _, v := range mainVols {
		if strings.HasPrefix(v, wantNamed) {
			mainHasShared = true
			break
		}
	}
	sideHasShared := false
	for _, v := range sidecarVols {
		if strings.HasPrefix(v, wantNamed) {
			sideHasShared = true
			break
		}
	}
	if !mainHasShared || !sideHasShared {
		t.Errorf("shared emptyDir 'logs' should appear as named volume %q in both services\n  main: %v\n  sidecar: %v", wantNamed, mainVols, sidecarVols)
	}

	// (b) Top-level volume declaration exists.
	if _, present := result.Compose.Volumes["app-logs"]; !present {
		t.Errorf("expected top-level volume 'app-logs', got %v", result.Compose.Volumes)
	}

	// (c) Non-shared 'scratch' stays an anonymous mount on the main only.
	hasAnonScratch := false
	for _, v := range mainVols {
		if v == "/tmp" {
			hasAnonScratch = true
			break
		}
	}
	if !hasAnonScratch {
		t.Errorf("non-shared emptyDir 'scratch' should remain an anonymous mount; main vols: %v", mainVols)
	}
	if _, present := result.Compose.Volumes["app-scratch"]; present {
		t.Error("non-shared emptyDir should NOT be promoted to a named volume")
	}
}

// TestConvert_NoSidecarsForSingleContainer is the regression guard:
// pods with one container shouldn't gain any sidecar services or
// network_mode tweaks.
func TestConvert_NoSidecarsForSingleContainer(t *testing.T) {
	manifests, err := kube.ParseDir("../../examples/simple/k8s")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	result, err := convert.Convert(manifests, nil)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	for name, s := range result.Compose.Services {
		if s.NetworkMode != "" {
			t.Errorf("service %q has NetworkMode = %q in single-container input; should be empty", name, s.NetworkMode)
		}
		if strings.Contains(name, "-") && (name != "postgres" && name != "api" && name != "worker") {
			// Whitelist the example's actual service names — anything
			// else with a hyphen would suggest an unwanted sidecar.
			t.Errorf("unexpected service %q in single-container example output", name)
		}
	}
}

func keys(m map[string]compose.Service) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Test helpers — kept here so they're easy to find next to the tests
// that use them.

func deploymentWithPort(name string, port int32, labels map[string]string) kube.Deployment {
	return kube.Deployment{
		Metadata: kube.ObjectMeta{Name: name},
		Spec: kube.DeploymentSpec{
			Selector: kube.LabelSelect{MatchLabels: labels},
			Template: kube.PodTemplate{
				Metadata: kube.ObjectMeta{Labels: labels},
				Spec: kube.PodSpec{
					Containers: []kube.Container{{
						Name:  name,
						Image: name + ":latest",
						Ports: []kube.ContainerPort{{ContainerPort: port}},
					}},
				},
			},
		},
	}
}

func serviceFor(name string, port int32, selector map[string]string) kube.Service {
	return kube.Service{
		Metadata: kube.ObjectMeta{Name: name},
		Spec: kube.ServiceSpec{
			Selector: selector,
			Ports:    []kube.ServicePort{{Port: port}},
		},
	}
}

func backendOn(name string, port int32) kube.IngressBackend {
	return kube.IngressBackend{
		Service: kube.IngressServiceBackend{
			Name: name,
			Port: kube.IngressServicePort{Number: port},
		},
	}
}

// TestConvert_StatefulSetSkipOverride verifies localk.yaml overrides apply
// to StatefulSets the same way they do to Deployments.
func TestConvert_StatefulSetSkipOverride(t *testing.T) {
	manifests := &kube.Manifests{
		StatefulSets: []kube.StatefulSet{{
			Metadata: kube.ObjectMeta{Name: "redis"},
			Spec: kube.StatefulSetSpec{
				Template: kube.PodTemplate{
					Spec: kube.PodSpec{
						Containers: []kube.Container{{Name: "redis", Image: "redis:7"}},
					},
				},
			},
		}},
	}
	cfg := &config.Config{Services: map[string]config.ServiceOverride{
		"redis": {Skip: true},
	}}
	result, err := convert.Convert(manifests, cfg)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if _, present := result.Compose.Services["redis"]; present {
		t.Error("expected redis (StatefulSet) to be skipped via localk.yaml")
	}
}
