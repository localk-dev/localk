package compose_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/localk-dev/localk/internal/compose"
)

// The compose package is pure data types — no logic to test, but the YAML
// shape they emit IS user-visible (it's the docker-compose.yml localk
// generates). Round-trip tests guard against a future refactor that
// silently changes a tag and corrupts every output file.

func TestFile_RoundTrip(t *testing.T) {
	in := compose.File{
		Version: "3.8",
		Services: map[string]compose.Service{
			"api": {
				Image:       "example/api:1.0",
				Restart:     "unless-stopped",
				Ports:       []string{"80:80"},
				Environment: map[string]string{"LOG_LEVEL": "info"},
				EnvFile:     []string{".env"},
			},
		},
		Volumes: map[string]compose.Volume{"data": {}},
	}
	out := marshalAndUnmarshal(t, in)

	if got := out.Version; got != "3.8" {
		t.Errorf("Version = %q, want 3.8", got)
	}
	api, ok := out.Services["api"]
	if !ok {
		t.Fatal("api service missing after round-trip")
	}
	if api.Image != "example/api:1.0" {
		t.Errorf("api.Image = %q", api.Image)
	}
	if len(api.Ports) != 1 || api.Ports[0] != "80:80" {
		t.Errorf("api.Ports = %v", api.Ports)
	}
	if api.Environment["LOG_LEVEL"] != "info" {
		t.Errorf("api.Environment[LOG_LEVEL] = %q", api.Environment["LOG_LEVEL"])
	}
	if _, present := out.Volumes["data"]; !present {
		t.Errorf("volume 'data' missing after round-trip")
	}
}

// TestService_NetworkModeYAMLTag is the regression guard for the field
// added during the sidecar work. Sidecar services express their pod-
// share via this field, and the YAML tag must be `network_mode` (not
// the default Go-cased `networkmode`) for compose to honor it.
func TestService_NetworkModeYAMLTag(t *testing.T) {
	in := compose.File{Services: map[string]compose.Service{
		"sidecar": {
			Image:       "alpine",
			NetworkMode: "service:main",
		},
	}}
	data, err := yaml.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), "network_mode: service:main") {
		t.Errorf("expected `network_mode: service:main` in output:\n%s", data)
	}
}

// TestService_DependsOnConditionShape covers the long-form depends_on
// added during the initContainers work. The map shape must marshal as
//
//	depends_on:
//	  init-1:
//	    condition: service_completed_successfully
//
// not as a flat list — the chain only works because compose honors
// the `condition` key.
func TestService_DependsOnConditionShape(t *testing.T) {
	in := compose.File{Services: map[string]compose.Service{
		"main": {
			Image: "example/app",
			DependsOn: map[string]compose.DependsOnSpec{
				"init-migrate": {Condition: "service_completed_successfully"},
			},
		},
	}}
	data, err := yaml.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	out := string(data)
	for _, want := range []string{
		"depends_on:",
		"init-migrate:",
		"condition: service_completed_successfully",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output:\n%s", want, out)
		}
	}
}

// TestService_OmitEmptyOptionals: empty optional fields shouldn't
// pollute the generated compose file. Compose users frequently look at
// the YAML by eye; suppressing zero-value fields keeps it readable.
func TestService_OmitEmptyOptionals(t *testing.T) {
	in := compose.File{Services: map[string]compose.Service{
		"minimal": {Image: "alpine"},
	}}
	data, err := yaml.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	out := string(data)
	for _, mustNotContain := range []string{
		"build:",
		"command:",
		"entrypoint:",
		"environment:",
		"env_file:",
		"ports:",
		"volumes:",
		"depends_on:",
		"deploy:",
		"network_mode:",
	} {
		if strings.Contains(out, mustNotContain) {
			t.Errorf("minimal service shouldn't include %q in output:\n%s", mustNotContain, out)
		}
	}
}

func TestBuild_AcceptsFullSpec(t *testing.T) {
	in := compose.File{Services: map[string]compose.Service{
		"web": {
			Build: &compose.Build{
				Context:    "./web",
				Dockerfile: "Dockerfile.dev",
			},
		},
	}}
	out := marshalAndUnmarshal(t, in)
	web := out.Services["web"]
	if web.Build == nil {
		t.Fatal("Build is nil after round-trip")
	}
	if web.Build.Context != "./web" || web.Build.Dockerfile != "Dockerfile.dev" {
		t.Errorf("Build = %+v", web.Build)
	}
}

func TestDeploy_ResourceLimitsRoundTrip(t *testing.T) {
	in := compose.File{Services: map[string]compose.Service{
		"db": {
			Deploy: &compose.Deploy{
				Resources: compose.DeployResources{
					Limits: &compose.ResourceSpec{
						CPUs:   "0.5",
						Memory: "512Mi",
					},
				},
			},
		},
	}}
	out := marshalAndUnmarshal(t, in)
	limits := out.Services["db"].Deploy.Resources.Limits
	if limits == nil {
		t.Fatal("Limits is nil after round-trip")
	}
	if limits.CPUs != "0.5" || limits.Memory != "512Mi" {
		t.Errorf("Limits = %+v", limits)
	}
}

// TestLoadFile_RoundTripsThroughDisk parses a real-shape compose
// file and verifies the values that the typed commands and the TUI
// loader both depend on (Service.Image, Service.Ports). LoadFile is
// the single entrypoint they share, so a regression here breaks
// both at once.
func TestLoadFile_RoundTripsThroughDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	body := `
services:
  api:
    image: example/api:1.0
    ports: ["3000:3000"]
  worker:
    image: example/worker:1.0
volumes:
  data: {}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	f, err := compose.LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(f.Services) != 2 {
		t.Errorf("got %d services, want 2", len(f.Services))
	}
	api := f.Services["api"]
	if api.Image != "example/api:1.0" {
		t.Errorf("api.Image = %q", api.Image)
	}
	if len(api.Ports) != 1 || api.Ports[0] != "3000:3000" {
		t.Errorf("api.Ports = %v", api.Ports)
	}
}

// TestLoadFile_MissingReturnsErrNotExist lets callers branch on
// errors.Is(err, os.ErrNotExist) to surface friendly hints. The
// typed `localk dev`/`disable` commands and the TUI all rely on
// this — anything else (parse errors, permission errors) falls
// through as a different message.
func TestLoadFile_MissingReturnsErrNotExist(t *testing.T) {
	_, err := compose.LoadFile(filepath.Join(t.TempDir(), "nope.yml"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected wrapped os.ErrNotExist; got %v", err)
	}
}

// marshalAndUnmarshal writes f to YAML and parses it back. Used by
// every round-trip test so they all fail with the same actionable
// diagnostic when YAML tags drift.
func marshalAndUnmarshal(t *testing.T, f compose.File) compose.File {
	t.Helper()
	data, err := yaml.Marshal(f)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out compose.File
	if err := yaml.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v\noutput was:\n%s", err, data)
	}
	return out
}
