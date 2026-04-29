package convert_test

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/go-yaml"

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

	result, err := convert.Convert(manifests)
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
	result, err := convert.Convert(manifests)
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
