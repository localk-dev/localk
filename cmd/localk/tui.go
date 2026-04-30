package main

import (
	"errors"
	"flag"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/localk-dev/localk/internal/tui"
)

// runTui implements `localk tui` — the interactive Bubble Tea
// dashboard. Thin wrapper: parse --out-dir, build the model, run the
// program. Friendly error if the compose file isn't there yet.
func runTui(args []string) {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	outDir := fs.String("out-dir", ".", "directory containing the generated docker-compose.yml")

	args = reorderFlagsFirst(args)
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	model, err := tui.New(*outDir)
	if err != nil {
		// compose.LoadFile wraps os.ErrNotExist; surface a clear
		// "run generate first" hint instead of the raw error.
		if errors.Is(err, os.ErrNotExist) {
			abs, _ := filepath.Abs(filepath.Join(*outDir, "docker-compose.yml"))
			fail("tui: compose file not found at %s\n  run `localk generate <input>` first, or pass --out-dir to point at an existing one", abs)
		}
		fail("tui: %v", err)
	}

	// AltScreen so the TUI doesn't scroll the user's shell scrollback;
	// MouseAllMotion stays off — keyboard-only feel.
	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fail("tui: %v", err)
	}
}
