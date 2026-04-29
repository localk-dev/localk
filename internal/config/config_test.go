package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "localk.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func TestLoad_MissingFile(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil config for missing file, got %+v", cfg)
	}
}

func TestLoad_EmptyFile(t *testing.T) {
	p := writeTemp(t, "")
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil config for empty file, got %+v", cfg)
	}
}

func TestLoad_AllFields(t *testing.T) {
	p := writeTemp(t, `
services:
  api:
    build: ./services/api
  worker:
    skip: true
  postgres:
    image: postgres:15-alpine
  web:
    build:
      context: ./web
      dockerfile: Dockerfile.dev
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load returned nil")
	}

	api := cfg.ServiceOverrideFor("api")
	if api.Build == nil || api.Build.Context != "./services/api" || api.Build.Dockerfile != "" {
		t.Errorf("api.Build = %+v, want context=./services/api", api.Build)
	}

	worker := cfg.ServiceOverrideFor("worker")
	if !worker.Skip {
		t.Errorf("worker.Skip = false, want true")
	}

	pg := cfg.ServiceOverrideFor("postgres")
	if pg.Image != "postgres:15-alpine" {
		t.Errorf("postgres.Image = %q, want postgres:15-alpine", pg.Image)
	}

	web := cfg.ServiceOverrideFor("web")
	if web.Build == nil || web.Build.Context != "./web" || web.Build.Dockerfile != "Dockerfile.dev" {
		t.Errorf("web.Build = %+v, want {./web, Dockerfile.dev}", web.Build)
	}

	missing := cfg.ServiceOverrideFor("nope")
	if missing.Skip || missing.Image != "" || missing.Build != nil {
		t.Errorf("expected zero-value override for missing service, got %+v", missing)
	}
}

func TestLoad_BuildAcceptsBothForms(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want BuildOverride
	}{
		{
			name: "string shorthand",
			yaml: "services:\n  api:\n    build: ./services/api\n",
			want: BuildOverride{Context: "./services/api"},
		},
		{
			name: "object",
			yaml: "services:\n  api:\n    build:\n      context: ./api\n      dockerfile: Dockerfile.local\n",
			want: BuildOverride{Context: "./api", Dockerfile: "Dockerfile.local"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := writeTemp(t, tc.yaml)
			cfg, err := Load(p)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			got := cfg.ServiceOverrideFor("api").Build
			if got == nil {
				t.Fatal("Build is nil")
			}
			if *got != tc.want {
				t.Errorf("got %+v, want %+v", *got, tc.want)
			}
		})
	}
}

func TestLoad_BuildObjectMissingContext(t *testing.T) {
	p := writeTemp(t, `
services:
  api:
    build:
      dockerfile: Dockerfile
`)
	if _, err := Load(p); err == nil {
		t.Fatal("expected error when build object lacks context")
	}
}

func TestLoad_MalformedYAML(t *testing.T) {
	p := writeTemp(t, "services: [this is not a map")
	if _, err := Load(p); err == nil {
		t.Fatal("expected error on malformed YAML")
	}
}

// TestServiceOverrideFor_NilConfig verifies the nil-safe accessor — callers
// shouldn't have to nil-check before applying overrides.
func TestServiceOverrideFor_NilConfig(t *testing.T) {
	var cfg *Config
	got := cfg.ServiceOverrideFor("anything")
	if got.Skip || got.Image != "" || got.Build != nil {
		t.Errorf("expected zero-value, got %+v", got)
	}
}
