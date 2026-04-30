package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/localk-dev/localk/internal/compose"
)

func TestLoadBaseCompose_MissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := loadBaseCompose(dir)
	if err == nil {
		t.Fatal("expected error for missing compose file")
	}
	if !strings.Contains(err.Error(), "localk generate") {
		t.Errorf("error should hint at `localk generate`, got: %v", err)
	}
}

func TestLoadBaseCompose_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	yaml := `services:
  api:
    image: api:1.0
    ports: ["3000:3000"]
  worker:
    image: worker:1.0
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	f, err := loadBaseCompose(dir)
	if err != nil {
		t.Fatalf("loadBaseCompose: %v", err)
	}
	if len(f.Services) != 2 {
		t.Errorf("got %d services, want 2", len(f.Services))
	}
	if got := f.Services["api"].Ports; len(got) != 1 || got[0] != "3000:3000" {
		t.Errorf("api.Ports = %v, want [3000:3000]", got)
	}
}

func TestServiceNamesSorted(t *testing.T) {
	f := &compose.File{Services: map[string]compose.Service{
		"zebra":  {},
		"alpha":  {},
		"middle": {},
	}}
	got := serviceNames(f)
	want := []string{"alpha", "middle", "zebra"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("serviceNames[%d] = %q, want %q (full: %v)", i, got[i], w, got)
		}
	}
}

func TestJoinHints(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"a", "b", "c"}, "a, b, c"},
		// Long list truncates with "(and N more)".
		{[]string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}, "a, b, c, d, e, f, g, h (and 2 more)"},
	}
	for _, tc := range cases {
		if got := joinHints(tc.in); got != tc.want {
			t.Errorf("joinHints(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSplitNonEmptyLines(t *testing.T) {
	in := `# comment
  # indented comment

KEY=value
ANOTHER=val
`
	got := splitNonEmptyLines(in)
	want := []string{"KEY=value", "ANOTHER=val"}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("line %d = %q, want %q", i, got[i], w)
		}
	}
}
