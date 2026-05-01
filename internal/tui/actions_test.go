package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/localk-dev/localk/internal/compose"
	"github.com/localk-dev/localk/internal/devmode"
)

// TestToggleDisable_AddsOpAndUpdatesRow covers the most-common path:
// pressing `d` on a normal row records a pending op and flips the
// row's flag for immediate visual feedback.
func TestToggleDisable_AddsOpAndUpdatesRow(t *testing.T) {
	m := newTestModel(t)
	m.cursor = indexOf(m, "api")
	if m.rows[m.cursor].Disabled {
		t.Fatal("setup: api should not be disabled to start")
	}

	m.toggleDisable()
	if !m.rows[m.cursor].Disabled {
		t.Errorf("expected api to be disabled after toggle")
	}
	if len(m.pending) != 1 || m.pending[0].kind != disableToggleOn || m.pending[0].service != "api" {
		t.Errorf("expected one disableToggleOn op for api; got %+v", m.pending)
	}

	// Toggling again flips it back AND records a new op (we don't
	// dedupe — replay-on-save handles the cancel-out semantics).
	m.toggleDisable()
	if m.rows[m.cursor].Disabled {
		t.Errorf("expected api to be re-enabled after second toggle")
	}
	if len(m.pending) != 2 || m.pending[1].kind != disableToggleOff {
		t.Errorf("expected second op to be disableToggleOff; got %+v", m.pending)
	}
}

// TestToggleDisable_RefusesDevModeService is the conflict guard from
// the typed `localk disable` command, mirrored in the TUI: services
// already in dev mode can't also be disabled.
func TestToggleDisable_RefusesDevModeService(t *testing.T) {
	m := newTestModel(t)
	idx := indexOf(m, "api")
	m.rows[idx].DevPort = 3000 // simulate already in dev mode
	m.cursor = idx

	m.toggleDisable()
	if m.rows[idx].Disabled {
		t.Error("disable should be refused for a dev-mode service")
	}
	if len(m.pending) != 0 {
		t.Errorf("no pending op should be recorded; got %+v", m.pending)
	}
	if m.flash == "" {
		t.Error("expected a flash warning explaining the conflict")
	}
}

// TestSave_PersistsToOverlays exercises the round-trip: take a model
// with pending changes, call save(), reload the overlays from disk,
// confirm they reflect the changes.
func TestSave_PersistsToOverlays(t *testing.T) {
	m := newTestModel(t)
	apiIdx := indexOf(m, "api")
	workerIdx := indexOf(m, "worker")

	// Simulate user actions: disable api, enter dev mode on worker @ 3001.
	m.cursor = apiIdx
	m.toggleDisable()

	m.cursor = workerIdx
	m.pendingDevService = "worker"
	m.port.SetValue("3001")
	m.confirmPortInput()

	if cmd := m.save(); cmd != nil {
		// Cmd return is allowed; we don't run it here. The fact that
		// save() returned without panicking and cleared pending is
		// what we're checking.
	}
	if m.dirty() {
		t.Errorf("expected pending to be cleared after save; got %d ops", len(m.pending))
	}

	// Reload overlays from disk and verify.
	disabled, _, err := devmode.LoadDisabled(m.disablePath)
	if err != nil {
		t.Fatalf("LoadDisabled: %v", err)
	}
	if !disabled.IsDisabled("api") {
		t.Errorf("expected api to be disabled in saved overlay; names: %v", disabled.Names())
	}

	dev, _, err := devmode.Load(m.devPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, inDev := dev.Services["worker"]; !inDev {
		t.Errorf("expected worker in dev overlay; got %v", dev.ProxyNames())
	}
}

// TestSave_ReplaysOpsInOrder covers the cancel-out case: disable
// then re-enable in one session leaves no trace in the overlay file.
// The replay semantics are what makes this work cleanly.
func TestSave_ReplaysOpsInOrder(t *testing.T) {
	m := newTestModel(t)
	m.cursor = indexOf(m, "api")
	m.toggleDisable() // disable
	m.toggleDisable() // re-enable

	m.save()

	// Either the file shouldn't exist (empty overlay → deleted), or
	// it exists but has no entries.
	if _, err := os.Stat(m.disablePath); !os.IsNotExist(err) {
		// File exists; verify it's empty.
		disabled, _, _ := devmode.LoadDisabled(m.disablePath)
		if len(disabled.Names()) != 0 {
			t.Errorf("expected empty disable overlay after cancel-out, got: %v", disabled.Names())
		}
	}
}

// TestRestoreFromDev_RemovesPort verifies the `r` action: given a
// row currently in dev mode, clears the port and records a devLeave
// op that save() will turn into RemoveProxy.
func TestRestoreFromDev_RemovesPort(t *testing.T) {
	m := newTestModel(t)
	idx := indexOf(m, "worker")
	m.rows[idx].DevPort = 4000 // simulate in dev mode
	m.cursor = idx

	m.restoreFromDev()
	if m.rows[idx].DevPort != 0 {
		t.Errorf("expected DevPort cleared after restore; got %d", m.rows[idx].DevPort)
	}
	if len(m.pending) != 1 || m.pending[0].kind != devLeave || m.pending[0].service != "worker" {
		t.Errorf("expected devLeave op for worker; got %+v", m.pending)
	}
}

// newTestModel produces a dashboardModel backed by real temp-dir files. Tests
// can mutate model state, call save(), and inspect the resulting
// overlay files on disk.
func newTestModel(t *testing.T) *dashboardModel {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(`
services:
  api:
    image: example/api:1.0
    ports: ["3000:3000"]
  worker:
    image: example/worker:1.0
`), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	m, err := newDashboardModel(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

func indexOf(m *dashboardModel, name string) int {
	for i, r := range m.rows {
		if r.Name == name {
			return i
		}
	}
	return -1
}

// Compile-time anchor for compose import — used implicitly via New.
var _ = compose.File{}
