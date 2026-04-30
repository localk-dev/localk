package convert_test

import (
	"flag"
	"fmt"
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

// TestConvert_HostPortConflictsDropped covers the case where multiple
// services in the input declare the same host port. Real-world hits:
// k8s manifests where 30 services all serve container :80, plus a
// Caddy proxy from the Ingress feature also wanting :80. compose
// would crash on the second bind; we resolve by dropping the
// conflicts (proxy keeps it; first sorted name otherwise) and
// surfacing a warning naming each affected service.
func TestConvert_HostPortConflictsDropped(t *testing.T) {
	manifests := &kube.Manifests{
		Deployments: []kube.Deployment{
			deploymentWithPort("api", 80, map[string]string{"app": "api"}),
			deploymentWithPort("worker", 80, map[string]string{"app": "worker"}),
			deploymentWithPort("data-svc", 80, map[string]string{"app": "data-svc"}),
		},
		Services: []kube.Service{
			serviceFor("api", 80, map[string]string{"app": "api"}),
			serviceFor("worker", 80, map[string]string{"app": "worker"}),
			serviceFor("data-svc", 80, map[string]string{"app": "data-svc"}),
		},
		// One Ingress so a Caddy proxy gets emitted on :80. `api` is
		// behind it; worker and data-svc are not.
		Ingresses: []kube.Ingress{{
			Metadata: kube.ObjectMeta{Name: "web"},
			Spec: kube.IngressSpec{Rules: []kube.IngressRule{{
				Host: "app.example.com",
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

	// proxy keeps its 80:80 mapping (claimed first).
	proxy := result.Compose.Services["proxy"]
	if len(proxy.Ports) != 1 || proxy.Ports[0] != "80:80" {
		t.Errorf("proxy.Ports = %v, want [80:80]", proxy.Ports)
	}

	// api (Ingress backend) had its host port stripped already.
	api := result.Compose.Services["api"]
	if len(api.Ports) != 0 {
		t.Errorf("api as Ingress backend should have no Ports; got %v", api.Ports)
	}

	// worker and data-svc — not behind Ingress, both wanted 80:80;
	// the conflict resolver should have dropped both since proxy
	// already claimed :80.
	for _, name := range []string{"worker", "data-svc"} {
		s := result.Compose.Services[name]
		if len(s.Ports) != 0 {
			t.Errorf("service %q should have its conflicting :80 mapping dropped; got %v", name, s.Ports)
		}
	}

	// Two services lost their bindings → two warnings naming them.
	for _, name := range []string{"worker", "data-svc"} {
		found := false
		for _, w := range result.Warnings {
			if strings.Contains(w, fmt.Sprintf("%q", name)) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected a warning naming %q; got %v", name, result.Warnings)
		}
	}
}

// TestConvert_HostPortFirstClaimWins handles the no-Ingress case:
// two unrelated services both declaring host:80. Without a proxy to
// prioritize, the first service (alphabetical) keeps the port and
// the others lose theirs — same start-the-stack-or-don't trade-off,
// just with a different winner.
func TestConvert_HostPortFirstClaimWins(t *testing.T) {
	manifests := &kube.Manifests{
		Deployments: []kube.Deployment{
			deploymentWithPort("zebra", 80, map[string]string{"app": "zebra"}),
			deploymentWithPort("alpha", 80, map[string]string{"app": "alpha"}),
		},
		Services: []kube.Service{
			serviceFor("zebra", 80, map[string]string{"app": "zebra"}),
			serviceFor("alpha", 80, map[string]string{"app": "alpha"}),
		},
	}
	result, err := convert.Convert(manifests, nil)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if got := result.Compose.Services["alpha"].Ports; len(got) != 1 || got[0] != "80:80" {
		t.Errorf("alpha (sorted first) should keep its 80:80; got %v", got)
	}
	if got := result.Compose.Services["zebra"].Ports; len(got) != 0 {
		t.Errorf("zebra should have its conflict dropped; got %v", got)
	}
}

// TestConvert_Ingress_FiltersCertManagerSolvers verifies cert-manager's
// ephemeral HTTP-01 solver Ingresses are dropped from the generated
// proxy without a noisy warning. These exist for ~1 minute during cert
// issuance/renewal and reference Services that don't outlive them — so
// in any cluster with cert-manager you'd otherwise get useless warnings
// every time you ran localk during a renewal cycle.
//
// Three independent recognition signals so the filter survives
// cert-manager rename / label drift: annotation, name prefix, and path
// prefix. Each one alone should be enough to skip the rule.
func TestConvert_Ingress_FiltersCertManagerSolvers(t *testing.T) {
	manifests := &kube.Manifests{
		Deployments: []kube.Deployment{
			deploymentWithPort("api", 80, map[string]string{"app": "api"}),
		},
		Services: []kube.Service{
			serviceFor("api", 80, map[string]string{"app": "api"}),
		},
		Ingresses: []kube.Ingress{
			// Real ingress — should make it through.
			{
				Metadata: kube.ObjectMeta{Name: "web"},
				Spec: kube.IngressSpec{Rules: []kube.IngressRule{{
					Host: "app.example.com",
					HTTP: &kube.IngressRuleHTTP{Paths: []kube.IngressPath{
						{Path: "/", Backend: backendOn("api", 80)},
					}},
				}}},
			},
			// Solver via annotation — skip.
			{
				Metadata: kube.ObjectMeta{
					Name:        "real-name-with-annotation",
					Annotations: map[string]string{"acme.cert-manager.io/http01-solver": "true"},
				},
				Spec: kube.IngressSpec{Rules: []kube.IngressRule{{
					Host: "app.example.com",
					HTTP: &kube.IngressRuleHTTP{Paths: []kube.IngressPath{
						{Path: "/whatever", Backend: backendOn("does-not-exist", 80)},
					}},
				}}},
			},
			// Solver via cm-acme-http-solver- name prefix — skip.
			{
				Metadata: kube.ObjectMeta{Name: "cm-acme-http-solver-abc12"},
				Spec: kube.IngressSpec{Rules: []kube.IngressRule{{
					Host: "app.example.com",
					HTTP: &kube.IngressRuleHTTP{Paths: []kube.IngressPath{
						{Path: "/.well-known/acme-challenge/x", Backend: backendOn("missing", 80)},
					}},
				}}},
			},
			// Solver via path prefix — skip even if name and labels look
			// innocuous.
			{
				Metadata: kube.ObjectMeta{Name: "weirdly-named"},
				Spec: kube.IngressSpec{Rules: []kube.IngressRule{{
					Host: "app.example.com",
					HTTP: &kube.IngressRuleHTTP{Paths: []kube.IngressPath{
						{Path: "/.well-known/acme-challenge/token", Backend: backendOn("nope", 80)},
					}},
				}}},
			},
		},
	}
	result, err := convert.Convert(manifests, nil)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}

	// Real ingress survives.
	if !strings.Contains(result.CaddyFile, "reverse_proxy api:80") {
		t.Errorf("real ingress should have produced a route; Caddyfile:\n%s", result.CaddyFile)
	}
	// None of the solver-targeted backend names leaked in.
	for _, leaked := range []string{"does-not-exist", "missing", "nope"} {
		if strings.Contains(result.CaddyFile, leaked) {
			t.Errorf("solver backend %q leaked into Caddyfile:\n%s", leaked, result.CaddyFile)
		}
	}
	// And no noisy "missing backend" warning for those skipped solvers —
	// the whole point of the filter is to avoid the noise.
	for _, w := range result.Warnings {
		if strings.Contains(w, "does-not-exist") || strings.Contains(w, "missing") || strings.Contains(w, "nope") {
			t.Errorf("solver should be filtered silently; got warning: %s", w)
		}
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
		if strings.HasPrefix(mountStr(v), wantNamed) {
			mainHasShared = true
			break
		}
	}
	sideHasShared := false
	for _, v := range sidecarVols {
		if strings.HasPrefix(mountStr(v), wantNamed) {
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

// TestConvert_InitContainer_SingleChain covers the simplest init pattern:
// one init container runs migrations, then the main app starts. Asserts
// (a) init becomes its own compose service with restart: "no", (b) main
// has depends_on the init with service_completed_successfully, (c) init
// has no depends_on of its own (it runs first).
func TestConvert_InitContainer_SingleChain(t *testing.T) {
	manifests := &kube.Manifests{
		Deployments: []kube.Deployment{{
			Metadata: kube.ObjectMeta{Name: "app"},
			Spec: kube.DeploymentSpec{
				Selector: kube.LabelSelect{MatchLabels: map[string]string{"app": "app"}},
				Template: kube.PodTemplate{
					Metadata: kube.ObjectMeta{Labels: map[string]string{"app": "app"}},
					Spec: kube.PodSpec{
						InitContainers: []kube.Container{{
							Name:    "migrate",
							Image:   "example/migrate:1.0",
							Command: []string{"/bin/migrate", "--up"},
						}},
						Containers: []kube.Container{{
							Name:  "app",
							Image: "example/app:1.0",
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

	init, ok := result.Compose.Services["app-migrate"]
	if !ok {
		t.Fatalf("expected init service 'app-migrate', got %v", keys(result.Compose.Services))
	}
	if init.Restart != "no" {
		t.Errorf("init.Restart = %q, want \"no\"", init.Restart)
	}
	if init.DependsOn != nil {
		t.Errorf("first init shouldn't have depends_on, got %v", init.DependsOn)
	}

	main := result.Compose.Services["app"]
	dep, ok := main.DependsOn["app-migrate"]
	if !ok {
		t.Fatalf("main.DependsOn should reference app-migrate, got %v", main.DependsOn)
	}
	if dep.Condition != "service_completed_successfully" {
		t.Errorf("main.DependsOn[app-migrate].Condition = %q, want service_completed_successfully", dep.Condition)
	}
}

// TestConvert_InitContainer_OrderedChain covers multiple init containers:
// they must run in declaration order, so each (after the first) has a
// depends_on the previous, and main depends on the LAST init.
func TestConvert_InitContainer_OrderedChain(t *testing.T) {
	manifests := &kube.Manifests{
		Deployments: []kube.Deployment{{
			Metadata: kube.ObjectMeta{Name: "app"},
			Spec: kube.DeploymentSpec{
				Selector: kube.LabelSelect{MatchLabels: map[string]string{"app": "app"}},
				Template: kube.PodTemplate{
					Metadata: kube.ObjectMeta{Labels: map[string]string{"app": "app"}},
					Spec: kube.PodSpec{
						InitContainers: []kube.Container{
							{Name: "wait-db", Image: "busybox:latest"},
							{Name: "migrate", Image: "example/migrate:1.0"},
							{Name: "seed", Image: "example/seed:1.0"},
						},
						Containers: []kube.Container{{
							Name:  "app",
							Image: "example/app:1.0",
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

	// First init has no depends_on.
	if got := result.Compose.Services["app-wait-db"].DependsOn; got != nil {
		t.Errorf("app-wait-db.DependsOn = %v, want nil", got)
	}
	// Middle init depends on first.
	migrate := result.Compose.Services["app-migrate"]
	if dep, ok := migrate.DependsOn["app-wait-db"]; !ok || dep.Condition != "service_completed_successfully" {
		t.Errorf("app-migrate should depend on app-wait-db with service_completed_successfully; got %v", migrate.DependsOn)
	}
	// Last init depends on middle.
	seed := result.Compose.Services["app-seed"]
	if dep, ok := seed.DependsOn["app-migrate"]; !ok || dep.Condition != "service_completed_successfully" {
		t.Errorf("app-seed should depend on app-migrate with service_completed_successfully; got %v", seed.DependsOn)
	}
	// Main depends on the LAST init only — not on every init.
	main := result.Compose.Services["app"]
	if len(main.DependsOn) != 1 {
		t.Errorf("main should depend on exactly one service (the last init); got %v", main.DependsOn)
	}
	if dep, ok := main.DependsOn["app-seed"]; !ok || dep.Condition != "service_completed_successfully" {
		t.Errorf("main.DependsOn[app-seed] = %+v, want completed-successfully", dep)
	}
}

// TestConvert_InitContainer_SharedVolume verifies the typical pattern
// where init writes config files into an emptyDir that the main reads.
// The shared emptyDir must be promoted to a named volume so init's
// writes survive into the main service's mount.
func TestConvert_InitContainer_SharedVolume(t *testing.T) {
	manifests := &kube.Manifests{
		Deployments: []kube.Deployment{{
			Metadata: kube.ObjectMeta{Name: "app"},
			Spec: kube.DeploymentSpec{
				Selector: kube.LabelSelect{MatchLabels: map[string]string{"app": "app"}},
				Template: kube.PodTemplate{
					Metadata: kube.ObjectMeta{Labels: map[string]string{"app": "app"}},
					Spec: kube.PodSpec{
						Volumes: []kube.Volume{
							{Name: "config", EmptyDir: &kube.EmptyDirVol{}},
						},
						InitContainers: []kube.Container{{
							Name:  "config-gen",
							Image: "example/config-gen:1.0",
							VolumeMounts: []kube.VolumeMount{
								{Name: "config", MountPath: "/out"},
							},
						}},
						Containers: []kube.Container{{
							Name:  "app",
							Image: "example/app:1.0",
							VolumeMounts: []kube.VolumeMount{
								{Name: "config", MountPath: "/etc/app"},
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

	if _, ok := result.Compose.Volumes["app-config"]; !ok {
		t.Errorf("expected promoted named volume 'app-config' (init+main share it), got %v", result.Compose.Volumes)
	}
	initVols := result.Compose.Services["app-config-gen"].Volumes
	mainVols := result.Compose.Services["app"].Volumes
	wantInitMount := "app-config:/out"
	wantMainMount := "app-config:/etc/app"
	hasInit := false
	for _, v := range initVols {
		if v == wantInitMount {
			hasInit = true
		}
	}
	hasMain := false
	for _, v := range mainVols {
		if v == wantMainMount {
			hasMain = true
		}
	}
	if !hasInit || !hasMain {
		t.Errorf("shared volume should appear with the right paths in both services\n  init wants %q in %v\n  main wants %q in %v",
			wantInitMount, initVols, wantMainMount, mainVols)
	}
}

// TestConvert_EmptyDir_MultiMountStaysAnonymous covers the Bitnami
// pattern: one container mounts a single emptyDir at multiple paths
// as separate scratch directories. Promoting it to a named compose
// volume would mean every path shows the same data — which broke
// rabbitmq's setup script (it complained that
// .../var/lib/rabbitmq/.erlang.cookie and .../.rabbitmq/.erlang.cookie
// resolved to the same file). Each mount must stay anonymous.
func TestConvert_EmptyDir_MultiMountStaysAnonymous(t *testing.T) {
	manifests := &kube.Manifests{
		Deployments: []kube.Deployment{{
			Metadata: kube.ObjectMeta{Name: "rabbitmq"},
			Spec: kube.DeploymentSpec{
				Selector: kube.LabelSelect{MatchLabels: map[string]string{"app": "rabbitmq"}},
				Template: kube.PodTemplate{
					Metadata: kube.ObjectMeta{Labels: map[string]string{"app": "rabbitmq"}},
					Spec: kube.PodSpec{
						Volumes: []kube.Volume{
							{Name: "empty-dir", EmptyDir: &kube.EmptyDirVol{}},
						},
						Containers: []kube.Container{{
							Name:  "rabbitmq",
							Image: "bitnami/rabbitmq:3.13",
							VolumeMounts: []kube.VolumeMount{
								{Name: "empty-dir", MountPath: "/tmp"},
								{Name: "empty-dir", MountPath: "/opt/bitnami/rabbitmq/etc"},
								{Name: "empty-dir", MountPath: "/opt/bitnami/rabbitmq/var/lib"},
								{Name: "empty-dir", MountPath: "/opt/bitnami/rabbitmq/.rabbitmq"},
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

	if _, ok := result.Compose.Volumes["rabbitmq-empty-dir"]; ok {
		t.Errorf("multi-mount within a single container should NOT promote to a named volume; compose.Volumes = %v", result.Compose.Volumes)
	}
	got := result.Compose.Services["rabbitmq"].Volumes
	want := []string{
		"/tmp",
		"/opt/bitnami/rabbitmq/etc",
		"/opt/bitnami/rabbitmq/var/lib",
		"/opt/bitnami/rabbitmq/.rabbitmq",
	}
	for _, w := range want {
		found := false
		for _, v := range got {
			if v == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected anonymous mount %q in service volumes, got %v", w, got)
		}
	}
	for _, v := range got {
		if strings.Contains(mountStr(v), "rabbitmq-empty-dir:") {
			t.Errorf("no mount should reference the named volume 'rabbitmq-empty-dir', got %q", v)
		}
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

// TestConvert_EscapesDollarsForCompose ensures we double every `$`
// in env values and command/entrypoint args before they hit
// docker-compose.yml. Compose interpolates `$VAR` / `${VAR}` at
// parse time, so anything we want the container's shell to see at
// runtime — Bitnami/nats-box bootstraps with `$XDG_CONFIG_HOME`,
// passwords with literal `$`, etc — must reach compose as `$$`.
//
// Surfaced by mp-production: nats-box's entrypoint is a literal
// shell script using $XDG_CONFIG_HOME and $work_dir; without the
// escape compose prints noisy "variable is not set" warnings on
// every up.
func TestConvert_EscapesDollarsForCompose(t *testing.T) {
	manifests := &kube.Manifests{
		Deployments: []kube.Deployment{{
			Metadata: kube.ObjectMeta{Name: "shellish"},
			Spec: kube.DeploymentSpec{
				Selector: kube.LabelSelect{MatchLabels: map[string]string{"app": "shellish"}},
				Template: kube.PodTemplate{
					Metadata: kube.ObjectMeta{Labels: map[string]string{"app": "shellish"}},
					Spec: kube.PodSpec{
						Containers: []kube.Container{{
							Name:    "shellish",
							Image:   "alpine",
							Command: []string{"sh", "-ec", `work_dir="$(pwd)"; cd "$XDG_CONFIG_HOME"`},
							Args:    []string{"--profile", "$ENV_NAME"},
							Env: []kube.EnvVar{
								{Name: "PASSWORD", Value: "pa$$w0rd!"},
								{Name: "DEPLOY_PATH", Value: "$HOME/app"},
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
	svc := result.Compose.Services["shellish"]

	// Entrypoint (k8s `command:`) gets escaped.
	wantEntrypoint := []string{"sh", "-ec", `work_dir="$$(pwd)"; cd "$$XDG_CONFIG_HOME"`}
	if !equalStringSlices(svc.Entrypoint, wantEntrypoint) {
		t.Errorf("entrypoint not escaped\n  got  %q\n  want %q", svc.Entrypoint, wantEntrypoint)
	}

	// Command (k8s `args:`) gets escaped.
	wantCommand := []string{"--profile", "$$ENV_NAME"}
	if !equalStringSlices(svc.Command, wantCommand) {
		t.Errorf("command not escaped\n  got  %q\n  want %q", svc.Command, wantCommand)
	}

	// Env values: round-trip through expandRefs (which treats `$$`
	// as the k8s escape for a literal `$`) then escapeDollars (which
	// doubles every remaining `$` for compose). A correctly-escaped
	// k8s input round-trips to the same string in the compose YAML
	// — compose's un-escape gets the user back to their intended
	// literal value at container start.
	//
	// Input `pa$$w0rd!` (k8s-escaped `$`) → expandRefs `pa$w0rd!`
	// (literal) → escapeDollars `pa$$w0rd!` (compose-escaped). ✓
	if got := svc.Environment["PASSWORD"]; got != `pa$$w0rd!` {
		t.Errorf("PASSWORD round-trip wrong: got %q, want pa$$w0rd!", got)
	}
	// `$HOME` (one `$`, container shell variable) → expandRefs leaves
	// untouched (no `(` after `$`) → escapeDollars `$$HOME`. Compose
	// un-escapes to `$HOME`; container's sh expands at runtime.
	if got := svc.Environment["DEPLOY_PATH"]; got != "$$HOME/app" {
		t.Errorf("DEPLOY_PATH = %q, want $$HOME/app", got)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestConvert_MemoryUnits verifies the k8s-to-compose memory unit
// conversion. Compose rejects k8s binary suffixes (Mi, Gi) — this
// would crash `docker compose up` on any real-world manifest that
// sets memory limits. Surfaced by Bitnami stateful charts in the
// user's mp-production namespace; the fix converts everything to
// plain bytes, which compose accepts losslessly.
func TestConvert_MemoryUnits(t *testing.T) {
	cases := []struct {
		k8sValue      string
		composeWanted string
	}{
		// Binary (k8s default) — bug-fix targets.
		{"512Mi", "536870912"},
		{"2Gi", "2147483648"},
		{"1Ki", "1024"},
		// Decimal forms. We translate to bytes for consistency even
		// though compose accepts m/g directly — one canonical
		// representation makes the generated YAML easier to diff.
		{"100M", "100000000"},
		{"1G", "1000000000"},
		// Lowercase suffixes (some manifests do this in the wild).
		{"512mi", "536870912"},
		{"2gi", "2147483648"},
		// Edge cases: pass-through where appropriate.
		{"", ""},
		{"42", "42"},     // already bytes-as-number
		{"5xyz", "5xyz"}, // unknown suffix — let compose error
		{"junk", "junk"}, // no digits — pass through
	}

	for _, tc := range cases {
		t.Run(tc.k8sValue, func(t *testing.T) {
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
									Image: "example/app",
									Resources: kube.ResourceReqs{
										Limits: kube.ResourceList{Memory: tc.k8sValue},
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
			svc := result.Compose.Services["app"]
			if tc.k8sValue == "" {
				// Empty memory → no Deploy block at all (existing behavior).
				if svc.Deploy != nil {
					t.Errorf("empty memory should produce nil Deploy; got %+v", svc.Deploy)
				}
				return
			}
			if svc.Deploy == nil || svc.Deploy.Resources.Limits == nil {
				t.Fatalf("expected Deploy.Resources.Limits set for memory %q", tc.k8sValue)
			}
			got := svc.Deploy.Resources.Limits.Memory
			if got != tc.composeWanted {
				t.Errorf("memory %q normalized to %q, want %q", tc.k8sValue, got, tc.composeWanted)
			}
		})
	}
}

// TestConvert_ConfigMapVolumeMount covers the realistic case
// surfaced by mp-production: a service like NATS that needs its
// config mounted at a known path. Compose has no native equivalent,
// so we materialize each ConfigMap key into a file under
// configs/<name>/ and bind-mount the directory into the container
// read-only (matching k8s configMap-volume semantics).
func TestConvert_ConfigMapVolumeMount(t *testing.T) {
	manifests := &kube.Manifests{
		ConfigMaps: []kube.ConfigMap{{
			Metadata: kube.ObjectMeta{Name: "nats-config"},
			Data: map[string]string{
				"nats.conf":    "port: 4222\n",
				"cluster.conf": "cluster: { listen: 0.0.0.0:6222 }\n",
			},
		}},
		Deployments: []kube.Deployment{{
			Metadata: kube.ObjectMeta{Name: "nats"},
			Spec: kube.DeploymentSpec{
				Selector: kube.LabelSelect{MatchLabels: map[string]string{"app": "nats"}},
				Template: kube.PodTemplate{
					Metadata: kube.ObjectMeta{Labels: map[string]string{"app": "nats"}},
					Spec: kube.PodSpec{
						Volumes: []kube.Volume{{
							Name:      "cfg",
							ConfigMap: &kube.ConfigMapVol{Name: "nats-config"},
						}},
						Containers: []kube.Container{{
							Name:    "nats",
							Image:   "nats:alpine",
							Command: []string{"--config", "/etc/nats-config/nats.conf"},
							VolumeMounts: []kube.VolumeMount{
								{Name: "cfg", MountPath: "/etc/nats-config"},
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

	// Mount entry: bind-mounted, read-only, under configs/<name>/.
	nats := result.Compose.Services["nats"]
	wantMount := "./configs/nats-config:/etc/nats-config:ro"
	found := false
	for _, v := range nats.Volumes {
		if v == wantMount {
			found = true
		}
	}
	if !found {
		t.Errorf("expected mount %q in nats.Volumes; got %v", wantMount, nats.Volumes)
	}

	// Each key in the ConfigMap should be a separate file in the
	// result's ConfigFiles map.
	for _, key := range []string{"nats.conf", "cluster.conf"} {
		path := "configs/nats-config/" + key
		if _, present := result.ConfigFiles[path]; !present {
			t.Errorf("expected ConfigFiles[%q] to be populated; got keys: %v", path, mapKeys(result.ConfigFiles))
		}
	}
	// Spot-check content survived round-trip.
	if got := result.ConfigFiles["configs/nats-config/nats.conf"]; got != "port: 4222\n" {
		t.Errorf("nats.conf content mismatch; got %q", got)
	}
}

// TestConvert_SecretVolumeMount verifies the Secret-volume sibling.
// Secret values get base64-decoded (existing secretValues helper),
// written under secrets/<name>/, mounted read-only. A warning fires
// because the resulting files contain real secret values — same
// hazard as the .env file, restated for the volume case.
func TestConvert_SecretVolumeMount(t *testing.T) {
	manifests := &kube.Manifests{
		Secrets: []kube.Secret{{
			Metadata:   kube.ObjectMeta{Name: "tls"},
			StringData: map[string]string{"cert.pem": "PEMDATA", "key.pem": "PRIVATEKEY"},
		}},
		Deployments: []kube.Deployment{{
			Metadata: kube.ObjectMeta{Name: "web"},
			Spec: kube.DeploymentSpec{
				Selector: kube.LabelSelect{MatchLabels: map[string]string{"app": "web"}},
				Template: kube.PodTemplate{
					Metadata: kube.ObjectMeta{Labels: map[string]string{"app": "web"}},
					Spec: kube.PodSpec{
						Volumes: []kube.Volume{{
							Name:   "tls",
							Secret: &kube.SecretVol{SecretName: "tls"},
						}},
						Containers: []kube.Container{{
							Name:  "web",
							Image: "nginx",
							VolumeMounts: []kube.VolumeMount{
								{Name: "tls", MountPath: "/etc/tls"},
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

	wantMount := "./secrets/tls:/etc/tls:ro"
	web := result.Compose.Services["web"]
	found := false
	for _, v := range web.Volumes {
		if v == wantMount {
			found = true
		}
	}
	if !found {
		t.Errorf("expected mount %q in web.Volumes; got %v", wantMount, web.Volumes)
	}

	if got := result.ConfigFiles["secrets/tls/cert.pem"]; got != "PEMDATA" {
		t.Errorf("cert.pem content mismatch; got %q", got)
	}
	if got := result.ConfigFiles["secrets/tls/key.pem"]; got != "PRIVATEKEY" {
		t.Errorf("key.pem content mismatch; got %q", got)
	}

	// A warning should explicitly flag that files contain real
	// secret values — same hazard the .env file already calls out.
	found = false
	for _, w := range result.Warnings {
		if strings.Contains(w, "secrets/") && strings.Contains(w, ".gitignore") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a warning naming secrets/ and gitignore; got %v", result.Warnings)
	}
}

// TestConvert_ConfigMapVolumeMissingWarns surfaces the case where
// a volumeMount references a ConfigMap that isn't in the input.
// Unlike a missing PVC, we can't fall back to anything sensible —
// just warn and skip the mount.
func TestConvert_ConfigMapVolumeMissingWarns(t *testing.T) {
	manifests := &kube.Manifests{
		Deployments: []kube.Deployment{{
			Metadata: kube.ObjectMeta{Name: "app"},
			Spec: kube.DeploymentSpec{
				Selector: kube.LabelSelect{MatchLabels: map[string]string{"app": "app"}},
				Template: kube.PodTemplate{
					Metadata: kube.ObjectMeta{Labels: map[string]string{"app": "app"}},
					Spec: kube.PodSpec{
						Volumes: []kube.Volume{{
							Name:      "cfg",
							ConfigMap: &kube.ConfigMapVol{Name: "missing-cm"},
						}},
						Containers: []kube.Container{{
							Name:         "app",
							Image:        "alpine",
							VolumeMounts: []kube.VolumeMount{{Name: "cfg", MountPath: "/etc/cfg"}},
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
	app := result.Compose.Services["app"]
	if len(app.Volumes) != 0 {
		t.Errorf("missing CM should result in no mount; got %v", app.Volumes)
	}
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "missing-cm") && strings.Contains(w, "not defined") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning naming the missing ConfigMap; got %v", result.Warnings)
	}
}

// TestConvert_ConfigMapVolumeMount_SubPath covers Bitnami's pattern
// of mounting a single key from a ConfigMap as a file (e.g.
// /scripts/setup.sh) via subPath. Without honouring subPath, the
// whole CM dir gets bind-mounted at the file path, Docker creates
// a directory at /scripts/setup.sh, and the container fails with
// "exec: ... is a directory: permission denied".
func TestConvert_ConfigMapVolumeMount_SubPath(t *testing.T) {
	manifests := &kube.Manifests{
		ConfigMaps: []kube.ConfigMap{{
			Metadata: kube.ObjectMeta{Name: "scripts"},
			Data: map[string]string{
				"setup.sh":   "#!/bin/bash\necho hi\n",
				"helper.sh":  "echo helper\n",
				"unrelated":  "noise\n",
			},
		}},
		Deployments: []kube.Deployment{{
			Metadata: kube.ObjectMeta{Name: "app"},
			Spec: kube.DeploymentSpec{
				Selector: kube.LabelSelect{MatchLabels: map[string]string{"app": "app"}},
				Template: kube.PodTemplate{
					Metadata: kube.ObjectMeta{Labels: map[string]string{"app": "app"}},
					Spec: kube.PodSpec{
						Volumes: []kube.Volume{
							{Name: "scripts", ConfigMap: &kube.ConfigMapVol{Name: "scripts"}},
						},
						Containers: []kube.Container{{
							Name:  "app",
							Image: "example/app",
							VolumeMounts: []kube.VolumeMount{
								{Name: "scripts", MountPath: "/scripts/setup.sh", SubPath: "setup.sh"},
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
	got := result.Compose.Services["app"].Volumes
	want := "./configs/scripts/setup.sh:/scripts/setup.sh:ro"
	found := false
	for _, v := range got {
		if v == want {
			found = true
		}
	}
	if !found {
		t.Errorf("expected subPath mount %q, got %v", want, got)
	}
	if _, ok := result.ConfigFiles["configs/scripts/setup.sh"]; !ok {
		t.Errorf("setup.sh content should still be materialized, got files=%v", mapKeys(result.ConfigFiles))
	}
}

// TestConvert_ConfigMapVolumeMount_SubPathMissingKeyWarns guards
// against silent typos: if the manifest references a subPath that
// doesn't match any ConfigMap key, the user gets a warning rather
// than a mystery missing-file at runtime.
func TestConvert_ConfigMapVolumeMount_SubPathMissingKeyWarns(t *testing.T) {
	manifests := &kube.Manifests{
		ConfigMaps: []kube.ConfigMap{{
			Metadata: kube.ObjectMeta{Name: "scripts"},
			Data:     map[string]string{"setup.sh": "ok"},
		}},
		Deployments: []kube.Deployment{{
			Metadata: kube.ObjectMeta{Name: "app"},
			Spec: kube.DeploymentSpec{
				Selector: kube.LabelSelect{MatchLabels: map[string]string{"app": "app"}},
				Template: kube.PodTemplate{
					Metadata: kube.ObjectMeta{Labels: map[string]string{"app": "app"}},
					Spec: kube.PodSpec{
						Volumes: []kube.Volume{
							{Name: "scripts", ConfigMap: &kube.ConfigMapVol{Name: "scripts"}},
						},
						Containers: []kube.Container{{
							Name:  "app",
							Image: "example/app",
							VolumeMounts: []kube.VolumeMount{
								{Name: "scripts", MountPath: "/scripts/missing.sh", SubPath: "missing.sh"},
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
	matched := false
	for _, w := range result.Warnings {
		if strings.Contains(w, `subPath "missing.sh"`) && strings.Contains(w, "not in the ConfigMap") {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("expected a warning about missing subPath key; got warnings=%v", result.Warnings)
	}
}

// TestConvert_SecretVolumeMount_SubPath is the Secret-side sibling.
// Real-world example: a Secret with multiple cert files where the
// container only wants /etc/ssl/private.pem mounted as the single
// private-key file.
func TestConvert_SecretVolumeMount_SubPath(t *testing.T) {
	manifests := &kube.Manifests{
		Secrets: []kube.Secret{{
			Metadata:   kube.ObjectMeta{Name: "tls"},
			StringData: map[string]string{"private.pem": "-----BEGIN-----"},
		}},
		Deployments: []kube.Deployment{{
			Metadata: kube.ObjectMeta{Name: "app"},
			Spec: kube.DeploymentSpec{
				Selector: kube.LabelSelect{MatchLabels: map[string]string{"app": "app"}},
				Template: kube.PodTemplate{
					Metadata: kube.ObjectMeta{Labels: map[string]string{"app": "app"}},
					Spec: kube.PodSpec{
						Volumes: []kube.Volume{
							{Name: "tls", Secret: &kube.SecretVol{SecretName: "tls"}},
						},
						Containers: []kube.Container{{
							Name:  "app",
							Image: "example/app",
							VolumeMounts: []kube.VolumeMount{
								{Name: "tls", MountPath: "/etc/ssl/private.pem", SubPath: "private.pem"},
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
	got := result.Compose.Services["app"].Volumes
	want := "./secrets/tls/private.pem:/etc/ssl/private.pem:ro"
	found := false
	for _, v := range got {
		if v == want {
			found = true
		}
	}
	if !found {
		t.Errorf("expected secret subPath mount %q, got %v", want, got)
	}
}

// TestConvert_ProjectedVolume_SecretSource covers the Bitnami
// rabbit pattern: a projected volume combines secret keys
// (rabbitmq-password, rabbitmq-erlang-cookie) into one directory
// the entrypoint reads from. Without projected support the mount
// silently disappears, the container finds no secrets, and rabbit
// hangs before opening AMQP — taking down every downstream client.
func TestConvert_ProjectedVolume_SecretSource(t *testing.T) {
	manifests := &kube.Manifests{
		Secrets: []kube.Secret{{
			Metadata: kube.ObjectMeta{Name: "rabbitmq"},
			StringData: map[string]string{
				"rabbitmq-password":       "supersecret",
				"rabbitmq-erlang-cookie":  "ABCDEF",
				"rabbitmq-admin-password": "anotherone",
			},
		}},
		Deployments: []kube.Deployment{{
			Metadata: kube.ObjectMeta{Name: "rabbitmq"},
			Spec: kube.DeploymentSpec{
				Selector: kube.LabelSelect{MatchLabels: map[string]string{"app": "rabbitmq"}},
				Template: kube.PodTemplate{
					Metadata: kube.ObjectMeta{Labels: map[string]string{"app": "rabbitmq"}},
					Spec: kube.PodSpec{
						Volumes: []kube.Volume{
							{
								Name: "rabbit-secrets",
								Projected: &kube.ProjectedVol{
									Sources: []kube.ProjectedSource{
										{Secret: &kube.ProjectedSecretSource{Name: "rabbitmq"}},
									},
								},
							},
						},
						Containers: []kube.Container{{
							Name:  "rabbitmq",
							Image: "bitnami/rabbitmq:3.13",
							VolumeMounts: []kube.VolumeMount{
								{Name: "rabbit-secrets", MountPath: "/opt/bitnami/rabbitmq/secrets"},
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
	mounts := result.Compose.Services["rabbitmq"].Volumes
	want := "./secrets/rabbit-secrets:/opt/bitnami/rabbitmq/secrets:ro"
	found := false
	for _, m := range mounts {
		if m == want {
			found = true
		}
	}
	if !found {
		t.Errorf("expected projected mount %q in service volumes, got %v", want, mounts)
	}
	for _, key := range []string{"rabbitmq-password", "rabbitmq-erlang-cookie", "rabbitmq-admin-password"} {
		path := "secrets/rabbit-secrets/" + key
		if _, ok := result.ConfigFiles[path]; !ok {
			t.Errorf("expected materialized file %q, files=%v", path, mapKeys(result.ConfigFiles))
		}
	}
}

// TestConvert_ProjectedVolume_ItemsRemap covers k8s' KeyToPath
// remapping: only specific keys are projected, optionally under
// different filenames. Used by charts that want to expose a single
// key from a multi-key Secret.
func TestConvert_ProjectedVolume_ItemsRemap(t *testing.T) {
	manifests := &kube.Manifests{
		Secrets: []kube.Secret{{
			Metadata: kube.ObjectMeta{Name: "tls"},
			StringData: map[string]string{
				"tls.crt": "CERT",
				"tls.key": "KEY",
				"unused":  "ignored",
			},
		}},
		Deployments: []kube.Deployment{{
			Metadata: kube.ObjectMeta{Name: "app"},
			Spec: kube.DeploymentSpec{
				Selector: kube.LabelSelect{MatchLabels: map[string]string{"app": "app"}},
				Template: kube.PodTemplate{
					Metadata: kube.ObjectMeta{Labels: map[string]string{"app": "app"}},
					Spec: kube.PodSpec{
						Volumes: []kube.Volume{{
							Name: "tls-bundle",
							Projected: &kube.ProjectedVol{
								Sources: []kube.ProjectedSource{{
									Secret: &kube.ProjectedSecretSource{
										Name: "tls",
										Items: []kube.KeyToPath{
											{Key: "tls.crt", Path: "server.crt"},
											{Key: "tls.key", Path: "server.key"},
										},
									},
								}},
							},
						}},
						Containers: []kube.Container{{
							Name:  "app",
							Image: "example/app",
							VolumeMounts: []kube.VolumeMount{
								{Name: "tls-bundle", MountPath: "/etc/ssl/private"},
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
	if _, ok := result.ConfigFiles["secrets/tls-bundle/server.crt"]; !ok {
		t.Errorf("remapped key 'server.crt' should be materialized; got %v", mapKeys(result.ConfigFiles))
	}
	if _, ok := result.ConfigFiles["secrets/tls-bundle/server.key"]; !ok {
		t.Errorf("remapped key 'server.key' should be materialized; got %v", mapKeys(result.ConfigFiles))
	}
	if _, ok := result.ConfigFiles["secrets/tls-bundle/unused"]; ok {
		t.Errorf("unselected key 'unused' should NOT be materialized when items list is given")
	}
}

// TestConvert_ProjectedVolume_MixedSources confirms a projected
// volume can combine a Secret and a ConfigMap in the same target
// directory (Bitnami nginx, several others). Secret presence wins
// the prefix decision so the dir lands under secrets/ and gets
// k8s-default 0644 mode at write time.
func TestConvert_ProjectedVolume_MixedSources(t *testing.T) {
	manifests := &kube.Manifests{
		ConfigMaps: []kube.ConfigMap{{
			Metadata: kube.ObjectMeta{Name: "app-config"},
			Data:     map[string]string{"app.conf": "hello"},
		}},
		Secrets: []kube.Secret{{
			Metadata:   kube.ObjectMeta{Name: "app-secret"},
			StringData: map[string]string{"token": "tk"},
		}},
		Deployments: []kube.Deployment{{
			Metadata: kube.ObjectMeta{Name: "app"},
			Spec: kube.DeploymentSpec{
				Selector: kube.LabelSelect{MatchLabels: map[string]string{"app": "app"}},
				Template: kube.PodTemplate{
					Metadata: kube.ObjectMeta{Labels: map[string]string{"app": "app"}},
					Spec: kube.PodSpec{
						Volumes: []kube.Volume{{
							Name: "bundle",
							Projected: &kube.ProjectedVol{
								Sources: []kube.ProjectedSource{
									{ConfigMap: &kube.ProjectedConfigMapSource{Name: "app-config"}},
									{Secret: &kube.ProjectedSecretSource{Name: "app-secret"}},
								},
							},
						}},
						Containers: []kube.Container{{
							Name:  "app",
							Image: "example/app",
							VolumeMounts: []kube.VolumeMount{
								{Name: "bundle", MountPath: "/etc/app"},
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
	for _, want := range []string{"secrets/bundle/app.conf", "secrets/bundle/token"} {
		if _, ok := result.ConfigFiles[want]; !ok {
			t.Errorf("expected materialized %q; got %v", want, mapKeys(result.ConfigFiles))
		}
	}
}

// TestConvert_FQDNAliases_HeadlessStatefulSet covers the failure
// mode where a Bitnami helm chart bakes a connection string like
// `nats-0.nats-headless.default.svc.cluster.local` into Secret env
// vars. The StatefulSet plus a clusterIP=None Service make those
// FQDNs resolvable in k8s; in compose they're nonsense unless we
// add network aliases to the converted service.
func TestConvert_FQDNAliases_HeadlessStatefulSet(t *testing.T) {
	manifests := &kube.Manifests{
		Services: []kube.Service{
			{
				Metadata: kube.ObjectMeta{Name: "nats", Namespace: "default"},
				Spec: kube.ServiceSpec{
					Selector: map[string]string{"app": "nats"},
					Ports:    []kube.ServicePort{{Port: 4222}},
				},
			},
			{
				Metadata: kube.ObjectMeta{Name: "nats-headless", Namespace: "default"},
				Spec: kube.ServiceSpec{
					Selector:  map[string]string{"app": "nats"},
					Ports:     []kube.ServicePort{{Port: 4222}},
					ClusterIP: "None",
				},
			},
		},
		StatefulSets: []kube.StatefulSet{{
			Metadata: kube.ObjectMeta{Name: "nats", Namespace: "default"},
			Spec: kube.StatefulSetSpec{
				Replicas: 3,
				Selector: kube.LabelSelect{MatchLabels: map[string]string{"app": "nats"}},
				Template: kube.PodTemplate{
					Metadata: kube.ObjectMeta{Labels: map[string]string{"app": "nats"}},
					Spec: kube.PodSpec{
						Containers: []kube.Container{{
							Name:  "nats",
							Image: "nats:2-alpine",
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
	got := result.Compose.Services["nats"].Networks["default"].Aliases
	mustHave := []string{
		// non-headless ClusterIP service forms
		"nats.default",
		"nats.default.svc",
		"nats.default.svc.cluster.local",
		// headless sibling service forms
		"nats-headless",
		"nats-headless.default",
		"nats-headless.default.svc",
		"nats-headless.default.svc.cluster.local",
		// pod-N forms (headless + StatefulSet)
		"nats-0",
		"nats-1",
		"nats-2",
		"nats-0.nats-headless.default.svc.cluster.local",
		"nats-1.nats-headless.default.svc.cluster.local",
		"nats-2.nats-headless.default.svc.cluster.local",
	}
	have := map[string]bool{}
	for _, a := range got {
		have[a] = true
	}
	for _, want := range mustHave {
		if !have[want] {
			t.Errorf("missing FQDN alias %q in %v", want, got)
		}
	}
	// The compose service name itself should not appear as an alias —
	// compose's embedded DNS already resolves it.
	if have["nats"] {
		t.Errorf("alias list should not include the compose service name itself, got %v", got)
	}
}

// TestConvert_FQDNAliases_BitnamiPattern covers the form Bitnami
// helm charts bake into env vars: <workload>.<headless>.<ns>.svc.cluster.local
// (no pod ordinal). The previous code only emitted <workload>-N forms,
// leaving Bitnami's RABBITMQ_NODE_NAME hostname unresolvable and
// hanging the Erlang node binding.
func TestConvert_FQDNAliases_BitnamiPattern(t *testing.T) {
	manifests := &kube.Manifests{
		Services: []kube.Service{
			{
				Metadata: kube.ObjectMeta{Name: "rabbitmq", Namespace: "default"},
				Spec: kube.ServiceSpec{
					Selector: map[string]string{"app": "rabbitmq"},
					Ports:    []kube.ServicePort{{Port: 5672}},
				},
			},
			{
				Metadata: kube.ObjectMeta{Name: "rabbitmq-headless", Namespace: "default"},
				Spec: kube.ServiceSpec{
					Selector:  map[string]string{"app": "rabbitmq"},
					Ports:     []kube.ServicePort{{Port: 5672}},
					ClusterIP: "None",
				},
			},
		},
		Deployments: []kube.Deployment{{
			Metadata: kube.ObjectMeta{Name: "rabbitmq", Namespace: "default"},
			Spec: kube.DeploymentSpec{
				Selector: kube.LabelSelect{MatchLabels: map[string]string{"app": "rabbitmq"}},
				Template: kube.PodTemplate{
					Metadata: kube.ObjectMeta{Labels: map[string]string{"app": "rabbitmq"}},
					Spec: kube.PodSpec{
						Containers: []kube.Container{{Name: "rabbitmq", Image: "bitnami/rabbitmq:3.13"}},
					},
				},
			},
		}},
	}
	result, err := convert.Convert(manifests, nil)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	rabbit := result.Compose.Services["rabbitmq"]
	want := "rabbitmq.rabbitmq-headless.default.svc.cluster.local"

	// Alias for the form RABBITMQ_NODE_NAME uses must be present.
	have := map[string]bool{}
	for _, a := range rabbit.Networks["default"].Aliases {
		have[a] = true
	}
	if !have[want] {
		t.Errorf("missing Bitnami-pattern alias %q in %v", want, rabbit.Networks["default"].Aliases)
	}

	// And hostname must be set so Erlang's node-binding self-lookup
	// succeeds — aliases only cover lookups from OTHER containers.
	if rabbit.Hostname != want {
		t.Errorf("compose Hostname should be %q (so the container can resolve its own FQDN), got %q", want, rabbit.Hostname)
	}
}

// TestConvert_FQDNAliases_DeploymentNoPodOrdinals guards against
// emitting StatefulSet-style pod-N aliases for plain Deployments,
// which don't have stable pod hostnames.
func TestConvert_FQDNAliases_DeploymentNoPodOrdinals(t *testing.T) {
	manifests := &kube.Manifests{
		Services: []kube.Service{{
			Metadata: kube.ObjectMeta{Name: "api", Namespace: "default"},
			Spec: kube.ServiceSpec{
				Selector: map[string]string{"app": "api"},
				Ports:    []kube.ServicePort{{Port: 3000}},
			},
		}},
		Deployments: []kube.Deployment{{
			Metadata: kube.ObjectMeta{Name: "api", Namespace: "default"},
			Spec: kube.DeploymentSpec{
				Replicas: 3,
				Selector: kube.LabelSelect{MatchLabels: map[string]string{"app": "api"}},
				Template: kube.PodTemplate{
					Metadata: kube.ObjectMeta{Labels: map[string]string{"app": "api"}},
					Spec: kube.PodSpec{
						Containers: []kube.Container{{Name: "api", Image: "example/api"}},
					},
				},
			},
		}},
	}
	result, err := convert.Convert(manifests, nil)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	for _, a := range result.Compose.Services["api"].Networks["default"].Aliases {
		if strings.HasPrefix(a, "api-0") || strings.HasPrefix(a, "api-1") || strings.HasPrefix(a, "api-2") {
			t.Errorf("Deployments shouldn't get pod-N aliases, got %q", a)
		}
	}
}

// TestConvert_EmptyDir_SubPathSharesVolume covers Bitnami's rabbitmq
// chart pattern: one emptyDir mounted by the init container at
// /emptydir/ AND by the main container at half a dozen paths,
// each with a different subPath partitioning the volume into
// per-path subdirectories. Init copies its plugins into
// /emptydir/app-plugins-dir; main expects to read them at
// /opt/.../plugins (subPath: app-plugins-dir). Without subpath
// support compose can't express this share and main starts with
// an empty plugins dir, hanging Erlang in prelaunch.
func TestConvert_EmptyDir_SubPathSharesVolume(t *testing.T) {
	manifests := &kube.Manifests{
		Deployments: []kube.Deployment{{
			Metadata: kube.ObjectMeta{Name: "rabbitmq"},
			Spec: kube.DeploymentSpec{
				Selector: kube.LabelSelect{MatchLabels: map[string]string{"app": "rabbitmq"}},
				Template: kube.PodTemplate{
					Metadata: kube.ObjectMeta{Labels: map[string]string{"app": "rabbitmq"}},
					Spec: kube.PodSpec{
						Volumes: []kube.Volume{
							{Name: "empty-dir", EmptyDir: &kube.EmptyDirVol{}},
						},
						InitContainers: []kube.Container{{
							Name:  "prepare-plugins-dir",
							Image: "bitnami/rabbitmq:3.13",
							VolumeMounts: []kube.VolumeMount{
								{Name: "empty-dir", MountPath: "/emptydir"},
							},
						}},
						Containers: []kube.Container{{
							Name:  "rabbitmq",
							Image: "bitnami/rabbitmq:3.13",
							VolumeMounts: []kube.VolumeMount{
								{Name: "empty-dir", MountPath: "/tmp", SubPath: "tmp-dir"},
								{Name: "empty-dir", MountPath: "/opt/bitnami/rabbitmq/plugins", SubPath: "app-plugins-dir"},
								{Name: "empty-dir", MountPath: "/opt/bitnami/rabbitmq/etc/rabbitmq", SubPath: "app-conf-dir"},
								{Name: "empty-dir", MountPath: "/opt/bitnami/rabbitmq/var/lib/rabbitmq", SubPath: "app-data-dir"},
								{Name: "empty-dir", MountPath: "/opt/bitnami/rabbitmq/.rabbitmq", SubPath: "rabbitmq-conf-dir"},
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
	if _, ok := result.Compose.Volumes["rabbitmq-empty-dir"]; !ok {
		t.Errorf("named volume rabbitmq-empty-dir should be declared (init+main share via subpaths); got %v", result.Compose.Volumes)
	}

	// Init's mount has no subPath — short form is fine.
	initMounts := result.Compose.Services["rabbitmq-prepare-plugins-dir"].Volumes
	wantInit := "rabbitmq-empty-dir:/emptydir"
	found := false
	for _, m := range initMounts {
		if mountStr(m) == wantInit {
			found = true
		}
	}
	if !found {
		t.Errorf("init container should mount %q in short form, got %v", wantInit, initMounts)
	}

	// Each main mount must be long-form referencing the same named
	// volume + its specific subpath.
	mainMounts := result.Compose.Services["rabbitmq"].Volumes
	wantSubpaths := map[string]string{
		"/tmp":                                    "tmp-dir",
		"/opt/bitnami/rabbitmq/plugins":           "app-plugins-dir",
		"/opt/bitnami/rabbitmq/etc/rabbitmq":      "app-conf-dir",
		"/opt/bitnami/rabbitmq/var/lib/rabbitmq":  "app-data-dir",
		"/opt/bitnami/rabbitmq/.rabbitmq":         "rabbitmq-conf-dir",
	}
	got := map[string]string{}
	for _, m := range mainMounts {
		long, ok := m.(*compose.MountLong)
		if !ok {
			continue
		}
		if long.Source != "rabbitmq-empty-dir" {
			t.Errorf("expected long-form source rabbitmq-empty-dir, got %q", long.Source)
		}
		if long.Volume == nil {
			t.Errorf("long-form mount should carry a volume.subpath, got %+v", long)
			continue
		}
		got[long.Target] = long.Volume.Subpath
	}
	for path, want := range wantSubpaths {
		if got[path] != want {
			t.Errorf("mount at %q: subpath = %q, want %q (full got=%v)", path, got[path], want, got)
		}
	}
}

// mountStr returns the short-form string of a Volumes entry, or
// "" if the entry is a long-form *MountLong (which short-form
// string assertions don't apply to). Tests that need to inspect
// long-form entries assert their type explicitly.
func mountStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
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
