// Package tui implements `localk tui` — an interactive Bubble Tea
// dashboard that replaces the typed `localk disable foo bar` /
// `localk dev foo --port 3000` workflows with arrow-key navigation
// and single-keystroke toggles.
//
// Read/write is symmetrical with the typed commands: the TUI mutates
// the same docker-compose.dev.yml and docker-compose.disable.yml
// overlays, so a session can move freely between TUI and CLI.
package tui

import (
	"github.com/charmbracelet/bubbles/textinput"

	"github.com/localk-dev/localk/internal/compose"
)

// ServiceRow is one row in the dashboard list. It bundles everything
// the renderer needs about a single service: the static facts from
// the base compose file, the desired state from the overlays, and the
// runtime state from `docker compose ps`.
type ServiceRow struct {
	Name     string
	Image    string
	Disabled bool   // sticky: in docker-compose.disable.yml
	DevPort  int    // 0 if not in dev mode; non-zero = host port the proxy forwards to
	Running  string // last-known state from `docker compose ps`: "running", "exited", "" (unknown)

	// Pre-computed for sort stability and case-insensitive filter
	// matching. Populated once at load.
	lowerName string
}

// dirtyOp records a single pending change made via the TUI but not
// yet persisted to the overlays. We collect these instead of mutating
// the overlay files on every keystroke so `q` (quit) can offer to
// discard, and `s` (save) is a single atomic operation per overlay.
type dirtyOp struct {
	kind    dirtyKind
	service string
	port    int // for devEnter
}

type dirtyKind int

const (
	disableToggleOn dirtyKind = iota
	disableToggleOff
	devEnter
	devLeave
)

// mode is the simple state machine inside the TUI. Default is normal
// list navigation; `/` enters filter, `e` enters port input, quitting
// with unsaved changes enters confirm.
type mode int

const (
	modeNormal mode = iota
	modeFilter
	modePortInput
	modeConfirmQuit
	modeHelp
)

// Model is the Bubble Tea model. Holds everything the Update/View
// functions need.
type Model struct {
	// Paths
	outDir      string
	composePath string
	devPath     string
	disablePath string

	// Static state from the base compose file. Used for image display
	// and as the source of truth for what services exist.
	base *compose.File

	// All rows in stable sort order (alphabetical by name). The
	// filtered view derives from this plus the filter string.
	rows []ServiceRow

	// Visible rows after filtering, as indices into rows. Empty filter
	// means visible == [0, 1, ..., len(rows)-1].
	visible []int

	// Cursor is an index into `visible` (NOT into rows directly), so
	// the cursor stays anchored to whatever's currently shown.
	cursor int

	// Pending changes; flushed by `s`. Order matters because we
	// replay them in sequence at save time, so a "disable then
	// re-enable" within one session correctly cancels out.
	pending []dirtyOp

	// Live runtime state per service name. Updated by tickMsg.
	runtime map[string]string // name -> "running" / "exited" / etc.

	// Mode + UI state
	mode      mode
	filter    textinput.Model // filter text input (mode == modeFilter)
	port      textinput.Model // port input (mode == modePortInput)
	statusErr string          // last `docker compose ps` error, if any
	flash     string          // one-shot footer message (e.g. conflict warnings)

	// pendingDevService captures which service we're prompting for a
	// port on (modePortInput). Cleared when the modal closes.
	pendingDevService string

	// Window dimensions, updated by tea.WindowSizeMsg.
	width, height int
}

// dirty reports whether there are any pending unsaved changes.
func (m *Model) dirty() bool { return len(m.pending) > 0 }

// currentRow returns the row currently under the cursor, or nil if
// the visible list is empty (which can happen when a filter matches
// nothing).
func (m *Model) currentRow() *ServiceRow {
	if len(m.visible) == 0 {
		return nil
	}
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return nil
	}
	return &m.rows[m.visible[m.cursor]]
}
