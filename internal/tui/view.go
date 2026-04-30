package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// View renders the current model state. The dashboard layout is
// header / divider / list / divider / footer. Modal modes (filter,
// port input, confirm quit, help) overlay the footer area or the
// middle of the screen.
func (m *Model) View() string {
	var b strings.Builder

	b.WriteString(m.renderHeader())
	b.WriteString("\n")
	b.WriteString(m.renderTableHeader())
	b.WriteString("\n")
	b.WriteString(m.renderRows())
	b.WriteString("\n")
	b.WriteString(m.renderFooter())

	if m.mode == modeHelp {
		// Render help overlay below the dashboard. Simpler than a
		// floating box, fits any terminal width.
		b.WriteString("\n\n")
		b.WriteString(m.renderHelp())
	}
	return b.String()
}

func (m *Model) renderHeader() string {
	dirty := ""
	if m.dirty() {
		dirty = " " + dirtyMarkerStyle.Render(fmt.Sprintf("[%d unsaved]", len(m.pending)))
	}
	count := fmt.Sprintf("%d service(s)", len(m.rows))
	if filter := strings.TrimSpace(m.filter.Value()); filter != "" {
		count = fmt.Sprintf("%d / %d service(s) match %q", len(m.visible), len(m.rows), filter)
	}
	left := headerStyle.Render("localk") + subHeaderStyle.Render(" — "+m.outDir) + dirty
	right := subHeaderStyle.Render(count)
	return left + "  " + right
}

func (m *Model) renderTableHeader() string {
	return dividerStyle.Render(strings.Repeat("─", maxWidth(m.width, 80))) + "\n" +
		fmt.Sprintf("  %-12s %-32s %s", subHeaderStyle.Render("STATUS"), subHeaderStyle.Render("SERVICE"), subHeaderStyle.Render("IMAGE")) + "\n" +
		dividerStyle.Render(strings.Repeat("─", maxWidth(m.width, 80)))
}

func (m *Model) renderRows() string {
	if len(m.visible) == 0 {
		return "  " + subHeaderStyle.Render("(no services match filter)")
	}
	var lines []string
	for visIdx, rowIdx := range m.visible {
		row := m.rows[rowIdx]
		cursor := "  "
		if visIdx == m.cursor {
			cursor = cursorStyle.Render("▶ ")
		}
		status := renderStatus(row)
		name := row.Name
		image := truncate(row.Image, 40)
		line := fmt.Sprintf("%s%-12s %-32s %s", cursor, status, name, subHeaderStyle.Render(image))
		if visIdx == m.cursor {
			// Slight highlight on the cursor row's name; status keeps
			// its semantic color.
			line = fmt.Sprintf("%s%-12s %-32s %s", cursor, status, cursorStyle.Render(name), subHeaderStyle.Render(image))
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderStatus(row ServiceRow) string {
	switch {
	case row.DevPort != 0:
		return statusDev.Render(fmt.Sprintf("→ :%d", row.DevPort))
	case row.Disabled:
		return statusDisabled.Render("✗ disabled")
	case row.Running == "running":
		return statusRunning.Render("✓ up")
	case row.Running == "exited" || row.Running == "stopped":
		return statusStopped.Render("⊙ stopped")
	case row.Running != "":
		// Other states (created, restarting, paused, dead, etc).
		// Truncate so they fit the column.
		return statusStopped.Render(truncate(row.Running, 11))
	default:
		return statusEnabled.Render("✓ enabled")
	}
}

func (m *Model) renderFooter() string {
	switch m.mode {
	case modeFilter:
		return dividerStyle.Render(strings.Repeat("─", maxWidth(m.width, 80))) + "\n" +
			"/ " + m.filter.View() + "  " + footerStyle.Render("(enter to apply, esc to clear)")
	case modePortInput:
		return dividerStyle.Render(strings.Repeat("─", maxWidth(m.width, 80))) + "\n" +
			modalStyle.Render(fmt.Sprintf("Enter host port for %q (where you'll run it locally):\n%s", m.pendingDevService, m.port.View())) + "\n" +
			footerStyle.Render("(enter to confirm, esc to cancel)")
	case modeConfirmQuit:
		return dividerStyle.Render(strings.Repeat("─", maxWidth(m.width, 80))) + "\n" +
			flashStyle.Render(fmt.Sprintf("%d unsaved change(s).", len(m.pending))) + " " +
			footerStyle.Render("[y] save & quit  [n] discard & quit  [esc] cancel")
	}

	// Normal footer: status banner (if any) + key bindings.
	parts := []string{
		dividerStyle.Render(strings.Repeat("─", maxWidth(m.width, 80))),
	}
	if m.statusErr != "" {
		parts = append(parts, errorStyle.Render("docker compose ps: "+m.statusErr))
	}
	if m.flash != "" {
		parts = append(parts, flashStyle.Render(m.flash))
	}
	parts = append(parts, footerStyle.Render(
		"↑/↓ navigate  ·  d disable  ·  e dev  ·  r restore  ·  s save  ·  / filter  ·  ? help  ·  q quit",
	))
	return strings.Join(parts, "\n")
}

func (m *Model) renderHelp() string {
	body := strings.TrimSpace(`
KEYS

  ↑ / k          move cursor up
  ↓ / j          move cursor down
  g / home       jump to top
  G / end        jump to bottom

  d              toggle sticky disable on the current service
  e              enter dev mode on the current service
  r              restore from dev mode (only if currently in dev)
  s              save pending changes to overlays

  /              filter services by name
  esc            clear filter / close modal

  ?              toggle this help
  q / ctrl-c     quit (warns on unsaved changes)

STATUS INDICATORS

  ✓ up           enabled, container running
  ✓ enabled      enabled, container not running (stack down or unstarted)
  ⊙ stopped      enabled, container exited
  ✗ disabled     in docker-compose.disable.yml — won't start on up
  → :PORT        in dev mode — proxy forwards to host PORT

Press any key to dismiss this help.
`)
	return helpBoxStyle.Render(body)
}

// truncate keeps long strings (e.g., docker image paths with full
// registries) from blowing out the column. Adds an ellipsis when it
// trims so the user knows there's more.
func truncate(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	r := []rune(s)
	if n <= 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// maxWidth picks a sane horizontal width for dividers and banners.
// Falls back to a reasonable default if WindowSizeMsg hasn't fired
// yet (rare — Bubble Tea sends it on startup).
func maxWidth(w, fallback int) int {
	if w <= 0 {
		return fallback
	}
	return w
}

// Compile-time check: lipgloss.Style.Render returns string so we can
// concatenate freely. Listed here to silence unused-import warnings
// if a future refactor drops the only inline reference.
var _ = lipgloss.Style{}
