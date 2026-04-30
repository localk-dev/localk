package devmode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

func TestDisabled_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.disable.yml")

	d, exists, err := LoadDisabled(path)
	if err != nil {
		t.Fatalf("LoadDisabled(missing): %v", err)
	}
	if exists {
		t.Errorf("expected exists=false for missing file")
	}

	d.Add("postgres")
	d.Add("redis")
	if err := d.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	d2, exists, err := LoadDisabled(path)
	if err != nil {
		t.Fatalf("LoadDisabled(after save): %v", err)
	}
	if !exists {
		t.Errorf("expected exists=true after save")
	}
	if len(d2.Services) != 2 {
		t.Fatalf("got %d services, want 2", len(d2.Services))
	}
	if !d2.IsDisabled("postgres") || !d2.IsDisabled("redis") {
		t.Errorf("expected postgres and redis to be disabled, got %v", d2.Names())
	}
	if d2.IsDisabled("api") {
		t.Errorf("api shouldn't be disabled")
	}

	// Verify the YAML shape is what compose expects: each service has
	// only the profiles field, so the base service definition merges
	// through unchanged.
	got, _ := os.ReadFile(path)
	for _, want := range []string{
		"postgres:",
		"profiles:",
		`- disabled`,
		"redis:",
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("expected %q in overlay file, got:\n%s", want, got)
		}
	}
}

func TestDisabled_RemoveMissingReturnsFalse(t *testing.T) {
	d := &DisabledOverlay{Services: map[string]disabledEntry{}}
	if d.Remove("ghost") {
		t.Error("Remove on missing service should return false")
	}
}

func TestDisabled_LastRemovalDeletesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.disable.yml")
	d := &DisabledOverlay{Services: map[string]disabledEntry{}}
	d.Add("postgres")
	if err := d.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file should exist: %v", err)
	}
	d.Remove("postgres")
	if err := d.Save(path); err != nil {
		t.Fatalf("Save after last remove: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected file to be removed when overlay is empty, got err: %v", err)
	}
}

func TestDisabled_Clear(t *testing.T) {
	d := &DisabledOverlay{Services: map[string]disabledEntry{}}
	d.Add("a")
	d.Add("b")
	d.Add("c")
	d.Clear()
	if len(d.Services) != 0 {
		t.Errorf("Clear should empty Services, got %v", d.Services)
	}
}

func TestDisabled_NamesSorted(t *testing.T) {
	d := &DisabledOverlay{Services: map[string]disabledEntry{}}
	d.Add("zebra")
	d.Add("alpha")
	d.Add("middle")
	got := d.Names()
	want := []string{"alpha", "middle", "zebra"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("Names[%d] = %q, want %q (full: %v)", i, got[i], w, got)
		}
	}
}

// TestDisabled_YAMLShape verifies the overlay matches what compose
// merge expects. A service's profiles list must REPLACE the base's
// profiles (compose default for arrays), so each entry only emits
// the profiles field — every other field falls through from base.
func TestDisabled_YAMLShape(t *testing.T) {
	d := &DisabledOverlay{Services: map[string]disabledEntry{}}
	d.Add("postgres")
	data, err := yaml.Marshal(d)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	// Should NOT contain image:, ports:, environment:, etc.
	for _, mustNot := range []string{"image:", "ports:", "environment:", "command:"} {
		if strings.Contains(string(data), mustNot) {
			t.Errorf("disabled overlay should only emit profiles, not %q:\n%s", mustNot, data)
		}
	}
}
