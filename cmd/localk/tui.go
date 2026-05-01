package main

import (
	"flag"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/localk-dev/localk/internal/tui"
)

// runTui implements `localk tui` — the interactive Bubble Tea front
// door. Thin wrapper: parse --out-dir, build the top-level Model,
// run the program. The TUI shows the menu first; the dashboard
// sub-screen surfaces any "run generate first" guidance inline if
// no compose file exists yet, so this entry no longer needs to
// pre-validate.
func runTui(args []string) {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	outDir := fs.String("out-dir", ".", "directory containing (or destined to contain) docker-compose.yml")

	args = reorderFlagsFirst(args)
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	model := tui.New(*outDir)

	// AltScreen so the TUI doesn't scroll the user's shell scrollback;
	// MouseAllMotion stays off — keyboard-only feel.
	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fail("tui: %v", err)
	}
}
