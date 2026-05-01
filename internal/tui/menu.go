package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// menuItem is one row in the top-level menu. The action field tells
// the parent Model which screen to switch to when Enter is pressed
// on this item.
type menuItem struct {
	label  string
	desc   string
	action menuAction
}

// menuAction enumerates what selecting a menu item does. We keep
// this as an enum (instead of, say, a closure) so the routing logic
// lives in one place — the parent Model's Update — and the menu
// itself stays a pure data structure that's easy to test.
type menuAction int

const (
	menuActionGenerate menuAction = iota
	menuActionDashboard
	menuActionUp
	menuActionDown
)

// menuModel is the top-level menu sub-screen. It's an index-based
// scrollable list (mirroring the dashboard's row-navigation style)
// rather than the heavier bubbles/list, because the menu is short
// and the look should match the rest of the TUI.
type menuModel struct {
	items  []menuItem
	cursor int

	// chosen is set by Update when the user presses Enter; the parent
	// Model reads it in its Update wrapper to switch screens, then
	// clears it (via reset()) before the next entry to the menu.
	chosen *menuAction
}

func newMenuModel() menuModel {
	return menuModel{
		items: []menuItem{
			{label: "Generate", desc: "convert manifests → docker-compose.yml", action: menuActionGenerate},
			{label: "Dashboard", desc: "manage services (dev mode, disable)", action: menuActionDashboard},
			{label: "Up", desc: "start the stack (docker compose up -d)", action: menuActionUp},
			{label: "Down", desc: "stop the stack (docker compose down)", action: menuActionDown},
		},
	}
}

// reset returns the menu to its initial state. Called by the parent
// Model after consuming a `chosen` action so re-entering the menu
// doesn't immediately re-fire the previous selection.
func (m *menuModel) reset() {
	m.chosen = nil
}

// Update handles key input on the menu screen. Returns the (possibly
// mutated) menu and a Cmd. Sets m.chosen on Enter so the parent
// Model can route to the next screen.
func (m menuModel) Update(msg tea.KeyMsg) (menuModel, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.items)-1 {
			m.cursor++
		}
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = len(m.items) - 1
	case "enter":
		// Capture the chosen action; the parent Model will dispatch.
		a := m.items[m.cursor].action
		m.chosen = &a
	}
	return m, nil
}

// View renders the menu. The header matches the dashboard's style
// so navigating between screens feels continuous.
func (m menuModel) View(outDir string, width int) string {
	var b strings.Builder

	// Header
	left := headerStyle.Render("localk") + subHeaderStyle.Render(" — "+outDir)
	right := subHeaderStyle.Render(fmt.Sprintf("%d action(s)", len(m.items)))
	b.WriteString(left + "  " + right)
	b.WriteString("\n")
	b.WriteString(dividerStyle.Render(strings.Repeat("─", maxWidth(width, 80))))
	b.WriteString("\n")

	// Items
	for i, it := range m.items {
		cursor := "  "
		labelStyled := it.label
		if i == m.cursor {
			cursor = cursorStyle.Render("▶ ")
			labelStyled = cursorStyle.Render(it.label)
		}
		b.WriteString(fmt.Sprintf("%s%-12s %s", cursor, labelStyled, subHeaderStyle.Render(it.desc)))
		b.WriteString("\n")
	}

	// Footer
	b.WriteString(dividerStyle.Render(strings.Repeat("─", maxWidth(width, 80))))
	b.WriteString("\n")
	b.WriteString(footerStyle.Render("↑/↓ navigate  ·  enter select  ·  q quit"))
	return b.String()
}
