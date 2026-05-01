// Package tui implements `localk tui` — an interactive Bubble Tea
// shell that exposes the localk command surface (generate, dashboard,
// up, down) through a menu-driven UI. The TUI is the front door for
// humans; the typed commands stay in place for scripting.
//
// Architecture: a top-level Model owns a `screen` enum and routes
// Update/View to one of the sub-screens (menu, dashboard, generate
// wizard, action runner). Each sub-screen is its own type with its
// own Update/View methods; the top-level Model just dispatches.
package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// screen is the top-level state machine: which sub-screen is
// currently visible. Each value corresponds to one of the four
// sub-models below.
type screen int

const (
	screenMenu screen = iota
	screenDashboard
	screenGenerate
	screenAction
)

// Model is the Bubble Tea model passed to tea.NewProgram. Holds
// shared state (terminal size, default out-dir) plus one instance of
// each sub-screen's model. Sub-screens are constructed eagerly except
// for the dashboard, which loads compose + overlay files and only
// makes sense once the user navigates to it.
type Model struct {
	screen        screen
	width, height int
	// outDir threads through to every sub-screen that needs to know
	// where docker-compose.yml lives. Pre-filled from the --out-dir
	// flag on `localk tui`.
	outDir string

	menu     menuModel
	generate generateModel
	action   actionModel
	// dashboard is built lazily (on first entry to screenDashboard)
	// so `localk tui` can show the menu even when no compose file
	// exists yet — the missing-file error surfaces inline on the
	// dashboard screen instead of refusing to launch the TUI.
	dashboard *dashboardModel
	// dashboardErr is the last error from newDashboardModel, surfaced
	// inside screenDashboard when dashboard is nil. Cleared on
	// successful (re-)load.
	dashboardErr error
}

// New constructs the top-level TUI model. Always succeeds — even when
// no compose file exists at outDir. The dashboard's missing-file
// error is deferred until the user actually picks Dashboard from the
// menu.
func New(outDir string) *Model {
	return &Model{
		screen:   screenMenu,
		outDir:   outDir,
		menu:     newMenuModel(),
		generate: newGenerateModel(outDir),
	}
}

func (m *Model) Init() tea.Cmd {
	// The menu doesn't need an Init; sub-screens initialize on entry
	// (e.g., the dashboard fires its first poll when loaded).
	return nil
}
