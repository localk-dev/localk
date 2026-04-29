package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSplitDoubleDash(t *testing.T) {
	cases := []struct {
		name            string
		in              []string
		wantOwn         []string
		wantPassthrough []string
	}{
		{
			name:    "no double dash",
			in:      []string{"-f", "out/docker-compose.yml", "--build"},
			wantOwn: []string{"-f", "out/docker-compose.yml", "--build"},
		},
		{
			name:            "double dash with passthrough",
			in:              []string{"-f", "out/docker-compose.yml", "--", "--remove-orphans", "--timeout", "5"},
			wantOwn:         []string{"-f", "out/docker-compose.yml"},
			wantPassthrough: []string{"--remove-orphans", "--timeout", "5"},
		},
		{
			name:            "double dash at start",
			in:              []string{"--", "service1", "service2"},
			wantOwn:         []string{},
			wantPassthrough: []string{"service1", "service2"},
		},
		{
			name: "empty",
			in:   []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotOwn, gotPass := splitDoubleDash(tc.in)
			if !equalStrings(gotOwn, tc.wantOwn) {
				t.Errorf("own: got %v, want %v", gotOwn, tc.wantOwn)
			}
			if !equalStrings(gotPass, tc.wantPassthrough) {
				t.Errorf("passthrough: got %v, want %v", gotPass, tc.wantPassthrough)
			}
		})
	}
}

// TestResolveExistingCompose covers the success path (file exists) and
// the failure path (file missing → error message points the user at
// `localk generate`).
func TestResolveExistingCompose(t *testing.T) {
	dir := t.TempDir()
	composePath := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	t.Run("found", func(t *testing.T) {
		got, err := resolveExistingCompose(dir, "docker-compose.yml")
		if err != nil {
			t.Fatalf("resolveExistingCompose: %v", err)
		}
		if got != composePath {
			t.Errorf("got %q, want %q", got, composePath)
		}
	})

	t.Run("missing points at generate", func(t *testing.T) {
		_, err := resolveExistingCompose(dir, "does-not-exist.yml")
		if err == nil {
			t.Fatal("expected error for missing file")
		}
		if !strings.Contains(err.Error(), "localk generate") {
			t.Errorf("error should suggest `localk generate`, got: %v", err)
		}
	})

	t.Run("absolute file overrides out-dir", func(t *testing.T) {
		got, err := resolveExistingCompose("/some/other/dir", composePath)
		if err != nil {
			t.Fatalf("resolveExistingCompose: %v", err)
		}
		if got != composePath {
			t.Errorf("absolute path should win over out-dir; got %q, want %q", got, composePath)
		}
	})
}

func equalStrings(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}
