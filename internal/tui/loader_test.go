package tui

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNew_LoadsBaseAndOverlays exercises the full loader against
// real overlay files. The TUI's correctness hinges on the merge
// (compose + dev overlay + disable overlay → []ServiceRow with
// correct flags), so worth a real-files round-trip rather than mocks.
func TestNew_LoadsBaseAndOverlays(t *testing.T) {
	dir := t.TempDir()

	// Base compose file: three services.
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(`
services:
  api:
    image: example/api:1.0
    ports: ["3000:3000"]
  worker:
    image: example/worker:1.0
  postgres:
    image: postgres:16-alpine
    ports: ["5432:5432"]
`), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}

	// Disable overlay: postgres is sticky-disabled.
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.disable.yml"), []byte(`
services:
  postgres:
    profiles:
    - disabled
`), 0o644); err != nil {
		t.Fatalf("write disable: %v", err)
	}

	// Dev overlay: worker is in dev mode forwarding to host port 4000.
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.dev.yml"), []byte(`
services:
  worker:
    image: alpine/socat:latest
    entrypoint:
      - socat
      - -d
      - TCP-LISTEN:80,fork,reuseaddr
      - TCP:host.docker.internal:4000
    ports: []
    depends_on: {}
`), 0o644); err != nil {
		t.Fatalf("write dev: %v", err)
	}

	m, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(m.rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(m.rows))
	}

	// Rows must be sorted alphabetically.
	wantOrder := []string{"api", "postgres", "worker"}
	for i, want := range wantOrder {
		if m.rows[i].Name != want {
			t.Errorf("rows[%d].Name = %q, want %q", i, m.rows[i].Name, want)
		}
	}

	api := findRow(m, "api")
	if api == nil || api.Disabled || api.DevPort != 0 {
		t.Errorf("api should be plain enabled; got %+v", api)
	}
	postgres := findRow(m, "postgres")
	if postgres == nil || !postgres.Disabled || postgres.DevPort != 0 {
		t.Errorf("postgres should be disabled, not in dev; got %+v", postgres)
	}
	worker := findRow(m, "worker")
	if worker == nil || worker.Disabled || worker.DevPort != 4000 {
		t.Errorf("worker should be in dev on port 4000; got %+v", worker)
	}
}

func TestNew_MissingCompose(t *testing.T) {
	dir := t.TempDir()
	_, err := New(dir)
	if err == nil {
		t.Fatal("expected error when compose file is missing")
	}
}

func TestApplyFilter(t *testing.T) {
	m := &Model{
		rows: []ServiceRow{
			{Name: "api", lowerName: "api"},
			{Name: "api-gateway", lowerName: "api-gateway"},
			{Name: "postgres", lowerName: "postgres"},
			{Name: "redis", lowerName: "redis"},
		},
		visible: []int{0, 1, 2, 3},
	}
	// Setup: filter input. We don't need the bubbles textinput
	// machinery; we just need to be able to read the value via
	// applyFilter, which calls m.filter.Value(). Inject directly.
	// Tests for the textinput Update path live in bubbles itself.
	m.filter.SetValue("api")
	m.applyFilter()
	wantNames := []string{"api", "api-gateway"}
	if got := visibleNames(m); !equalStrings(got, wantNames) {
		t.Errorf("visible names with filter %q = %v, want %v", "api", got, wantNames)
	}

	m.filter.SetValue("")
	m.applyFilter()
	if len(m.visible) != 4 {
		t.Errorf("empty filter should restore all rows; got %d", len(m.visible))
	}

	// Filter that matches nothing.
	m.filter.SetValue("zzz")
	m.applyFilter()
	if len(m.visible) != 0 {
		t.Errorf("non-matching filter should result in 0 visible rows; got %d", len(m.visible))
	}
}

// TestParseDevForwardPort covers the entrypoint-string parser. The
// dev overlay round-trips ports through socat args, and a parsing
// regression here would silently lose the user's port number.
func TestParseDevForwardPort(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want int
	}{
		{"standard", []string{"socat", "-d", "TCP-LISTEN:80,fork,reuseaddr", "TCP:host.docker.internal:3000"}, 3000},
		{"empty", nil, 0},
		{"non-numeric tail", []string{"TCP:host:abc"}, 0},
		{"no colon", []string{"localhost"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseDevForwardPort(tc.in); got != tc.want {
				t.Errorf("parseDevForwardPort(%v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func findRow(m *Model, name string) *ServiceRow {
	for i := range m.rows {
		if m.rows[i].Name == name {
			return &m.rows[i]
		}
	}
	return nil
}

func visibleNames(m *Model) []string {
	out := make([]string, 0, len(m.visible))
	for _, idx := range m.visible {
		out = append(out, m.rows[idx].Name)
	}
	return out
}

func equalStrings(a, b []string) bool {
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
